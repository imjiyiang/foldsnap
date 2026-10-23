//go:build darwin

package backup

import (
	"golang.org/x/sys/unix"
	"syscall"
)

func statTimes(st *syscall.Stat_t) (int64, int64) {
	return st.Ctimespec.Nano(), st.Birthtimespec.Nano()
}
func cacheFilesystem(path string) (string, bool) {
	var st unix.Statfs_t
	if unix.Statfs(path, &st) != nil {
		return "unknown", false
	}
	n := unix.ByteSliceToString(st.Fstypename[:])
	return n, n == "apfs"
}
