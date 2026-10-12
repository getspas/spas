package gitexec

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/getspas/spas/internal/limits"
	"golang.org/x/sys/windows"
)

func configureCancellation(cmd *exec.Cmd, nonInteractive bool) {
	if !nonInteractive {
		return
	}
	cmd.Cancel = func() error {
		// taskkill's tree option terminates SSH and credential-helper children
		// before their parent disappears. Resolve the system executable rather
		// than trusting a workspace or PATH entry.
		dir, err := windows.GetSystemDirectory()
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), limits.GitCommandWaitDelay)
			defer cancel()
			kill := exec.CommandContext(ctx, filepath.Join(dir, "taskkill.exe"), "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
			if err := kill.Run(); err == nil {
				return nil
			}
		}
		// Retain direct-process cancellation if tree termination is unavailable.
		return cmd.Process.Kill()
	}
}
