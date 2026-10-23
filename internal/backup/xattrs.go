package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

func AttributeHash(path string) (string, error) {
	n, e := unix.Llistxattr(path, nil)
	if errors.Is(e, unix.ENOTSUP) {
		return "", nil
	}
	if e != nil {
		return "", e
	}
	if n == 0 {
		return "", nil
	}
	b := make([]byte, n)
	n, e = unix.Llistxattr(path, b)
	if e != nil {
		return "", e
	}
	names := strings.Split(strings.TrimRight(string(b[:n]), "\x00"), "\x00")
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		n, e = unix.Lgetxattr(path, name, nil)
		if e != nil {
			return "", e
		}
		value := make([]byte, n)
		n, e = unix.Lgetxattr(path, name, value)
		if e != nil {
			return "", e
		}
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write(value[:n])
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// CopyAttributes is only used for freshly created directories, symlinks or copied
// files, never a hardlink shared with a committed generation.
func CopyAttributes(from, to string) error {
	n, e := unix.Llistxattr(from, nil)
	if errors.Is(e, unix.ENOTSUP) {
		return nil
	}
	if e != nil {
		return e
	}
	if n == 0 {
		return nil
	}
	list := make([]byte, n)
	n, e = unix.Llistxattr(from, list)
	if e != nil {
		return e
	}
	for _, name := range strings.Split(strings.TrimRight(string(list[:n]), "\x00"), "\x00") {
		size, e := unix.Lgetxattr(from, name, nil)
		if e != nil {
			return e
		}
		value := make([]byte, size)
		size, e = unix.Lgetxattr(from, name, value)
		if e != nil {
			return e
		}
		if e = unix.Lsetxattr(to, name, value[:size], 0); e != nil {
			return e
		}
	}
	return nil
}
