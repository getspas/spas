package gitexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/getspas/spas/internal/lock"
)

func TestUnattendedCancellationReleasesDescendantResources(t *testing.T) {
	for _, method := range []string{"captured", "streamed", "input"} {
		t.Run(method, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("SPAS_GITEXEC_HELPER", "locked-descendant-parent")
			t.Setenv("SPAS_GITEXEC_LOCK_DIR", dir)
			t.Setenv("GORACE", "atexit_sleep_ms=0")
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				r := Runner{Path: os.Args[0], NonInteractive: true}
				args := []string{"-test.run=^TestGitExecHelperProcess$"}
				var err error
				switch method {
				case "captured":
					_, err = r.Run(ctx, dir, args...)
				case "streamed":
					_, err = r.RunStreaming(ctx, dir, args...)
				case "input":
					_, err = r.RunInput(ctx, dir, strings.NewReader("data"), args...)
				}
				done <- err
			}()
			ready := filepath.Join(dir, "ready")
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("child exited before ready: %v", err)
				case <-ctx.Done():
					t.Fatal("child did not become ready")
				case <-time.After(10 * time.Millisecond):
				}
			}
			// Register cleanup before asserting so a failing regression leaves no child.
			raw, err := os.ReadFile(ready)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if p, err := os.FindProcess(pid); err == nil {
					_ = p.Kill()
				}
			})
			if l, err := lock.Acquire(dir, "descendant"); err == nil {
				_ = l.Release()
				t.Fatal("descendant did not hold its lock")
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation = %v", err)
			}
			l, err := lock.Acquire(dir, "descendant")
			if err != nil {
				t.Fatalf("descendant still holds resource after cancellation: %v", err)
			}
			if err := l.Release(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
