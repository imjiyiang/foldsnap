package server

import "syscall"

func diskSpace(path string) (uint64, uint64) {
	var s syscall.Statfs_t
	if syscall.Statfs(path, &s) != nil {
		return 0, 0
	}
	return s.Blocks * uint64(s.Bsize), s.Bavail * uint64(s.Bsize)
}
