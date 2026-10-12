package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getspas/spas/internal/linkstate"
	"github.com/getspas/spas/internal/lock"
	"github.com/getspas/spas/internal/spaserr"
)

func TestSyncInterruptedPushRetainsRecoveryAndReleasesLock(t *testing.T) {
	for _, failure := range []string{"cancel", "timeout", "authentication"} {
		t.Run(failure, func(t *testing.T) {
			instance, workspace, _, remote := fixture(t)
			asset := filepath.Join(workspace, "personal.bin")
			contents := []byte{0, 1, 255, 10}
			if err := os.WriteFile(asset, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := instance.Add(t.Context(), AddOptions{Paths: []string{"personal.bin"}, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip}); err != nil {
				t.Fatal(err)
			}
			realGit, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("SPAS_APP_REAL_GIT", realGit)
			ready := filepath.Join(t.TempDir(), "push-ready")
			t.Setenv("SPAS_APP_PUSH_READY", ready)
			mode := "stall-push"
			if failure == "authentication" {
				mode = "auth-fail-push"
			}
			enableGitProxy(t, mode)
			instance.Git.Path = os.Args[0]
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- instance.Sync(ctx, syncOptions("save personal file")) }()
			if failure != "authentication" {
				for {
					if _, err := os.Stat(ready); err == nil {
						break
					}
					select {
					case err := <-done:
						t.Fatalf("sync exited before push: %v", err)
					case <-ctx.Done():
						t.Fatal("sync did not reach push")
					case <-time.After(10 * time.Millisecond):
					}
				}
			}
			if failure == "cancel" {
				cancel()
			}
			err = <-done
			switch failure {
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("Sync error = %v", err)
				}
			case "timeout":
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("Sync error = %v", err)
				}
			case "authentication":
				if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindAuthNetwork || !strings.Contains(err.Error(), "controlled authentication failure") {
					t.Fatalf("Sync error = %v", err)
				}
			}
			state := loadState(t, instance, workspace)
			if state.Materializing == nil || state.Materializing.Phase != linkstate.MaterializationPushPending {
				t.Fatalf("missing pending push: %+v", state.Materializing)
			}
			l, err := lock.Acquire(filepath.Join(instance.Store.DataDir, "locks"), state.LinkID)
			if err != nil {
				t.Fatal(err)
			}
			if err := l.Release(); err != nil {
				t.Fatal(err)
			}
			instance.Git.Path = realGit
			t.Setenv("SPAS_APP_GIT_PROXY", "")
			if err := instance.Sync(t.Context(), syncOptions("resume")); err != nil {
				t.Fatalf("resume failed: %v", err)
			}
			if state := loadState(t, instance, workspace); state.Materializing != nil {
				t.Fatal("recovery not cleared after resume")
			}
			result, err := instance.Git.Run(t.Context(), workspace, "--git-dir="+remote, "show", "main:personal.bin")
			if err != nil || !bytes.Equal(result.Stdout, contents) {
				t.Fatalf("published binary = %q, err=%v", result.Stdout, err)
			}
		})
	}
}
