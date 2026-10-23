package backup

import (
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
)

// Metadata must be a bounded regular file, never a blocking FIFO/device or a
// followed link. Directory containment remains the caller's responsibility.
func ReadMetadataFile(path string, limit int64) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() > limit {
		return nil, fmt.Errorf("元数据文件类型或大小无效: %s", path)
	}
	// The regular file's size is already known. Read it once into one bounded
	// allocation, with one extra byte to detect growth rather than repeatedly
	// growing/copying io.ReadAll's buffer for large manifests.
	if before.Size() > int64(int(^uint(0)>>1))-1 {
		return nil, fmt.Errorf("元数据文件过大: %s", path)
	}
	b := make([]byte, int(before.Size())+1)
	n, err := io.ReadFull(f, b)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if int64(n) != before.Size() || !samePhysicalVersion(before, after) {
		return nil, fmt.Errorf("读取期间元数据文件发生变化: %s", path)
	}
	return b[:n], nil
}
