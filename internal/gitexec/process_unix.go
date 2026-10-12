//go:build darwin || linux

package gitexec

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureCancellation(cmd *exec.Cmd, nonInteractive bool) {
	if !nonInteractive {
		return
	}
	// Keep unattended Git and its SSH/helper descendants in a process group
	// so cancellation releases their pipes, locks, and network connections.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
