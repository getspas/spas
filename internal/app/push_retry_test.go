package app

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPushRetryConflictCanContinueAfterReload(t *testing.T) {
	instance, workspace, root, remote := fixture(t)
	enroll(t, instance, workspace, map[string]string{"asset.txt": "original\n"})
	before := strings.TrimSpace(gitOutput(t, root, "--git-dir="+remote, "rev-parse", "main"))
	teammatePush(t, root, remote, map[string]string{"asset.txt": "remote edit\n"}, nil)
	remoteHead := strings.TrimSpace(gitOutput(t, root, "--git-dir="+remote, "rev-parse", "main"))
	runGit(t, root, "--git-dir="+remote, "update-ref", "refs/heads/main", before)
	asset := filepath.Join(workspace, "asset.txt")
	if err := os.WriteFile(asset, []byte("local edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPAS_APP_REAL_GIT", realGit)
	t.Setenv("SPAS_APP_REMOTE", remote)
	t.Setenv("SPAS_APP_REMOTE_HEAD", remoteHead)
	t.Setenv("SPAS_APP_PUSH_READY", filepath.Join(root, "advanced"))
	enableGitProxy(t, "advance-before-push")
	instance.Git.Path = os.Args[0]
	if err := instance.Sync(t.Context(), syncOptions("Local edit")); !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("concurrent push conflict = %v", err)
	}
	if got := gitOutput(t, root, "--git-dir="+remote, "show", "main:asset.txt"); got != "remote edit\n" {
		t.Fatalf("conflicting push changed remote asset: %q", got)
	}
	if err := os.WriteFile(asset, []byte("resolved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Continue from persisted recovery state using a fresh application instance.
	instance, _ = testApp(t, workspace, root, remote)
	options := syncOptions("Resolve conflict")
	options.Continue = true
	if err := instance.Sync(t.Context(), options); err != nil {
		t.Fatalf("continue push retry conflict: %v", err)
	}
	if got := gitOutput(t, root, "--git-dir="+remote, "show", "main:asset.txt"); got != "resolved\n" {
		t.Fatalf("published resolution = %q", got)
	}
	if got := strings.TrimSpace(gitOutput(t, root, "--git-dir="+remote, "rev-list", "--count", before+"..main")); got != "3" {
		t.Fatalf("new commits = %s, want local edit, remote edit, and resolution", got)
	}
	if state := loadState(t, instance, workspace); state.ActiveMerge != nil || state.Materializing != nil {
		t.Fatalf("completed continuation retained recovery state: %+v", state)
	}
}
