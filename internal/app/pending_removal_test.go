package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/getspas/spas/internal/linkstate"
	"github.com/getspas/spas/internal/pathmodel"
)

func TestAddAndRemoveRejectActivePrivateMergeState(t *testing.T) {
	t.Parallel()

	instance, _, _, _ := fixture(t)
	state := loadState(t, instance, instance.RepoHint)
	state.Private.Initialized = true
	state.Private.ExpectedHead = strings.Repeat("a", 40)
	state.ActiveMerge = validTestActiveMerge()
	saveState(t, instance, state)

	if err := instance.Add(context.Background(), AddOptions{Paths: []string{".env"}}); err == nil || !strings.Contains(err.Error(), "private merge is in progress") {
		t.Fatalf("Add() error = %v, want active-merge rejection", err)
	}
	if err := instance.Remove(context.Background(), RemoveOptions{Paths: []string{".env"}, MissingOK: true}); err == nil || !strings.Contains(err.Error(), "private merge is in progress") {
		t.Fatalf("Remove() error = %v, want active-merge rejection", err)
	}
}

func TestAddCancelsPendingRemoval(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, _, instance := initializedApp(t, root)
	path := filepath.Join(publicRoot, "docs", "ARCHITECTURE.md")
	if err := instance.Remove(ctx, RemoveOptions{Paths: []string{"docs/ARCHITECTURE.md"}}); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	state := loadState(t, instance, publicRoot)
	if len(state.PendingRemoves) != 1 {
		t.Fatalf("PendingRemoves after Remove() = %v, want one removal", state.PendingRemoves)
	}
	if err := instance.Add(ctx, AddOptions{
		Paths:           []string{"docs/ARCHITECTURE.md"},
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	}); err != nil {
		t.Fatalf("Add() re-enrollment error = %v", err)
	}
	state = loadState(t, instance, publicRoot)
	if len(state.PendingRemoves) != 0 {
		t.Fatalf("PendingRemoves after re-enrollment = %v, want empty", state.PendingRemoves)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("re-enrolled workspace file missing: %v", err)
	}
}

func TestPlanLocalChangesDefersRemovalAfterExecutableModeChange(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose a portable executable-bit test")
	}

	root := t.TempDir()
	publicRoot := filepath.Join(root, "public")
	privateRoot := filepath.Join(root, "private")
	if err := os.MkdirAll(publicRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("TOKEN=1\n")
	publicFile := filepath.Join(publicRoot, ".env")
	if err := os.WriteFile(publicFile, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(publicFile, 0o700); err != nil {
		t.Fatal(err)
	}
	path, err := pathmodel.Parse(".env")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)

	plan, err := planLocalChanges(
		publicRoot,
		privateRoot,
		[]pathmodel.Path{path},
		nil,
		map[string]struct{}{path.String(): {}},
		map[string]linkstate.PendingRemoval{
			path.String(): {
				Path:       path.String(),
				Digest:     hex.EncodeToString(digest[:]),
				Existed:    true,
				Executable: false,
			},
		},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("planLocalChanges() error = %v", err)
	}
	if len(plan.Changes) != 0 || len(plan.DeferredRemovals) != 1 || plan.DeferredRemovals[0] != path {
		t.Fatalf("planLocalChanges() = changes %v deferred removals %v, want mode-only deferred removal", plan.Changes, plan.DeferredRemovals)
	}
}
