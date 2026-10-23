package store

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
)

func fingerprintKey(path string) ([]byte, error) {
	file := filepath.Join(filepath.Dir(path), "fingerprint-integrity.key")
	fd, err := unix.Open(file, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err == nil {
		f := os.NewFile(uintptr(fd), file)
		st, e := f.Stat()
		if e != nil {
			f.Close()
			return nil, e
		}
		if !st.Mode().IsRegular() {
			f.Close()
			return nil, fmt.Errorf("缓存完整性凭据类型无效")
		}
		key, e := io.ReadAll(io.LimitReader(f, 33))
		f.Close()
		if e != nil {
			return nil, e
		}
		if len(key) == 32 {
			return key, nil
		}
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	// This key authenticates disposable cache records; it never encrypts backups.
	// Atomic replacement after a truncated-key crash invalidates old cache tags.
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".fingerprint-key-")
	if err != nil {
		return nil, err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(key); err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if err = os.Rename(name, file); err != nil {
		return nil, err
	}
	return key, nil
}
func (s *Store) cacheTag(key, hash string) string {
	if len(s.fingerprintKey) != 32 {
		return ""
	}
	m := hmac.New(sha256.New, s.fingerprintKey)
	m.Write([]byte("fingerprint-v1\x00" + key + "\x00" + hash))
	return hex.EncodeToString(m.Sum(nil))
}
func (s *Store) validCacheTag(key, hash, tag string) bool {
	if len(s.fingerprintKey) != 32 || len(tag) != 64 {
		return false
	}
	expected := s.cacheTag(key, hash)
	return hmac.Equal([]byte(expected), []byte(tag))
}
