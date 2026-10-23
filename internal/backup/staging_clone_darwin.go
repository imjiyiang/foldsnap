//go:build darwin

package backup

import "golang.org/x/sys/unix"

// A clone has a separate inode: later writes to the interrupted tree cannot
// modify the published snapshot. Unsupported filesystems fall back to copying.
func cloneStagedFile(from, to string) bool {
	return unix.Clonefile(from, to, unix.CLONE_NOFOLLOW) == nil
}
