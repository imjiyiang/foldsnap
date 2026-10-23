//go:build linux

package backup

import "syscall"

func statTimes(st *syscall.Stat_t) (int64, int64) { return st.Ctim.Nano(), 0 }

// Device numbers are not persistent volume UUIDs on Linux. Keep the index but
// do not persistently reuse hashes under an unverified volume identity.
func cacheFilesystem(path string) (string, bool) { return "linux-unverified-volume", false }
