package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/getspas/spas/internal/interaction"
	"github.com/getspas/spas/internal/linkstate"
	"github.com/getspas/spas/internal/spaserr"
)

type mutateBeforeFinalByteReader struct {
	data      []byte
	offset    int
	mutate    func() error
	mutated   bool
	mutateErr error
}

func (r *mutateBeforeFinalByteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	if !r.mutated && r.offset == len(r.data)-1 {
		r.mutated = true
		r.mutateErr = r.mutate()
		if r.mutateErr != nil {
			return 0, r.mutateErr
		}
	}
	p[0] = r.data[r.offset]
	r.offset++
	return 1, nil
}

func TestLinkIsStrictlyLocalAndOffline(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot := initializePublicRepository(t, root)
	remote := filepath.Join(root, "must-not-exist.git")
	instance, _ := testApp(t, publicRoot, root, remote)
	excludeBefore := gitOutput(t, publicRoot, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	excludeBefore = strings.TrimSpace(excludeBefore)
	before, err := os.ReadFile(excludeBefore)
	if err != nil {
		t.Fatal(err)
	}

	if err := instance.Link(ctx, LinkOptions{
		Repository: "getspas/private-files",
		Branch:     "main",
	}); err != nil {
		t.Fatalf("Link() error = %v", err)
	}
	repository, state, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Private.Initialized {
		t.Fatal("link initialized the private repository")
	}
	if _, err := os.Stat(state.Private.LocalRepositoryPath); !os.IsNotExist(err) {
		t.Fatalf("link created the private clone: %v", err)
	}
	after, err := os.ReadFile(excludeBefore)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("link modified the public repository local exclude file")
	}
	if got := gitOutput(t, repository.Root, "config", "--local", "--list"); strings.Contains(got, "mergeoptions") {
		t.Fatalf("link modified branch merge options:\n%s", got)
	}
}

func TestDryRunsDoNotInitializeOrMutateState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot := initializePublicRepository(t, root)
	remote := filepath.Join(root, "private.git")
	runGit(t, root, "init", "--bare", "-q", remote)
	if err := os.WriteFile(filepath.Join(publicRoot, ".env"), []byte("PRIVATE=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	instance, _ := testApp(t, publicRoot, root, remote)
	if err := instance.Link(ctx, LinkOptions{Repository: "getspas/private-files", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	repository, before, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	excludePath, err := repository.InfoExcludePath(ctx)
	if err != nil {
		t.Fatal(err)
	}
	excludeBefore, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}

	if err := instance.Add(ctx, AddOptions{
		Paths:           []string{".env"},
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
		DryRun:          true,
	}); err != nil {
		t.Fatalf("Add(dry-run) error = %v", err)
	}
	if err := instance.Sync(ctx, SyncOptions{DryRun: true}); err != nil {
		t.Fatalf("Sync(dry-run) error = %v", err)
	}
	after, err := instance.Store.Load(repository.Root, repository.CommonDir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("dry-run changed link state:\nbefore: %#v\nafter:  %#v", before, after)
	}
	excludeAfter, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(excludeAfter, excludeBefore) {
		t.Fatal("dry-run changed the public repository local exclude file")
	}
	if _, err := os.Stat(before.Private.LocalRepositoryPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run initialized a private clone: %v", err)
	}
}

func TestInitializedSyncAndRemoveDryRunsAreMutationFree(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, remote, instance := initializedApp(t, root)
	repository, before, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	excludePath, err := repository.InfoExcludePath(ctx)
	if err != nil {
		t.Fatal(err)
	}
	excludeBefore, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	privateHeadBefore := strings.TrimSpace(gitOutput(t, before.Private.LocalRepositoryPath, "rev-parse", "HEAD"))
	remoteHeadBefore := strings.TrimSpace(gitOutput(t, root, "--git-dir="+remote, "rev-parse", "main"))
	if err := os.WriteFile(
		filepath.Join(publicRoot, "docs", "ARCHITECTURE.md"),
		[]byte("dry-run edit\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	workspaceBefore, err := os.ReadFile(filepath.Join(publicRoot, "docs", "ARCHITECTURE.md"))
	if err != nil {
		t.Fatal(err)
	}

	if err := instance.Sync(ctx, SyncOptions{
		Message:         "Would update architecture",
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
		DryRun:          true,
	}); err != nil {
		t.Fatalf("Sync(dry-run) error = %v", err)
	}
	if err := instance.Remove(ctx, RemoveOptions{
		Paths:  []string{"docs/ARCHITECTURE.md"},
		DryRun: true,
	}); err != nil {
		t.Fatalf("Remove(dry-run) error = %v", err)
	}
	after, err := instance.Store.Load(repository.Root, repository.CommonDir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("initialized dry-run changed link state")
	}
	excludeAfter, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(excludeAfter, excludeBefore) {
		t.Fatal("initialized dry-run changed local exclusions")
	}
	workspaceAfter, err := os.ReadFile(filepath.Join(publicRoot, "docs", "ARCHITECTURE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(workspaceAfter, workspaceBefore) {
		t.Fatal("initialized dry-run changed the workspace file")
	}
	if got := strings.TrimSpace(gitOutput(t, before.Private.LocalRepositoryPath, "rev-parse", "HEAD")); got != privateHeadBefore {
		t.Fatalf("private HEAD changed from %s to %s", privateHeadBefore, got)
	}
	if got := strings.TrimSpace(gitOutput(t, root, "--git-dir="+remote, "rev-parse", "main")); got != remoteHeadBefore {
		t.Fatalf("remote HEAD changed from %s to %s", remoteHeadBefore, got)
	}
}

func TestAddRejectsSymlinksNestedGitMetadataEmptyDirectoriesAndOutsidePaths(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot := initializePublicRepository(t, root)
	remote := filepath.Join(root, "private.git")
	runGit(t, root, "init", "--bare", "-q", remote)
	instance, _ := testApp(t, publicRoot, root, remote)
	if err := instance.Link(ctx, LinkOptions{Repository: "getspas/private-files", Branch: "main"}); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(publicRoot, "source.env"), []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("source.env", filepath.Join(publicRoot, "link.env")); err == nil {
		err := instance.Add(ctx, AddOptions{
			Paths:           []string{"link.env"},
			ExistingExclude: ExcludePreserve,
			MergeProtection: MergeSkip,
		})
		if err == nil || !strings.Contains(err.Error(), "regular file or directory") {
			t.Fatalf("Add(symlink) error = %v", err)
		}
	}

	nested := filepath.Join(publicRoot, "nested")
	if err := os.MkdirAll(filepath.Join(nested, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "private.txt"), []byte("private\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := instance.Add(ctx, AddOptions{
		Paths:           []string{"nested"},
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if err == nil || !strings.Contains(err.Error(), "nested Git metadata") {
		t.Fatalf("Add(nested Git) error = %v", err)
	}

	empty := filepath.Join(publicRoot, "empty")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	err = instance.Add(ctx, AddOptions{
		Paths:           []string{"empty"},
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if err == nil || !strings.Contains(err.Error(), "no regular files") {
		t.Fatalf("Add(empty directory) error = %v", err)
	}

	outside := filepath.Join(root, "outside.env")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = instance.Add(ctx, AddOptions{
		Paths:           []string{outside},
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if err == nil || !strings.Contains(err.Error(), "stay inside") {
		t.Fatalf("Add(outside path) error = %v", err)
	}
	if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsupportedPath {
		t.Fatalf("Add(outside path) kind = %v, want KindUnsupportedPath", kind)
	}
}

func TestRemoveCancelsPendingAdditionBeforeFirstSync(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot := initializePublicRepository(t, root)
	remote := filepath.Join(root, "private.git")
	runGit(t, root, "init", "--bare", "-q", remote)
	if err := os.WriteFile(filepath.Join(publicRoot, ".env"), []byte("PRIVATE=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	instance, _ := testApp(t, publicRoot, root, remote)
	if err := instance.Link(ctx, LinkOptions{Repository: "getspas/private-files", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := instance.Add(ctx, AddOptions{
		Paths:           []string{".env"},
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	}); err != nil {
		t.Fatal(err)
	}
	if err := instance.Remove(ctx, RemoveOptions{Paths: []string{".env"}}); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	repository, state, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.PendingAdds) != 0 || len(state.PendingRemoves) != 0 {
		t.Fatalf("pending state = additions %v, removals %v", state.PendingAdds, state.PendingRemoves)
	}
	excludePath, err := repository.InfoExcludePath(ctx)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "/.env") || strings.Contains(string(content), "BEGIN SPAS") {
		t.Fatalf("Remove() retained local ownership exclusion:\n%s", content)
	}
	if _, err := os.Stat(filepath.Join(publicRoot, ".env")); err != nil {
		t.Fatalf("Remove() deleted pending workspace file: %v", err)
	}
}

func TestUntrackedWorkspaceObstructionCanAbortSkipOrOverride(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot := initializePublicRepository(t, root)
	remote := initializePrivateRemoteWithFile(t, root, ".env", "remote private\n")
	localPath := filepath.Join(publicRoot, ".env")
	if err := os.WriteFile(localPath, []byte("unmanaged local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	instance, _ := testApp(t, publicRoot, root, remote)
	if err := instance.Link(ctx, LinkOptions{Repository: "getspas/private-files"}); err != nil {
		t.Fatal(err)
	}

	err := instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if err == nil || !strings.Contains(err.Error(), "untracked working tree file") {
		t.Fatalf("Sync(abort) error = %v", err)
	}
	if content, readErr := os.ReadFile(localPath); readErr != nil || string(content) != "unmanaged local\n" {
		t.Fatalf("abort changed obstruction to %q, %v", content, readErr)
	}

	if err := instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictSkip,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	}); err != nil {
		t.Fatalf("Sync(skip) error = %v", err)
	}
	if content, readErr := os.ReadFile(localPath); readErr != nil || string(content) != "unmanaged local\n" {
		t.Fatalf("skip changed obstruction to %q, %v", content, readErr)
	}

	if err := instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictOverride,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	}); err != nil {
		t.Fatalf("Sync(override) error = %v", err)
	}
	if content, readErr := os.ReadFile(localPath); readErr != nil || string(content) != "remote private\n" {
		t.Fatalf("override content = %q, %v", content, readErr)
	}
	if got := gitOutput(t, publicRoot, "status", "--porcelain", "--untracked-files=all"); got != "" {
		t.Fatalf("overridden path is visible to public Git: %q", got)
	}
}

func TestStatusDiffAndDoctorAreReadOnlyAndOffline(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, _, instance := initializedApp(t, root)
	repository, before, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	excludePath, err := repository.InfoExcludePath(ctx)
	if err != nil {
		t.Fatal(err)
	}
	excludeBefore, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	publicHeadBefore := strings.TrimSpace(gitOutput(t, publicRoot, "rev-parse", "HEAD"))
	privateHeadBefore := strings.TrimSpace(gitOutput(t, before.Private.LocalRepositoryPath, "rev-parse", "HEAD"))
	if err := os.WriteFile(
		filepath.Join(publicRoot, "docs", "ARCHITECTURE.md"),
		[]byte("local design edit\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	instance.Out = &output
	instance.Err = &output
	instance.JSON = true
	if err := instance.Status(ctx, StatusOptions{ShowPaths: true}); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	var status Status
	if err := json.Unmarshal(output.Bytes(), &status); err != nil {
		t.Fatalf("decode status: %v\n%s", err, output.String())
	}
	if !status.Linked || !status.PrivateInitialized || status.ManagedFiles != 1 {
		t.Fatalf("Status() = %#v", status)
	}
	if !reflect.DeepEqual(status.WorkspaceModified, []string{"docs/ARCHITECTURE.md"}) ||
		len(status.WorkspaceMissing) != 0 ||
		status.PrivateAhead == nil || *status.PrivateAhead != 0 ||
		status.PrivateBehind == nil || *status.PrivateBehind != 0 ||
		len(status.PathConflicts) != 0 ||
		len(status.ExclusionFailures) != 0 ||
		status.PendingRecovery {
		t.Fatalf("Status(health) = %#v", status)
	}
	if !sameDirectory(t, status.PublicWorkspace, publicRoot) ||
		!sameDirectory(t, status.PrivateClone, before.Private.LocalRepositoryPath) {
		t.Fatalf("Status(show paths) = %#v", status)
	}

	output.Reset()
	if err := instance.Diff(ctx, DiffOptions{NameOnly: true}); err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	var diff struct {
		ChangedPaths []string `json:"changedPaths"`
	}
	if err := json.Unmarshal(output.Bytes(), &diff); err != nil {
		t.Fatalf("decode diff: %v\n%s", err, output.String())
	}
	if !reflect.DeepEqual(diff.ChangedPaths, []string{"docs/ARCHITECTURE.md"}) {
		t.Fatalf("Diff() changed paths = %v", diff.ChangedPaths)
	}

	output.Reset()
	if err := instance.Doctor(ctx); err != nil {
		t.Fatalf("Doctor() error = %v\n%s", err, output.String())
	}
	var doctor DoctorResult
	if err := json.Unmarshal(output.Bytes(), &doctor); err != nil {
		t.Fatalf("decode doctor: %v\n%s", err, output.String())
	}
	if !doctor.Healthy || doctor.Errors != 0 {
		t.Fatalf("Doctor() = %#v", doctor)
	}

	instance.JSON = false
	output.Reset()
	if err := instance.Status(ctx, StatusOptions{Short: true}); err != nil {
		t.Fatalf("Status(short) error = %v", err)
	}
	if !strings.Contains(output.String(), "linked=true") || !strings.Contains(output.String(), "managed=1") {
		t.Fatalf("Status(short) output = %q", output.String())
	}
	output.Reset()
	if err := instance.Diff(ctx, DiffOptions{NameOnly: true}); err != nil {
		t.Fatalf("Diff(name-only) error = %v", err)
	}
	if strings.TrimSpace(output.String()) != "docs/ARCHITECTURE.md" {
		t.Fatalf("Diff(name-only) output = %q", output.String())
	}
	output.Reset()
	if err := instance.Diff(ctx, DiffOptions{Stat: true}); err != nil {
		t.Fatalf("Diff(stat) error = %v", err)
	}
	if !strings.Contains(output.String(), "ARCHITECTURE.md") {
		t.Fatalf("Diff(stat) output = %q", output.String())
	}

	after, err := instance.Store.Load(repository.Root, repository.CommonDir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("read-only commands changed link state")
	}
	excludeAfter, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(excludeAfter, excludeBefore) {
		t.Fatal("read-only commands changed local exclusions")
	}
	if got := strings.TrimSpace(gitOutput(t, publicRoot, "rev-parse", "HEAD")); got != publicHeadBefore {
		t.Fatalf("public HEAD changed from %s to %s", publicHeadBefore, got)
	}
	if got := strings.TrimSpace(gitOutput(t, before.Private.LocalRepositoryPath, "rev-parse", "HEAD")); got != privateHeadBefore {
		t.Fatalf("private HEAD changed from %s to %s", privateHeadBefore, got)
	}
}

func TestStatusTextShowsLocalPathsOnlyWhenRequested(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, _, instance := initializedApp(t, root)
	state := loadState(t, instance, publicRoot)

	var output bytes.Buffer
	instance.Out = &output
	if err := instance.Status(ctx, StatusOptions{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), state.Public.Root) || strings.Contains(output.String(), state.Private.LocalRepositoryPath) {
		t.Fatalf("status exposed local paths without --show-paths:\n%s", output.String())
	}

	output.Reset()
	if err := instance.Status(ctx, StatusOptions{ShowPaths: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), state.Public.Root) || !strings.Contains(output.String(), state.Private.LocalRepositoryPath) {
		t.Fatalf("status --show-paths omitted local paths:\n%s", output.String())
	}
}

func TestDiffIncludesPendingRemoval(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	_, _, instance := initializedApp(t, root)
	if err := instance.Remove(ctx, RemoveOptions{Paths: []string{"docs/ARCHITECTURE.md"}}); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	var output bytes.Buffer
	instance.Out = &output
	if err := instance.Diff(ctx, DiffOptions{NameOnly: true}); err != nil {
		t.Fatalf("Diff(name-only) error = %v", err)
	}
	if strings.TrimSpace(output.String()) != "docs/ARCHITECTURE.md" {
		t.Fatalf("Diff(name-only) = %q, want pending removal", output.String())
	}

	output.Reset()
	if err := instance.Diff(ctx, DiffOptions{Stat: true}); err != nil {
		t.Fatalf("Diff(stat) error = %v", err)
	}
	if !strings.Contains(output.String(), "ARCHITECTURE.md") {
		t.Fatalf("Diff(stat) = %q, want pending removal", output.String())
	}
}

func TestPrivateMergeConflictCanAbort(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, remote, instance := initializedApp(t, root)
	localFile := filepath.Join(publicRoot, "docs", "ARCHITECTURE.md")
	if err := os.WriteFile(localFile, []byte("local change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "other-abort")
	runGit(t, root, "clone", "-q", remote, other)
	runGit(t, other, "config", "user.name", "Other Test")
	runGit(t, other, "config", "user.email", "other@example.invalid")
	if err := os.WriteFile(filepath.Join(other, "docs", "ARCHITECTURE.md"), []byte("remote change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, "add", "docs/ARCHITECTURE.md")
	runGit(t, other, "commit", "-q", "-m", "remote conflict")
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
	if err := instance.Sync(ctx, SyncOptions{Abort: true}); err != nil {
		t.Fatalf("Sync(abort) error = %v", err)
	}
	content, err := os.ReadFile(localFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "local change\n" {
		t.Fatalf("workspace content after abort = %q", content)
	}
	_, state, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveMerge != nil {
		t.Fatalf("active merge remains after abort: %#v", state.ActiveMerge)
	}
	if _, err := os.Stat(filepath.Join(state.Private.LocalRepositoryPath, ".git", "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Fatalf("private merge still active: %v", err)
	}
	if got := gitOutput(t, root, "--git-dir="+remote, "show", "main:docs/ARCHITECTURE.md"); got != "remote change\n" {
		t.Fatalf("abort changed private remote content to %q", got)
	}
}

func TestPushFailurePreservesCommitAndRetryDoesNotDuplicateIt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, remote, instance := initializedApp(t, root)
	_, state, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	baseCount := strings.TrimSpace(gitOutput(t, root, "--git-dir="+remote, "rev-list", "--count", "main"))
	hook := filepath.Join(remote, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(publicRoot, "docs", "ARCHITECTURE.md"),
		[]byte("preserve this edit\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	err = instance.Sync(ctx, SyncOptions{
		Message:         "Preserve this private edit",
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	})
	if err == nil || !strings.Contains(err.Error(), "push private branch") {
		t.Fatalf("Sync() error = %v, want rejected push", err)
	}
	ahead := strings.TrimSpace(gitOutput(
		t,
		state.Private.LocalRepositoryPath,
		"rev-list",
		"--count",
		"refs/remotes/origin/main..HEAD",
	))
	if ahead != "1" {
		t.Fatalf("local private branch ahead by %s commits, want 1", ahead)
	}
	if got := gitOutput(t, root, "--git-dir="+remote, "show", "main:docs/ARCHITECTURE.md"); got != "initial\n" {
		t.Fatalf("rejected push changed remote to %q", got)
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}

	if err := instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	}); err != nil {
		t.Fatalf("retry Sync() error = %v", err)
	}
	if got := gitOutput(t, root, "--git-dir="+remote, "show", "main:docs/ARCHITECTURE.md"); got != "preserve this edit\n" {
		t.Fatalf("retry remote content = %q", got)
	}
	finalCount := strings.TrimSpace(gitOutput(t, root, "--git-dir="+remote, "rev-list", "--count", "main"))
	if finalCount != incrementDecimal(t, baseCount) {
		t.Fatalf("remote commit count = %s, want exactly one more than %s", finalCount, baseCount)
	}
}

func TestPrivateCommitDisablesCloneHooksAndPreservesExactBytes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, remote, instance := initializedApp(t, root)
	_, state, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hookDir := filepath.Join(state.Private.LocalRepositoryPath, ".git", "hooks")
	if err := os.MkdirAll(hookDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(state.Private.LocalRepositoryPath, ".git", "spas-test-hook-ran")
	script := "#!/bin/sh\nprintf ran > .git/spas-test-hook-ran\nexit 1\n"
	if err := os.WriteFile(filepath.Join(hookDir, "pre-commit"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, state.Private.LocalRepositoryPath, "config", "--local", "core.hooksPath", ".git/hooks")

	want := []byte("LINE=one\r\nBINARY=\x00\r\n")
	if err := os.WriteFile(filepath.Join(publicRoot, "docs", "ARCHITECTURE.md"), want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(ctx, SyncOptions{
		Message:         "Preserve exact bytes",
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	}); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("private clone hook ran: %v", err)
	}
	got := []byte(gitOutput(t, root, "--git-dir="+remote, "show", "main:docs/ARCHITECTURE.md"))
	if !bytes.Equal(got, want) {
		t.Fatalf("remote bytes = %q, want %q", got, want)
	}
}

func TestOverrideWithoutDiscardLeavesDirtyPublicPathUnchanged(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, remote, instance := initializedApp(t, root)
	privateFile := filepath.Join(publicRoot, "docs", "ARCHITECTURE.md")
	if err := os.WriteFile(privateFile, []byte("public committed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, publicRoot, "add", "-f", "docs/ARCHITECTURE.md")
	runGit(t, publicRoot, "commit", "-q", "-m", "public architecture")
	if err := os.WriteFile(privateFile, []byte("public dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	statusBefore := gitOutput(t, publicRoot, "status", "--porcelain=v2", "-z", "--untracked-files=all")
	headBefore := strings.TrimSpace(gitOutput(t, publicRoot, "rev-parse", "HEAD"))

	err := instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictOverride,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	})
	if !errors.Is(err, interaction.ErrDecisionRequired) {
		t.Fatalf("Sync() error = %v, want non-interactive decision required", err)
	}
	content, readErr := os.ReadFile(privateFile)
	if readErr != nil || string(content) != "public dirty\n" {
		t.Fatalf("dirty public path changed to %q, %v", content, readErr)
	}
	if got := gitOutput(t, publicRoot, "status", "--porcelain=v2", "-z", "--untracked-files=all"); got != statusBefore {
		t.Fatalf("public status changed from %q to %q", statusBefore, got)
	}
	if got := strings.TrimSpace(gitOutput(t, publicRoot, "rev-parse", "HEAD")); got != headBefore {
		t.Fatalf("public HEAD changed from %s to %s", headBefore, got)
	}
	if got := gitOutput(t, root, "--git-dir="+remote, "show", "main:docs/ARCHITECTURE.md"); got != "initial\n" {
		t.Fatalf("private remote changed to %q", got)
	}
}

func TestUnlinkDefaultsKeepFilesAndCloneAndRestoreOwnedSettings(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, _, instance := initializedApp(t, root)
	repository, state, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	managedFile := filepath.Join(publicRoot, "docs", "ARCHITECTURE.md")
	clone := state.Private.LocalRepositoryPath

	if err := instance.Unlink(ctx, UnlinkOptions{}); err != nil {
		t.Fatalf("Unlink() error = %v", err)
	}
	if _, err := os.Stat(managedFile); err != nil {
		t.Fatalf("unlink removed workspace file: %v", err)
	}
	if _, err := os.Stat(clone); err != nil {
		t.Fatalf("unlink removed private clone: %v", err)
	}
	if _, err := instance.Store.Load(repository.Root, repository.CommonDir); !errors.Is(err, linkstate.ErrNotLinked) {
		t.Fatalf("link state load error = %v, want ErrNotLinked", err)
	}
	excludePath, err := repository.InfoExcludePath(ctx)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "BEGIN SPAS") {
		t.Fatalf("unlink left the managed exclude block:\n%s", content)
	}
	if got := gitOutput(t, publicRoot, "config", "--local", "--list"); strings.Contains(got, "branch.main.mergeoptions") {
		t.Fatalf("unlink left merge protection:\n%s", got)
	}
}

func TestUnlinkCanExplicitlyRemoveManagedFilesAndPrivateClone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, _, instance := initializedApp(t, root)
	repository, state, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Unlink(ctx, UnlinkOptions{
		RemoveFiles:        true,
		ApproveRemoveFiles: true,
		RemovePrivateClone: true,
	}); err != nil {
		t.Fatalf("Unlink(remove) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(publicRoot, "docs", "ARCHITECTURE.md")); !os.IsNotExist(err) {
		t.Fatalf("managed workspace file still exists: %v", err)
	}
	if _, err := os.Stat(state.Private.LocalRepositoryPath); !os.IsNotExist(err) {
		t.Fatalf("private clone still exists: %v", err)
	}
	if _, err := instance.Store.Load(repository.Root, repository.CommonDir); !errors.Is(err, linkstate.ErrNotLinked) {
		t.Fatalf("link state load error = %v, want ErrNotLinked", err)
	}
}

func TestUnlinkReportsPrivateCloneRemovalFailureAfterDeletingLink(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	_, _, instance := initializedApp(t, root)
	repository, state, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	oldRemove := removePrivateClone
	removePrivateClone = func(string) error { return errors.New("injected removal failure") }
	defer func() { removePrivateClone = oldRemove }()

	err = instance.Unlink(ctx, UnlinkOptions{RemovePrivateClone: true})
	if err == nil {
		t.Fatal("Unlink() error = nil, want cleanup failure")
	}
	if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindOperation {
		t.Fatalf("Unlink() error = %v, kind = %v, want KindOperation", err, kind)
	}
	for _, want := range []string{"workspace was successfully unlinked", state.Private.LocalRepositoryPath, instance.privateRepository(state).StagingPath(), "injected removal failure"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Unlink() error = %v, want %q", err, want)
		}
	}
	if _, err := instance.Store.Load(repository.Root, repository.CommonDir); !errors.Is(err, linkstate.ErrNotLinked) {
		t.Fatalf("link state load error = %v, want ErrNotLinked", err)
	}
	if _, err := os.Stat(state.Private.LocalRepositoryPath); err != nil {
		t.Fatalf("private clone stat after injected failure = %v, want retained clone", err)
	}
}

func TestSyncRejectsFileChangedAfterCommitApproval(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, remote, instance := initializedApp(t, root)
	managedPath := filepath.Join(publicRoot, "docs", "ARCHITECTURE.md")
	if err := os.WriteFile(managedPath, []byte("approved content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, state, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	private := instance.privateRepository(state)
	headBefore, err := private.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	reader := &mutateBeforeFinalByteReader{
		data: []byte("y\nApproved architecture update\n"),
		mutate: func() error {
			return os.WriteFile(managedPath, []byte("changed after approval\n"), 0o600)
		},
	}
	instance.Out = &output
	instance.Err = &output
	instance.Prompt = interaction.Prompter{
		In:          reader,
		Out:         &output,
		Interactive: true,
	}

	err = instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if reader.mutateErr != nil {
		t.Fatalf("test mutation failed: %v", reader.mutateErr)
	}
	if err == nil || !strings.Contains(err.Error(), "changed after commit approval for the linked repository") {
		t.Fatalf("Sync() error = %v, want post-approval change rejection", err)
	}
	headAfter, headErr := private.Head(ctx)
	if headErr != nil {
		t.Fatal(headErr)
	}
	if headAfter != headBefore {
		t.Fatalf("private HEAD changed from %s to %s", headBefore, headAfter)
	}
	if clean, cleanErr := private.IsClean(ctx); cleanErr != nil || !clean {
		t.Fatalf("private clone clean = %v, error = %v", clean, cleanErr)
	}
	if got := gitOutput(t, root, "--git-dir="+remote, "show", "main:docs/ARCHITECTURE.md"); got != "initial\n" {
		t.Fatalf("remote content = %q, want original", got)
	}
}

func TestMergeProtectionPoliciesFailClosedWhenConfigurationCannotBeInstalled(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot := initializePublicRepository(t, root)
	remote := initializePrivateRemoteWithFile(t, root, ".env", "private\n")
	instance, _ := testApp(t, publicRoot, root, remote)
	repository, err := instance.publicRepository(ctx)
	if err != nil {
		t.Fatal(err)
	}

	runGit(t, publicRoot, "checkout", "--detach", "HEAD")
	for _, test := range []struct {
		policy MergeProtectionPolicy
		want   string
	}{
		{policy: MergeEnable, want: "error"},
		{policy: MergeRequire, want: "error"},
		{policy: MergeAsk, want: string(MergeSkip)},
		{policy: MergeSkip, want: string(MergeSkip)},
	} {
		action, err := instance.planMergeProtection(ctx, repository, test.policy)
		if test.want == "error" {
			if err == nil {
				t.Fatalf("detached policy %q error = nil", test.policy)
			}
			if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsafeGitState {
				t.Fatalf("detached policy %q error = %v, kind = %v", test.policy, err, kind)
			}
		} else if err != nil || action != test.want {
			t.Fatalf("detached policy %q = %q, %v; want %q", test.policy, action, err, test.want)
		}
	}

	runGit(t, publicRoot, "checkout", "-q", "main")
	runGit(t, publicRoot, "config", "--local", "--add", "branch.main.mergeOptions", "--no-edit")
	runGit(t, publicRoot, "config", "--local", "--add", "branch.main.mergeOptions", "--log")
	for _, test := range []struct {
		policy MergeProtectionPolicy
		want   string
	}{
		{policy: MergeEnable, want: "error"},
		{policy: MergeRequire, want: "error"},
		{policy: MergeAsk, want: string(MergeSkip)},
		{policy: MergeSkip, want: string(MergeSkip)},
	} {
		action, err := instance.planMergeProtection(ctx, repository, test.policy)
		if test.want == "error" {
			if err == nil {
				t.Fatalf("ambiguous policy %q error = nil", test.policy)
			}
			if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsafeGitState {
				t.Fatalf("ambiguous policy %q error = %v, kind = %v", test.policy, err, kind)
			}
		} else if err != nil || action != test.want {
			t.Fatalf("ambiguous policy %q = %q, %v; want %q", test.policy, action, err, test.want)
		}
	}
}

func TestLinkRejectsLinkedWorktreeBeforeSavingState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot := initializePublicRepository(t, root)
	remote := initializePrivateRemoteWithFile(t, root, ".env", "private\n")
	second := filepath.Join(root, "second-worktree")
	runGit(t, publicRoot, "worktree", "add", "-q", "-b", "second", second)
	instance, _ := testApp(t, publicRoot, root, remote)

	err := instance.Link(ctx, LinkOptions{Repository: "getspas/private-files", DryRun: true})
	if err == nil {
		t.Fatal("Link(dry-run) error = nil, want linked-worktree rejection")
	}
	if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsafeGitState {
		t.Fatalf("Link(dry-run) error = %v, kind = %v, want KindUnsafeGitState", err, kind)
	}
	repository, err := instance.publicRepository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.Store.Load(repository.Root, repository.CommonDir); !errors.Is(err, linkstate.ErrNotLinked) {
		t.Fatalf("link state after rejected dry-run = %v, want ErrNotLinked", err)
	}
}

func TestMutatingCommandsRejectMultiplePublicWorktrees(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, _, instance := initializedApp(t, root)
	second := filepath.Join(root, "second-worktree")
	runGit(t, publicRoot, "worktree", "add", "-q", "-b", "second", second)
	if err := os.WriteFile(filepath.Join(publicRoot, ".env"), []byte("private\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := instance.Add(ctx, AddOptions{
		Paths:           []string{".env"},
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if err == nil || !strings.Contains(err.Error(), "multiple Git worktrees") {
		t.Fatalf("Add() error = %v, want multiple-worktree rejection", err)
	}
	err = instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if err == nil || !strings.Contains(err.Error(), "multiple Git worktrees") {
		t.Fatalf("Sync() error = %v, want multiple-worktree rejection", err)
	}
	err = instance.Remove(ctx, RemoveOptions{Paths: []string{"docs/ARCHITECTURE.md"}})
	if err == nil || !strings.Contains(err.Error(), "multiple Git worktrees") {
		t.Fatalf("Remove() error = %v, want multiple-worktree rejection", err)
	}
	err = instance.Unlink(ctx, UnlinkOptions{})
	if err == nil || !strings.Contains(err.Error(), "multiple Git worktrees") {
		t.Fatalf("Unlink() error = %v, want multiple-worktree rejection", err)
	}
	var output bytes.Buffer
	instance.Out = &output
	instance.Err = &output
	instance.JSON = true
	err = instance.Doctor(ctx)
	var written OutputWrittenError
	if !errors.As(err, &written) {
		t.Fatalf("Doctor() error = %v, want OutputWrittenError", err)
	}
	var doctor DoctorResult
	if decodeErr := json.Unmarshal(output.Bytes(), &doctor); decodeErr != nil {
		t.Fatalf("decode Doctor() output: %v\n%s", decodeErr, output.String())
	}
	if doctor.Healthy || doctor.Errors == 0 {
		t.Fatalf("Doctor() = %#v, want multiple-worktree error", doctor)
	}
}

func TestSyncRechecksCasePolicy(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, _, instance := initializedApp(t, root)
	repository, _, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	filesystemIgnoresCase, err := repository.FilesystemIgnoresCase()
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, publicRoot, "config", "--local", "core.ignoreCase", boolText(!filesystemIgnoresCase))
	var output bytes.Buffer
	instance.Out = &output
	instance.Err = &output

	if err := instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	}); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if !strings.Contains(output.String(), "core.ignoreCase") || !strings.Contains(output.String(), "case-insensitive") {
		t.Fatalf("Sync() output has no case-policy warning:\n%s", output.String())
	}
}

func TestSyncRejectsUnsafePrivateCloneStates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		prepare     func(*testing.T, string, string)
		wantInError string
	}{
		{
			name: "dirty private clone",
			prepare: func(t *testing.T, privateClone, _ string) {
				if err := os.WriteFile(filepath.Join(privateClone, "unexpected.tmp"), []byte("dirty\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantInError: "private clone is not clean",
		},
		{
			name: "wrong private branch",
			prepare: func(t *testing.T, privateClone, _ string) {
				runGit(t, privateClone, "switch", "-q", "-c", "other")
			},
			wantInError: "expected \"main\"",
		},
		{
			name: "remote branch disappeared",
			prepare: func(t *testing.T, _, remote string) {
				runGit(t, rootOf(remote), "--git-dir="+remote, "update-ref", "-d", "refs/heads/main")
			},
			wantInError: "disappeared",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			root := t.TempDir()
			publicRoot, remote, instance := initializedApp(t, root)
			_, state, err := instance.linked(ctx)
			if err != nil {
				t.Fatal(err)
			}
			test.prepare(t, state.Private.LocalRepositoryPath, remote)
			err = instance.Sync(ctx, SyncOptions{
				Conflict:        ConflictAbort,
				ExistingExclude: ExcludePreserve,
				MergeProtection: MergeEnable,
			})
			if err == nil || !strings.Contains(err.Error(), test.wantInError) {
				t.Fatalf("Sync() error = %v, want %q", err, test.wantInError)
			}
			if got := gitOutput(t, publicRoot, "status", "--porcelain"); got != "" {
				t.Fatalf("unsafe private state changed public status: %q", got)
			}
		})
	}
}

func initializePublicRepository(t *testing.T, root string) string {
	t.Helper()
	publicRoot := filepath.Join(root, "public")
	if err := os.MkdirAll(publicRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, publicRoot, "init", "-q", "-b", "main")
	runGit(t, publicRoot, "config", "user.name", "SPAS Test")
	runGit(t, publicRoot, "config", "user.email", "spas@example.invalid")
	if err := os.WriteFile(filepath.Join(publicRoot, "README.md"), []byte("public\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, publicRoot, "add", "README.md")
	runGit(t, publicRoot, "commit", "-q", "-m", "public initial")
	return publicRoot
}

func initializePrivateRemoteWithFile(t *testing.T, root, relativePath, content string) string {
	t.Helper()
	source := filepath.Join(root, "private-source")
	remote := filepath.Join(root, "private.git")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(source, relativePath)), 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "--bare", "-q", remote)
	runGit(t, root, "init", "-q", "-b", "main", source)
	runGit(t, source, "config", "user.name", "Private Source")
	runGit(t, source, "config", "user.email", "private@example.invalid")
	if err := os.WriteFile(filepath.Join(source, relativePath), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "--", relativePath)
	runGit(t, source, "commit", "-q", "-m", "private initial")
	runGit(t, source, "remote", "add", "origin", remote)
	runGit(t, source, "push", "-q", "origin", "main")
	runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	return remote
}

func incrementDecimal(t *testing.T, value string) string {
	t.Helper()
	count, err := strconv.Atoi(value)
	if err != nil {
		t.Fatalf("parse commit count %q: %v", value, err)
	}
	return strconv.Itoa(count + 1)
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func rootOf(path string) string {
	return filepath.Dir(path)
}

func sameDirectory(t *testing.T, left, right string) bool {
	t.Helper()
	leftInfo, err := os.Stat(left)
	if err != nil {
		t.Fatal(err)
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(leftInfo, rightInfo)
}

func TestUnlinkRemoveFilesIncludesPendingAdditions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, _, instance := initializedApp(t, root)
	pending := filepath.Join(publicRoot, ".env")
	if err := os.WriteFile(pending, []byte("PRIVATE=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Add(ctx, AddOptions{
		Paths:           []string{".env"},
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	}); err != nil {
		t.Fatal(err)
	}
	if err := instance.Unlink(ctx, UnlinkOptions{
		Force:              true,
		RemoveFiles:        true,
		ApproveRemoveFiles: true,
	}); err != nil {
		t.Fatalf("Unlink(remove pending add) error = %v", err)
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatalf("pending addition still exists after unlink --remove-files: %v", err)
	}
}

func TestAddRejectsGitControlFilesBeforeEnrollment(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, _, instance := initializedApp(t, root)
	for _, value := range []string{".gitattributes", ".gitignore", ".gitmodules"} {
		if err := os.WriteFile(filepath.Join(publicRoot, value), []byte("private\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := instance.Add(ctx, AddOptions{
			Paths:           []string{value},
			ExistingExclude: ExcludePreserve,
			MergeProtection: MergeSkip,
		})
		if err == nil {
			t.Fatalf("Add(%q) error = nil", value)
		}
	}
}

func TestTrackedOverrideRejectsContentChangeWithStableGitStatus(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, remote, instance := initializedApp(t, root)
	path := filepath.Join(publicRoot, "docs", "ARCHITECTURE.md")
	if err := os.WriteFile(path, []byte("public committed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, publicRoot, "add", "-f", "docs/ARCHITECTURE.md")
	runGit(t, publicRoot, "commit", "-q", "-m", "public architecture")
	if err := os.WriteFile(path, []byte("dirty before approval\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	publicHeadBefore := strings.TrimSpace(gitOutput(t, publicRoot, "rev-parse", "HEAD"))

	var output bytes.Buffer
	reader := &mutateBeforeFinalByteReader{
		data: []byte("y\n"),
		mutate: func() error {
			// Both contents produce the same porcelain status (.M). The byte
			// snapshot, not merely the status record, must protect the edit made
			// after the user approved discarding the previous contents.
			return os.WriteFile(path, []byte("dirty after approval\n"), 0o600)
		},
	}
	instance.Out = &output
	instance.Err = &output
	instance.Prompt = interaction.Prompter{
		In:          reader,
		Out:         &output,
		Interactive: true,
	}

	err := instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictOverride,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if reader.mutateErr != nil {
		t.Fatalf("test mutation failed: %v", reader.mutateErr)
	}
	if err == nil || !strings.Contains(err.Error(), "changed after ownership override approval") {
		t.Fatalf("Sync() error = %v, want post-approval byte-change rejection", err)
	}
	if content, readErr := os.ReadFile(path); readErr != nil || string(content) != "dirty after approval\n" {
		t.Fatalf("workspace content = %q, %v; want post-approval edit preserved", content, readErr)
	}
	if got := strings.TrimSpace(gitOutput(t, publicRoot, "rev-parse", "HEAD")); got != publicHeadBefore {
		t.Fatalf("public HEAD changed from %s to %s", publicHeadBefore, got)
	}
	if got := gitOutput(t, publicRoot, "diff", "--cached", "--name-only"); got != "" {
		t.Fatalf("public staged changes = %q, want none", got)
	}
	if got := gitOutput(t, root, "--git-dir="+remote, "show", "main:docs/ARCHITECTURE.md"); got != "initial\n" {
		t.Fatalf("private remote content = %q, want original", got)
	}
}

func TestLinkReplacementRequiresUnlinkForManagedGitConfiguration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot := initializePublicRepository(t, root)
	instance, _ := testApp(t, publicRoot, root, filepath.Join(root, "unused.git"))
	if err := instance.Link(ctx, LinkOptions{
		Repository: "getspas/original-private-files",
		Branch:     "main",
	}); err != nil {
		t.Fatalf("Link() error = %v", err)
	}
	repository, state, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state.Merge.ManagedBranches["main"] = linkstate.ManagedBranch{
		BeforePresent: false,
		After:         "--no-overwrite-ignore",
	}
	if err := instance.Store.Save(state); err != nil {
		t.Fatal(err)
	}

	err = instance.Link(ctx, LinkOptions{
		Repository: "getspas/replacement-private-files",
		Branch:     "main",
		Replace:    true,
	})
	if err == nil || !strings.Contains(err.Error(), "run spas unlink") {
		t.Fatalf("Link(replace) error = %v, want unlink requirement", err)
	}
	remaining, err := instance.Store.Load(repository.Root, repository.CommonDir)
	if err != nil {
		t.Fatal(err)
	}
	if remaining.Private.Repository != "getspas/original-private-files" {
		t.Fatalf("private repository = %q, want original link retained", remaining.Private.Repository)
	}
	if _, ok := remaining.Merge.ManagedBranches["main"]; !ok {
		t.Fatal("managed merge-protection restoration state was lost")
	}
}

func TestLinkReplacementRejectsCloneInitialization(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name           string
		initialization *linkstate.CloneInitialization
	}{
		{
			name: "preparing",
			initialization: &linkstate.CloneInitialization{
				Phase:           linkstate.ClonePreparing,
				RequestedBranch: "main",
			},
		},
		{
			name: "prepared",
			initialization: &linkstate.CloneInitialization{
				Phase:           linkstate.ClonePrepared,
				RequestedBranch: "main",
				Branch:          "main",
				Head:            strings.Repeat("a", 40),
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			root := t.TempDir()
			publicRoot := initializePublicRepository(t, root)
			instance, _ := testApp(t, publicRoot, root, filepath.Join(root, "unused.git"))
			if err := instance.Link(ctx, LinkOptions{
				Repository: "getspas/original-private-files",
				Branch:     "main",
			}); err != nil {
				t.Fatalf("Link() error = %v", err)
			}
			repository, state, err := instance.linked(ctx)
			if err != nil {
				t.Fatal(err)
			}
			state.Private.Initialization = test.initialization
			if err := instance.Store.Save(state); err != nil {
				t.Fatal(err)
			}

			err = instance.Link(ctx, LinkOptions{
				Repository: "getspas/replacement-private-files",
				Branch:     "main",
				Replace:    true,
			})
			if err == nil || !strings.Contains(err.Error(), "run spas unlink") {
				t.Fatalf("Link(replace) error = %v, want initialization recovery rejection", err)
			}
			remaining, err := instance.Store.Load(repository.Root, repository.CommonDir)
			if err != nil {
				t.Fatal(err)
			}
			if remaining.Private.Initialization == nil || remaining.Private.Initialization.Phase != test.initialization.Phase {
				t.Fatalf("initialization after rejected replacement = %+v, want phase %q", remaining.Private.Initialization, test.initialization.Phase)
			}
		})
	}
}

func TestLinkedRejectsTamperedPrivateBranch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot := initializePublicRepository(t, root)
	instance, _ := testApp(t, publicRoot, root, filepath.Join(root, "private.git"))
	if err := instance.Link(ctx, LinkOptions{Repository: "getspas/private-files", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	repository, state, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state.Private.Branch = "--option-shaped-branch"
	if err := instance.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	if _, _, err := instance.linked(ctx); err == nil || !strings.Contains(err.Error(), "invalid private branch") {
		t.Fatalf("linked() error = %v, want invalid private branch rejection", err)
	}
	canonicalPublicRoot, err := filepath.EvalSymlinks(publicRoot)
	if err != nil {
		t.Fatal(err)
	}
	if repository.Root != canonicalPublicRoot {
		t.Fatalf("public repository root = %q, want %q", repository.Root, canonicalPublicRoot)
	}
}

func TestLinkedStrictlyReResolvesPersistedRepositoryIdentity(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		mutate func(*linkstate.State)
		want   string
	}{
		{
			name: "unknown provider",
			mutate: func(state *linkstate.State) {
				state.Private.Provider = "other"
			},
			want: "unknown repository provider",
		},
		{
			name: "remote URL mismatch",
			mutate: func(state *linkstate.State) {
				state.Private.RemoteURL += ".tampered"
			},
			want: "does not match provider resolution",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			root := t.TempDir()
			publicRoot := initializePublicRepository(t, root)
			instance, _ := testApp(t, publicRoot, root, filepath.Join(root, "private.git"))
			if err := instance.Link(ctx, LinkOptions{Repository: "getspas/private-files", Branch: "main"}); err != nil {
				t.Fatal(err)
			}
			repository, state, err := instance.linked(ctx)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&state)
			if err := instance.Store.Save(state); err != nil {
				t.Fatal(err)
			}
			if _, err := instance.loadState(repository.Root, repository.CommonDir); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("loadState() error = %v, want %q", err, test.want)
			}
		})
	}
}
