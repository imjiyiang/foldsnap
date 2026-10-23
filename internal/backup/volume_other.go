//go:build !darwin

package backup

import (
	"fmt"
	"syscall"
)

func VolumeID(path string) (string, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return "", err
	}
	return fmt.Sprintf("device-%d", st.Dev), nil
}
