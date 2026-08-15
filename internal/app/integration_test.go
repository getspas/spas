package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/getspas/spas/internal/filesync"
	"github.com/getspas/spas/internal/gitexec"
	"github.com/getspas/spas/internal/githubref"
	"github.com/getspas/spas/internal/interaction"
	"github.com/getspas/spas/internal/linkstate"
	"github.com/getspas/spas/internal/pathmodel"
	"github.com/getspas/spas/internal/provider"
)

func TestEndToEndEmptyPrivateRepository(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot := filepath.Join(root, "public")
	remote := filepath.Join(root, "private.git")
	if err := os.MkdirAll(publicRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "--bare", "-q", remote)
	runGit(t, publicRoot, "init", "-q", "-b", "main")
	runGit(t, publicRoot, "config", "user.name", "SPAS Test")
	runGit(t, publicRoot, "config", "user.email", "spas@example.invalid")
	if err := os.WriteFile(filepath.Join(publicRoot, "README.md"), []byte("public\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, publicRoot, "add", "README.md")
	runGit(t, publicRoot, "commit", "-q", "-m", "public initial")
	if err := os.WriteFile(filepath.Join(publicRoot, ".env"), []byte("TOKEN=test-only\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	instance, output := testApp(t, publicRoot, root, remote)
	if err := instance.Link(ctx, LinkOptions{
		Repository: "getspas/private-files",
		Branch:     "main",
	}); err != nil {
		t.Fatalf("Link() error = %v", err)
	}
	if err := instance.Add(ctx, AddOptions{
		Paths:           []string{".env"},
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if err := instance.Sync(ctx, SyncOptions{
		Message:         "Add private environment",
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
		Branch:          "main",
	}); err != nil {
		t.Fatalf("Sync() error = %v\noutput:\n%s", err, output.String())
	}

	privateContent := gitOutput(t, root, "--git-dir="+remote, "show", "main:.env")
	if privateContent != "TOKEN=test-only\n" {
		t.Fatalf("private .env = %q", privateContent)
	}
	status := gitOutput(t, publicRoot, "status", "--porcelain", "--untracked-files=all")
	if status != "" {
		t.Fatalf("public git status = %q, want clean", status)
	}

	if err := os.Remove(filepath.Join(publicRoot, ".env")); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	}); err != nil {
		t.Fatalf("restore Sync() error = %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(publicRoot, ".env")); err != nil || string(content) != "TOKEN=test-only\n" {
		t.Fatalf("restored .env = %q, %v", content, err)
	}

	if err := os.WriteFile(filepath.Join(publicRoot, ".env"), []byte("TOKEN=updated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(ctx, SyncOptions{
		Message:         "Update private environment",
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	}); err != nil {
		t.Fatalf("update Sync() error = %v", err)
	}
	if got := gitOutput(t, root, "--git-dir="+remote, "show", "main:.env"); got != "TOKEN=updated\n" {
		t.Fatalf("updated private .env = %q", got)
	}

	if err := instance.Remove(ctx, RemoveOptions{Paths: []string{".env"}}); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if err := instance.Sync(ctx, SyncOptions{
		Message:         "Remove private environment",
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	}); err != nil {
		t.Fatalf("removal Sync() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(publicRoot, ".env")); !os.IsNotExist(err) {
		t.Fatalf(".env still exists after private removal: %v", err)
	}
}

func TestAddDirectoryEnrollsCurrentGitignoredFilesOnly(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot := filepath.Join(root, "public")
	remote := filepath.Join(root, "private.git")
	if err := os.MkdirAll(publicRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "--bare", "-q", remote)
	runGit(t, publicRoot, "init", "-q", "-b", "main")
	runGit(t, publicRoot, "config", "user.name", "SPAS Test")
	runGit(t, publicRoot, "config", "user.email", "spas@example.invalid")
	if err := os.MkdirAll(filepath.Join(publicRoot, ".secret"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(publicRoot, ".gitignore"), []byte(".secret/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"one.env": "one\n", "two.json": "two\n"} {
		if err := os.WriteFile(filepath.Join(publicRoot, ".secret", name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, publicRoot, "add", ".gitignore")
	runGit(t, publicRoot, "commit", "-q", "-m", "public initial")

	instance, _ := testApp(t, publicRoot, root, remote)
	if err := instance.Link(ctx, LinkOptions{Repository: "getspas/private-files", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := instance.Add(ctx, AddOptions{
		Paths:           []string{".secret"},
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(publicRoot, ".secret", "later.txt"), []byte("later\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	repository, state, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".secret/one.env", ".secret/two.json"}
	if strings.Join(state.PendingAdds, ",") != strings.Join(want, ",") {
		t.Fatalf("pending additions = %v, want %v", state.PendingAdds, want)
	}
	excludePath, err := repository.InfoExcludePath(ctx)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "/.secret/\n") || strings.Contains(string(content), "later.txt") {
		t.Fatalf("local exclude claimed the directory or future file:\n%s", content)
	}
	for _, path := range want {
		if !strings.Contains(string(content), "/"+path+"\n") {
			t.Fatalf("local exclude does not contain exact path %q:\n%s", path, content)
		}
	}
}

func TestAddRejectsWhenPublicGitignoreReincludesPath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot := filepath.Join(root, "public")
	remote := filepath.Join(root, "private.git")
	if err := os.MkdirAll(publicRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "--bare", "-q", remote)
	runGit(t, publicRoot, "init", "-q", "-b", "main")
	runGit(t, publicRoot, "config", "user.name", "SPAS Test")
	runGit(t, publicRoot, "config", "user.email", "spas@example.invalid")
	if err := os.WriteFile(filepath.Join(publicRoot, ".gitignore"), []byte("!.env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(publicRoot, ".env"), []byte("private\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, publicRoot, "add", ".gitignore")
	runGit(t, publicRoot, "commit", "-q", "-m", "public initial")

	instance, _ := testApp(t, publicRoot, root, remote)
	if err := instance.Link(ctx, LinkOptions{Repository: "getspas/private-files", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	err := instance.Add(ctx, AddOptions{
		Paths:           []string{".env"},
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	})
	if err == nil || !strings.Contains(err.Error(), "not effectively excluded") {
		t.Fatalf("Add() error = %v, want effective-exclusion failure", err)
	}
	repository, _, linkedErr := instance.linked(ctx)
	if linkedErr != nil {
		t.Fatal(linkedErr)
	}
	excludePath, pathErr := repository.InfoExcludePath(ctx)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	content, readErr := os.ReadFile(excludePath)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if strings.Contains(string(content), "BEGIN SPAS") {
		t.Fatalf("failed add left a SPAS exclude block:\n%s", content)
	}
}

func TestRemoteOnlyUpdateMaterializesToPublicWorkspace(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, remote, instance := initializedApp(t, root)

	other := filepath.Join(root, "other")
	runGit(t, root, "clone", "-q", remote, other)
	runGit(t, other, "config", "user.name", "Other Test")
	runGit(t, other, "config", "user.email", "other@example.invalid")
	if err := os.WriteFile(filepath.Join(other, "docs", "ARCHITECTURE.md"), []byte("remote update\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, "add", "docs/ARCHITECTURE.md")
	runGit(t, other, "commit", "-q", "-m", "remote update")
	runGit(t, other, "push", "-q", "origin", "main")

	if err := instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	}); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	content, err := os.ReadFile(filepath.Join(publicRoot, "docs", "ARCHITECTURE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "remote update\n" {
		t.Fatalf("public architecture = %q", content)
	}
}

func TestTrackedPathOverrideStagesPublicRemovalWithoutPublicCommit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, remote, instance := initializedApp(t, root)
	privateFile := filepath.Join(publicRoot, "docs", "ARCHITECTURE.md")
	if err := os.WriteFile(privateFile, []byte("public version\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, publicRoot, "add", "-f", "docs/ARCHITECTURE.md")
	runGit(t, publicRoot, "commit", "-q", "-m", "public architecture")
	publicHeadBefore := strings.TrimSpace(gitOutput(t, publicRoot, "rev-parse", "HEAD"))

	if err := instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictOverride,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	}); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	publicHeadAfter := strings.TrimSpace(gitOutput(t, publicRoot, "rev-parse", "HEAD"))
	if publicHeadAfter != publicHeadBefore {
		t.Fatalf("public HEAD changed from %s to %s", publicHeadBefore, publicHeadAfter)
	}
	if content, err := os.ReadFile(privateFile); err != nil || string(content) != "initial\n" {
		t.Fatalf("workspace private file = %q, %v", content, err)
	}
	status := gitOutput(t, publicRoot, "status", "--porcelain=v1")
	if !strings.Contains(status, "D  docs/ARCHITECTURE.md") {
		t.Fatalf("public status = %q, want staged deletion", status)
	}
	if got := gitOutput(t, root, "--git-dir="+remote, "show", "main:docs/ARCHITECTURE.md"); got != "initial\n" {
		t.Fatalf("private remote file = %q", got)
	}
}

func TestTrackedPathOverrideWithExplicitDiscardPreservesApprovalSnapshot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, _, instance := initializedApp(t, root)
	privateFile := filepath.Join(publicRoot, "docs", "ARCHITECTURE.md")
	if err := os.WriteFile(privateFile, []byte("public version\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, publicRoot, "add", "-f", "docs/ARCHITECTURE.md")
	runGit(t, publicRoot, "commit", "-q", "-m", "public architecture")
	if err := os.WriteFile(privateFile, []byte("dirty public version\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := instance.Sync(ctx, SyncOptions{
		Conflict:             ConflictOverride,
		DiscardPublicChanges: true,
		ExistingExclude:      ExcludePreserve,
		MergeProtection:      MergeEnable,
	}); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if content, err := os.ReadFile(privateFile); err != nil || string(content) != "initial\n" {
		t.Fatalf("workspace private file = %q, %v", content, err)
	}
	status := gitOutput(t, publicRoot, "status", "--porcelain=v1")
	if !strings.Contains(status, "D  docs/ARCHITECTURE.md") {
		t.Fatalf("public status = %q, want staged deletion", status)
	}
}

func TestSkippedTrackedPathRemainsAConflictOnNextSync(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, remote, instance := initializedApp(t, root)
	privateFile := filepath.Join(publicRoot, "docs", "ARCHITECTURE.md")
	runGit(t, publicRoot, "add", "-f", "docs/ARCHITECTURE.md")
	runGit(t, publicRoot, "commit", "-q", "-m", "public architecture")

	if err := instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictSkip,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	}); err != nil {
		t.Fatalf("skip Sync() error = %v", err)
	}
	repository, state, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ManagedPaths) != 0 {
		t.Fatalf("managed paths after skip = %v, want none", state.ManagedPaths)
	}
	if got := gitOutput(t, root, "--git-dir="+remote, "show", "main:docs/ARCHITECTURE.md"); got != "initial\n" {
		t.Fatalf("private remote file = %q", got)
	}
	if content, err := os.ReadFile(privateFile); err != nil || string(content) != "initial\n" {
		t.Fatalf("public file changed during skip: %q, %v", content, err)
	}

	err = instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	})
	if err == nil || !strings.Contains(err.Error(), "tracked-path conflict") {
		t.Fatalf("next Sync() error = %v, want repeated tracked-path conflict", err)
	}
	if _, err := repository.PathStatus(ctx, mustPath(t, "docs/ARCHITECTURE.md")); err != nil {
		t.Fatal(err)
	}
}

func TestSyncDiscoversBranchCreatedAfterInitialEmptyClone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot := filepath.Join(root, "public")
	remote := filepath.Join(root, "private.git")
	if err := os.MkdirAll(publicRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "--bare", "-q", remote)
	runGit(t, publicRoot, "init", "-q", "-b", "main")
	runGit(t, publicRoot, "config", "user.name", "SPAS Test")
	runGit(t, publicRoot, "config", "user.email", "spas@example.invalid")
	if err := os.WriteFile(filepath.Join(publicRoot, "README.md"), []byte("public\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, publicRoot, "add", "README.md")
	runGit(t, publicRoot, "commit", "-q", "-m", "public initial")

	instance, _ := testApp(t, publicRoot, root, remote)
	if err := instance.Link(ctx, LinkOptions{Repository: "getspas/private-files", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	err := instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	})
	if err == nil || !strings.Contains(err.Error(), "private repository is empty") {
		t.Fatalf("first Sync() error = %v, want empty repository error", err)
	}

	source := filepath.Join(root, "late-source")
	runGit(t, root, "init", "-q", "-b", "main", source)
	runGit(t, source, "config", "user.name", "Late Source")
	runGit(t, source, "config", "user.email", "late@example.invalid")
	if err := os.WriteFile(filepath.Join(source, ".env"), []byte("late\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", ".env")
	runGit(t, source, "commit", "-q", "-m", "initialize later")
	runGit(t, source, "remote", "add", "origin", remote)
	runGit(t, source, "push", "-q", "origin", "main")
	runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")

	if err := instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	}); err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(publicRoot, ".env")); err != nil || string(content) != "late\n" {
		t.Fatalf("late remote file = %q, %v", content, err)
	}
}

func TestSyncRecoversPreparedCloneAfterPublicationBeforeStateFinalization(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	source := filepath.Join(root, "late-source")
	runGit(t, root, "init", "-q", "-b", "main", source)
	runGit(t, source, "config", "user.name", "Late Source")
	runGit(t, source, "config", "user.email", "late@example.invalid")
	if err := os.WriteFile(filepath.Join(source, ".env"), []byte("prepared\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", ".env")
	runGit(t, source, "commit", "-q", "-m", "prepared clone")
	runGit(t, source, "remote", "add", "origin", remote)
	runGit(t, source, "push", "-q", "origin", "main")
	runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")

	_, state, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	private := instance.privateRepository(state)
	result, err := private.PrepareClone(ctx, remote, "main")
	if err != nil {
		t.Fatalf("PrepareClone() error = %v", err)
	}
	state.Private.Initialization = &linkstate.CloneInitialization{
		Phase:           linkstate.ClonePrepared,
		RequestedBranch: "main",
		Branch:          result.Branch,
		Head:            result.Head,
		RemoteEmpty:     result.Empty,
	}
	if err := instance.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(private.StagingPath(), private.Path); err != nil {
		t.Fatalf("simulate publication = %v", err)
	}

	if err := instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	}); err != nil {
		t.Fatalf("Sync() recovery error = %v", err)
	}
	loaded := loadState(t, instance, publicRoot)
	if !loaded.Private.Initialized || loaded.Private.Initialization != nil || loaded.Private.ExpectedHead != result.Head {
		t.Fatalf("recovered private state = %+v", loaded.Private)
	}
	if content, err := os.ReadFile(filepath.Join(publicRoot, ".env")); err != nil || string(content) != "prepared\n" {
		t.Fatalf("recovered workspace file = %q, %v", content, err)
	}
}

func TestSyncRetriesPreparingCloneAfterRemovingPartialStaging(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	source := filepath.Join(root, "late-source")
	runGit(t, root, "init", "-q", "-b", "main", source)
	runGit(t, source, "config", "user.name", "Late Source")
	runGit(t, source, "config", "user.email", "late@example.invalid")
	if err := os.WriteFile(filepath.Join(source, ".env"), []byte("retry\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", ".env")
	runGit(t, source, "commit", "-q", "-m", "retry clone")
	runGit(t, source, "remote", "add", "origin", remote)
	runGit(t, source, "push", "-q", "origin", "main")
	runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")

	_, state, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	private := instance.privateRepository(state)
	state.Private.Initialization = &linkstate.CloneInitialization{Phase: linkstate.ClonePreparing, RequestedBranch: "main"}
	if err := instance.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(private.StagingPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private.StagingPath(), "partial"), []byte("not a clone"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	}); err != nil {
		t.Fatalf("Sync() preparation retry error = %v", err)
	}
	loaded := loadState(t, instance, publicRoot)
	if !loaded.Private.Initialized || loaded.Private.Initialization != nil || loaded.Private.ExpectedHead == "" {
		t.Fatalf("retried private state = %+v", loaded.Private)
	}
	if _, err := os.Stat(private.StagingPath()); !os.IsNotExist(err) {
		t.Fatalf("staging path after retry = %v", err)
	}
}

func TestSyncRejectsChangedPrivateOrigin(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, _, instance := initializedApp(t, root)
	_, state, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "other.git")
	runGit(t, root, "init", "--bare", "-q", other)
	runGit(t, state.Private.LocalRepositoryPath, "config", "--local", "remote.origin.url", other)

	err = instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	})
	if err == nil || !strings.Contains(err.Error(), "origin changed") {
		t.Fatalf("Sync() error = %v, want changed-origin rejection", err)
	}
	if got := gitOutput(t, publicRoot, "status", "--porcelain"); got != "" {
		t.Fatalf("public status changed: %q", got)
	}
}

func TestLinkReplacementRequiresUnlinkWhenPrivateStateExists(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, _, instance := initializedApp(t, root)
	err := instance.Link(ctx, LinkOptions{
		Repository: "getspas/different-private-files",
		Replace:    true,
	})
	if err == nil || !strings.Contains(err.Error(), "run spas unlink") {
		t.Fatalf("Link(replace) error = %v, want unlink requirement", err)
	}
	if got := gitOutput(t, publicRoot, "status", "--porcelain"); got != "" {
		t.Fatalf("public status changed: %q", got)
	}
}

func TestUnlinkRefusesCleanUnpushedPrivateCommit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	_, _, instance := initializedApp(t, root)
	repository, state, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	privateFile := filepath.Join(state.Private.LocalRepositoryPath, "docs", "ARCHITECTURE.md")
	if err := os.WriteFile(privateFile, []byte("unpushed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, state.Private.LocalRepositoryPath, "config", "user.name", "SPAS Test")
	runGit(t, state.Private.LocalRepositoryPath, "config", "user.email", "spas@example.invalid")
	runGit(t, state.Private.LocalRepositoryPath, "add", "docs/ARCHITECTURE.md")
	runGit(t, state.Private.LocalRepositoryPath, "commit", "-q", "-m", "unpushed")

	err = instance.Unlink(ctx, UnlinkOptions{RemovePrivateClone: true})
	if err == nil || !strings.Contains(err.Error(), "not been pushed") {
		t.Fatalf("Unlink() error = %v, want unpushed-commit rejection", err)
	}
	if _, err := os.Stat(state.Private.LocalRepositoryPath); err != nil {
		t.Fatalf("private clone was removed: %v", err)
	}
	if _, err := instance.Store.Load(repository.Root, repository.CommonDir); err != nil {
		t.Fatalf("link state was removed: %v", err)
	}
}

func TestFailedPrivateCommitRollsBackManagedClone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, _, instance := initializedApp(t, root)
	_, state, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	private := instance.privateRepository(state)
	headBefore, err := private.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, state.Private.LocalRepositoryPath, "config", "--local", "commit.gpgsign", "true")
	runGit(t, state.Private.LocalRepositoryPath, "config", "--local", "gpg.program", filepath.Join(root, "missing-gpg"))
	if err := os.WriteFile(filepath.Join(publicRoot, "docs", "ARCHITECTURE.md"), []byte("cannot commit\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = instance.Sync(ctx, SyncOptions{
		Message:         "This commit must fail",
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	})
	if err == nil {
		t.Fatal("Sync() error = nil, want signing failure")
	}
	clean, cleanErr := private.IsClean(ctx)
	if cleanErr != nil {
		t.Fatal(cleanErr)
	}
	if !clean {
		t.Fatal("private clone remained dirty after failed commit")
	}
	headAfter, headErr := private.Head(ctx)
	if headErr != nil {
		t.Fatal(headErr)
	}
	if headAfter != headBefore {
		t.Fatalf("private HEAD changed from %s to %s", headBefore, headAfter)
	}
}

func TestRemoteCaseOnlyRenameSynchronizes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, remote, instance := initializedApp(t, root)
	// Force the conservative case-insensitive policy even though this test
	// normally runs on a case-sensitive Linux filesystem. This exercises the
	// required remove-before-copy ordering used on Windows and common macOS
	// filesystems.
	runGit(t, publicRoot, "config", "--local", "core.ignoreCase", "true")

	other := filepath.Join(root, "other-case-rename")
	runGit(t, root, "clone", "-q", remote, other)
	runGit(t, other, "config", "user.name", "Other Test")
	runGit(t, other, "config", "user.email", "other@example.invalid")
	runGit(t, other, "mv", "docs/ARCHITECTURE.md", "docs/architecture.md")
	if err := os.WriteFile(filepath.Join(other, "docs", "architecture.md"), []byte("renamed remotely\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, "add", "docs/architecture.md")
	runGit(t, other, "commit", "-q", "-m", "rename architecture document by case")
	runGit(t, other, "push", "-q", "origin", "main")

	if err := instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	}); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(publicRoot, "docs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "architecture.md" {
		t.Fatalf("workspace directory entries = %v, want exact renamed spelling architecture.md", entries)
	}
	content, err := os.ReadFile(filepath.Join(publicRoot, "docs", "architecture.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "renamed remotely\n" {
		t.Fatalf("renamed workspace content = %q", content)
	}
	state := loadState(t, instance, publicRoot)
	if !slices.Equal(state.ManagedPaths, []string{"docs/architecture.md"}) {
		t.Fatalf("managed paths = %v, want renamed spelling", state.ManagedPaths)
	}
}

func TestPrivateMergeConflictCanContinue(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, remote, instance := initializedApp(t, root)

	localFile := filepath.Join(publicRoot, "docs", "ARCHITECTURE.md")
	if err := os.WriteFile(localFile, []byte("local change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "other-conflict")
	runGit(t, root, "clone", "-q", remote, other)
	runGit(t, other, "config", "user.name", "Other Test")
	runGit(t, other, "config", "user.email", "other@example.invalid")
	if err := os.WriteFile(filepath.Join(other, "docs", "ARCHITECTURE.md"), []byte("remote change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, "add", "docs/ARCHITECTURE.md")
	runGit(t, other, "commit", "-q", "-m", "remote conflicting change")
	runGit(t, other, "push", "-q", "origin", "main")

	err := instance.Sync(ctx, SyncOptions{
		Message:         "Local conflicting change",
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	})
	if !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want ErrPrivateMergeConflict", err)
	}
	conflicted, err := os.ReadFile(localFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(conflicted, []byte("<<<<<<<")) {
		t.Fatalf("workspace conflict file has no markers: %q", conflicted)
	}

	if err := os.WriteFile(localFile, []byte("resolved change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	publicIgnore := filepath.Join(publicRoot, ".gitignore")
	if err := os.WriteFile(publicIgnore, []byte("!docs/ARCHITECTURE.md\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = instance.Sync(ctx, SyncOptions{
		Continue: true,
		Message:  "Resolve architecture conflict",
	})
	if err == nil || !strings.Contains(err.Error(), "not effectively excluded") {
		t.Fatalf("continue with ineffective exclusion error = %v", err)
	}
	state := loadState(t, instance, publicRoot)
	if state.ActiveMerge == nil {
		t.Fatal("failed continue cleared active merge state")
	}
	if err := os.Remove(publicIgnore); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(ctx, SyncOptions{
		Continue: true,
		Message:  "Resolve architecture conflict",
	}); err != nil {
		t.Fatalf("continue Sync() error = %v", err)
	}
	if got := gitOutput(t, root, "--git-dir="+remote, "show", "main:docs/ARCHITECTURE.md"); got != "resolved change\n" {
		t.Fatalf("resolved private file = %q", got)
	}
}

func TestPrivateMergeAbortRecoversAfterGitAbortAndRemovesStaleConflictFile(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, remote, instance := initializedApp(t, root)
	managedPath := filepath.Join(publicRoot, "docs", "ARCHITECTURE.md")

	if err := instance.Remove(ctx, RemoveOptions{Paths: []string{"docs/ARCHITECTURE.md"}}); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "other-abort-conflict")
	runGit(t, root, "clone", "-q", remote, other)
	runGit(t, other, "config", "user.name", "Other Test")
	runGit(t, other, "config", "user.email", "other@example.invalid")
	if err := os.WriteFile(filepath.Join(other, "docs", "ARCHITECTURE.md"), []byte("remote replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, "add", "docs/ARCHITECTURE.md")
	runGit(t, other, "commit", "-q", "-m", "modify file deleted locally")
	runGit(t, other, "push", "-q", "origin", "main")

	err := instance.Sync(ctx, SyncOptions{
		Message:         "Remove private architecture document",
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want ErrPrivateMergeConflict", err)
	}
	state := loadState(t, instance, publicRoot)
	if state.ActiveMerge == nil || state.ActiveMerge.PreMergeHead == "" {
		t.Fatalf("active merge recovery metadata = %#v", state.ActiveMerge)
	}
	conflicted, err := os.ReadFile(managedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(conflicted) != "remote replacement\n" {
		t.Fatalf("workspace modify/delete conflict file = %q", conflicted)
	}

	// Simulate a crash after Git completed merge --abort but before SPAS
	// restored the public workspace and cleared ActiveMerge.
	private := instance.privateRepository(state)
	if err := private.AbortMerge(ctx); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(ctx, SyncOptions{Abort: true}); err != nil {
		t.Fatalf("recover aborted merge: %v", err)
	}
	if _, err := os.Stat(managedPath); !os.IsNotExist(err) {
		t.Fatalf("stale merge-conflict workspace file remains: %v", err)
	}
	reloaded := loadState(t, instance, publicRoot)
	if reloaded.ActiveMerge != nil {
		t.Fatalf("active merge state remains after abort: %#v", reloaded.ActiveMerge)
	}
	if slices.Contains(reloaded.ManagedPaths, "docs/ARCHITECTURE.md") {
		t.Fatalf("deleted private path remains managed: %v", reloaded.ManagedPaths)
	}
	if slices.Contains(reloaded.PendingRemovalPaths(), "docs/ARCHITECTURE.md") {
		t.Fatalf("completed private deletion remains pending after abort: %v", reloaded.PendingRemovalPaths())
	}

	recoveryRoot := filepath.Join(instance.Store.DataDir, "recovery", state.LinkID)
	entries, err := os.ReadDir(recoveryRoot)
	if err != nil || len(entries) == 0 {
		t.Fatalf("merge resolution recovery copy was not retained: entries=%v err=%v", entries, err)
	}
	found := false
	for _, entry := range entries {
		content, readErr := os.ReadFile(filepath.Join(recoveryRoot, entry.Name(), "docs", "ARCHITECTURE.md"))
		if readErr == nil && string(content) == "remote replacement\n" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("recovery store does not contain the discarded conflict resolution")
	}
}

func TestPrivateMergeContinueRetainsDeferredEnrollmentAndRemoval(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, remote, instance := initializedApp(t, root)

	removalPath := filepath.Join(publicRoot, "removal.txt")
	if err := os.WriteFile(removalPath, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Add(ctx, AddOptions{
		Paths:           []string{"removal.txt"},
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	}); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(ctx, SyncOptions{
		Message:         "Add deferred-state fixtures",
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	}); err != nil {
		t.Fatal(err)
	}
	pendingPath := filepath.Join(publicRoot, "pending.txt")
	if err := os.WriteFile(pendingPath, []byte("pending\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Add(ctx, AddOptions{
		Paths:           []string{"pending.txt"},
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(pendingPath); err != nil {
		t.Fatal(err)
	}
	if err := instance.Remove(ctx, RemoveOptions{Paths: []string{"removal.txt"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(removalPath, []byte("edited after removal request\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	localFile := filepath.Join(publicRoot, "docs", "ARCHITECTURE.md")
	if err := os.WriteFile(localFile, []byte("local change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "other-deferred-conflict")
	runGit(t, root, "clone", "-q", remote, other)
	runGit(t, other, "config", "user.name", "Other Test")
	runGit(t, other, "config", "user.email", "other@example.invalid")
	if err := os.WriteFile(filepath.Join(other, "docs", "ARCHITECTURE.md"), []byte("remote change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, "add", "docs/ARCHITECTURE.md")
	runGit(t, other, "commit", "-q", "-m", "remote conflicting change")
	runGit(t, other, "push", "-q", "origin", "main")

	err := instance.Sync(ctx, SyncOptions{
		Message:         "Local conflicting change",
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want ErrPrivateMergeConflict", err)
	}
	if err := os.WriteFile(localFile, []byte("resolved change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(ctx, SyncOptions{
		Continue: true,
		Message:  "Resolve architecture conflict",
	}); err != nil {
		t.Fatalf("continue Sync() error = %v", err)
	}

	state := loadState(t, instance, publicRoot)
	if !slices.Contains(state.PendingAdds, "pending.txt") {
		t.Fatalf("PendingAdds = %v, want pending.txt retained", state.PendingAdds)
	}
	if !slices.Contains(state.PendingRemovalPaths(), "removal.txt") {
		t.Fatalf("PendingRemoves = %v, want removal.txt retained", state.PendingRemovalPaths())
	}
	if !slices.Contains(state.ManagedPaths, "removal.txt") {
		t.Fatalf("ManagedPaths = %v, want deferred removal retained", state.ManagedPaths)
	}
	repository, _, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	removalRepoPath, err := pathmodel.Parse("removal.txt")
	if err != nil {
		t.Fatal(err)
	}
	if excluded, err := repository.IsEffectivelyExcluded(ctx, removalRepoPath); err != nil || !excluded {
		t.Fatalf("deferred removal exclusion = %t, %v", excluded, err)
	}
	content, err := os.ReadFile(removalPath)
	if err != nil || string(content) != "edited after removal request\n" {
		t.Fatalf("deferred removal workspace content = %q, %v", content, err)
	}
}

func TestCompletedMergeContinuationRecoversBeforePush(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, remote, instance := initializedApp(t, root)

	localFile := filepath.Join(publicRoot, "docs", "ARCHITECTURE.md")
	if err := os.WriteFile(localFile, []byte("local change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "other-committed-continuation")
	runGit(t, root, "clone", "-q", remote, other)
	runGit(t, other, "config", "user.name", "Other Test")
	runGit(t, other, "config", "user.email", "other@example.invalid")
	if err := os.WriteFile(filepath.Join(other, "docs", "ARCHITECTURE.md"), []byte("remote change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, "add", "docs/ARCHITECTURE.md")
	runGit(t, other, "commit", "-q", "-m", "remote conflicting change")
	runGit(t, other, "push", "-q", "origin", "main")

	err := instance.Sync(ctx, SyncOptions{
		Message:         "Local conflicting change",
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want ErrPrivateMergeConflict", err)
	}

	state := loadState(t, instance, publicRoot)
	private := instance.privateRepository(state)
	preMergeHead, err := private.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resolvedPath, err := pathmodel.Parse("docs/ARCHITECTURE.md")
	if err != nil {
		t.Fatal(err)
	}
	finalPaths := stringsToPaths(state.ManagedPaths)
	snapshots, err := snapshotPaths(publicRoot, finalPaths)
	if err != nil {
		t.Fatal(err)
	}
	materialization, err := newMaterialization(
		preMergeHead,
		state.ManagedPaths,
		finalPaths,
		finalPaths,
		nil,
		nil,
		nil,
		nil,
		snapshots,
		state.ActiveMerge.RemainingPendingAdds,
		state.ActiveMerge.RemainingPendingRemoves,
	)
	if err != nil {
		t.Fatal(err)
	}
	state.Materializing = materialization
	state.Materializing.Phase = linkstate.MaterializationMergeContinuing
	saveState(t, instance, state)
	if err := instance.Sync(ctx, syncOptions("")); err == nil || !strings.Contains(err.Error(), "spas sync --continue") {
		t.Fatalf("interrupted pre-commit continuation error = %v, want sync --continue guidance", err)
	}

	if err := os.WriteFile(localFile, []byte("resolved after interrupted state save\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := filesync.CopyManaged(publicRoot, resolvedPath, private.Path, resolvedPath); err != nil {
		t.Fatal(err)
	}
	if err := private.StageMergeResolution(ctx, []pathmodel.Path{resolvedPath}, nil); err != nil {
		t.Fatal(err)
	}
	mergeHead, err := private.MergeHead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stagedTree, err := private.IndexTree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state.Materializing.MergeHead = mergeHead
	state.Materializing.StagedTree = stagedTree
	state.Materializing.Phase = linkstate.MaterializationMergeStaged
	saveState(t, instance, state)
	if err := private.Commit(ctx, "Resolve before state save"); err != nil {
		t.Fatal(err)
	}

	if err := instance.Sync(ctx, syncOptions("")); err != nil {
		t.Fatalf("recover completed merge continuation: %v", err)
	}
	if got := gitOutput(t, root, "--git-dir="+remote, "show", "main:docs/ARCHITECTURE.md"); got != "resolved after interrupted state save\n" {
		t.Fatalf("resolved private file = %q", got)
	}
	reloaded := loadState(t, instance, publicRoot)
	if reloaded.ActiveMerge != nil || reloaded.Materializing != nil {
		t.Fatalf("merge recovery state was not cleared: active=%+v materializing=%+v", reloaded.ActiveMerge, reloaded.Materializing)
	}
}

func TestPrivateMergeConflictCanResolveByDeletion(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, remote, instance := initializedApp(t, root)

	localFile := filepath.Join(publicRoot, "docs", "ARCHITECTURE.md")
	if err := os.WriteFile(localFile, []byte("local change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "other-delete-conflict")
	runGit(t, root, "clone", "-q", remote, other)
	runGit(t, other, "config", "user.name", "Other Test")
	runGit(t, other, "config", "user.email", "other@example.invalid")
	runGit(t, other, "rm", "-q", "docs/ARCHITECTURE.md")
	runGit(t, other, "commit", "-q", "-m", "remote deletion")
	runGit(t, other, "push", "-q", "origin", "main")

	err := instance.Sync(ctx, SyncOptions{
		Message:         "Local conflicting change",
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	})
	if !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want ErrPrivateMergeConflict", err)
	}
	if err := os.Remove(localFile); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(ctx, SyncOptions{
		Continue: true,
		Message:  "Resolve architecture conflict by deletion",
	}); err != nil {
		t.Fatalf("continue Sync() error = %v", err)
	}
	result, showErr := (gitexec.Runner{}).Run(ctx, root, "--git-dir="+remote, "show", "main:docs/ARCHITECTURE.md")
	if showErr == nil {
		t.Fatalf("private file still exists after deletion resolution: %q", result.Stdout)
	}
}

func TestCopyConflictFilesRejectsConcurrentWorkspaceEdit(t *testing.T) {
	t.Parallel()

	publicRoot := t.TempDir()
	privateRoot := t.TempDir()
	path := mustPath(t, "docs/ARCHITECTURE.md")
	for _, root := range []string{publicRoot, privateRoot} {
		if err := os.MkdirAll(filepath.Join(root, "docs"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path.OSPath(publicRoot), []byte("planned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path.OSPath(privateRoot), []byte("<<<<<<< ours\n=======\n>>>>>>> theirs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshots, err := snapshotPaths(publicRoot, []pathmodel.Path{path})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path.OSPath(publicRoot), []byte("concurrent edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = copyConflictFiles(privateRoot, publicRoot, []pathmodel.Path{path}, snapshots)
	if err == nil || !strings.Contains(err.Error(), "changed after synchronization planning") {
		t.Fatalf("copyConflictFiles() error = %v, want concurrent-edit rejection", err)
	}
}

func TestMaterializationCandidatesIncludePreviouslyManagedDeletion(t *testing.T) {
	t.Parallel()

	path := mustPath(t, "docs/ARCHITECTURE.md")
	got := materializationCandidates(nil, []string{path.String()}, map[string]struct{}{})
	if len(got) != 1 || got[0] != path {
		t.Fatalf("materializationCandidates() = %v, want previously managed path", got)
	}
}

func initializedApp(t *testing.T, root string) (string, string, App) {
	t.Helper()
	publicRoot := filepath.Join(root, "public")
	source := filepath.Join(root, "source")
	remote := filepath.Join(root, "private.git")
	if err := os.MkdirAll(publicRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "--bare", "-q", remote)
	runGit(t, source, "init", "-q", "-b", "main")
	runGit(t, source, "config", "user.name", "Source Test")
	runGit(t, source, "config", "user.email", "source@example.invalid")
	if err := os.MkdirAll(filepath.Join(source, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "docs", "ARCHITECTURE.md"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "docs/ARCHITECTURE.md")
	runGit(t, source, "commit", "-q", "-m", "initial")
	runGit(t, source, "remote", "add", "origin", remote)
	runGit(t, source, "push", "-q", "origin", "main")
	runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")

	runGit(t, publicRoot, "init", "-q", "-b", "main")
	runGit(t, publicRoot, "config", "user.name", "SPAS Test")
	runGit(t, publicRoot, "config", "user.email", "spas@example.invalid")
	if err := os.WriteFile(filepath.Join(publicRoot, "README.md"), []byte("public\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, publicRoot, "add", "README.md")
	runGit(t, publicRoot, "commit", "-q", "-m", "public initial")

	instance, _ := testApp(t, publicRoot, root, remote)
	if err := instance.Link(context.Background(), LinkOptions{Repository: "getspas/private-files"}); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(context.Background(), SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	}); err != nil {
		t.Fatal(err)
	}
	return publicRoot, remote, instance
}

func testApp(t *testing.T, publicRoot, root, remote string) (App, *bytes.Buffer) {
	t.Helper()
	var output bytes.Buffer
	return App{
		Git:      gitexec.Runner{NonInteractive: true},
		Store:    linkstate.Store{ConfigDir: filepath.Join(root, "config"), DataDir: filepath.Join(root, "data")},
		Prompt:   interaction.Prompter{In: strings.NewReader(""), Out: &output, Interactive: false},
		Out:      &output,
		Err:      &output,
		RepoHint: publicRoot,
		PathBase: publicRoot,
		Provider: testRepositoryProvider{remoteURL: remote},
	}, &output
}

type testRepositoryProvider struct {
	remoteURL string
}

func (testRepositoryProvider) ID() provider.ID { return githubref.ID }

func (p testRepositoryProvider) Resolve(request provider.RepositoryRequest) (provider.RepositoryRef, error) {
	ref, err := (githubref.Provider{}).Resolve(request)
	if err != nil {
		return provider.RepositoryRef{}, err
	}
	ref.RemoteURL = p.remoteURL
	return ref, nil
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := (gitexec.Runner{}).Run(context.Background(), dir, args...); err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	result, err := (gitexec.Runner{}).Run(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(result.Stdout)
}

func mustPath(t *testing.T, value string) pathmodel.Path {
	t.Helper()
	path, err := pathmodel.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return path
}
