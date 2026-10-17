package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/getspas/spas/internal/gitexec"
	"github.com/getspas/spas/internal/interaction"
	"github.com/getspas/spas/internal/presentation"
	"github.com/getspas/spas/internal/provider"
)

// Native execution verifies application phases with a real checkout and remote.
func TestNativeTerminalSyncProgress(t *testing.T) {
	if !interaction.IsTerminal(os.Stdout) {
		t.Skip("stdout is not a native terminal")
	}
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("CI", "")
	t.Setenv("NO_COLOR", "1")
	workspace, _, instance := initializedApp(t, t.TempDir())
	instance.Provider = slowVisibilityProvider{instance.Provider}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	enableGitProxy(t, "slow-inspection")
	t.Setenv("SPAS_APP_REAL_GIT", realGit)
	instance.Git.Path = os.Args[0]
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		if canceled {
			cancel()
		}
		var out bytes.Buffer
		p := presentation.New("never", false, false, &out, os.Stdout)
		p.StartProgress(ctx, "auto", false, false, os.Stdout)
		instance.Presenter = p
		instance.Out, instance.Err = p.Writer(&out), p.Writer(os.Stdout)
		instance.Git.Stdout, instance.Git.Stderr = instance.Out, instance.Err
		instance.Git.BeforeTerminal = func() func() { return p.Pause(true) }
		err := instance.Sync(ctx, SyncOptions{Conflict: ConflictAbort, ExistingExclude: ExcludePreserve, MergeProtection: MergeEnable})
		p.Stop()
		cancel()
		if canceled {
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled sync: %v", err)
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(out.Bytes(), []byte("\x1b")) || !bytes.Contains(out.Bytes(), []byte("synchronized: true")) {
				t.Fatalf("result stdout: %q", out.String())
			}
			content, err := os.ReadFile(filepath.Join(workspace, "docs", "ARCHITECTURE.md"))
			if err != nil || string(content) != "initial\n" {
				t.Fatalf("asset contents %q: %v", content, err)
			}
		}
	}
}

type slowVisibilityProvider struct{ provider.RepositoryProvider }

func (p slowVisibilityProvider) ProbePublic(ctx context.Context, git gitexec.Runner, ref provider.RepositoryRef) (bool, error) {
	timer := time.NewTimer(350 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
		return p.RepositoryProvider.ProbePublic(ctx, git, ref)
	case <-ctx.Done():
		return false, ctx.Err()
	}
}
