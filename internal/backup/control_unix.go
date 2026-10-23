//go:build darwin || linux

package backup

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func groupSignal(pid int, sig syscall.Signal) error {
	err := syscall.Kill(-pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
func pauseGroup(pid int) error  { return groupSignal(pid, syscall.SIGSTOP) }
func resumeGroup(pid int) error { return groupSignal(pid, syscall.SIGCONT) }
func configureGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
