package privategit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/getspas/spas/internal/gitexec"
	"github.com/getspas/spas/internal/limits"
	"github.com/getspas/spas/internal/pathmodel"
	"github.com/getspas/spas/internal/spaserr"
)

func publishCloneForTest(t *testing.T, repository Repository, ctx context.Context, remoteURL, branch string) InitResult {
	t.Helper()
	if err := repository.RemoveStagingClone(); err != nil {
		t.Fatal(err)
	}
	result, err := repository.PrepareClone(ctx, remoteURL, branch)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishPreparedClone(ctx, result); err != nil {
		t.Fatal(err)
	}
	if err := repository.VerifyPublishedClone(ctx, remoteURL, result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestMain(m *testing.M) {
	if os.Getenv("SPAS_PRIVATEGIT_PROXY") != "" {
		os.Exit(runPrivateGitProxy())
	}
	os.Exit(m.Run())
}

func runPrivateGitProxy() int {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 1
	}
	if recordPath := os.Getenv("SPAS_PRIVATEGIT_RECORD"); recordPath != "" {
		record := strings.Join(os.Args[1:], "\x00") + "\n--stdin--\n" + string(input)
		if err := os.WriteFile(recordPath, []byte(record), 0o600); err != nil {
			return 1
		}
	}
	command := exec.Command(os.Getenv("SPAS_PRIVATEGIT_REAL_GIT"), os.Args[1:]...)
	command.Stdin = bytes.NewReader(input)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return 127
	}
	return 0
}

func TestParseTreeCountsAllEntriesBeforeSemanticValidation(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	for index := range limits.MaxPrivateTreeEntries {
		fmt.Fprintf(
			&output,
			"120000 blob %040x 1\tunsupported-%05d\x00",
			index+1,
			index,
		)
	}
	entries, err := parseTree(output.Bytes())
	if err != nil {
		t.Fatalf("parseTree() error = %v", err)
	}
	if len(entries) != limits.MaxPrivateTreeEntries {
		t.Fatalf("parseTree() entries = %d, want %d", len(entries), limits.MaxPrivateTreeEntries)
	}
	if entries[0].Mode != "120000" {
		t.Fatalf("parseTree() first mode = %q, want unsupported mode retained for later validation", entries[0].Mode)
	}

	output.WriteString("120000 blob ffffffffffffffffffffffffffffffffffffffff 1\textra\x00")
	_, err = parseTree(output.Bytes())
	var limitErr *TreeLimitError
	if !errors.As(err, &limitErr) || limitErr.Metric != "tree-entry" {
		t.Fatalf("parseTree() error = %v, want tree-entry limit before semantic validation", err)
	}
}

func TestParseTreeEnforcesMetadataLimitWithoutParsingTruncatedOutput(t *testing.T) {
	t.Parallel()

	exact := make([]byte, limits.MaxPrivateTreeMetadataBytes)
	_, exactErr := parseTree(exact)
	var exactLimitErr *TreeLimitError
	if errors.As(exactErr, &exactLimitErr) && exactLimitErr.Metric == "tree-metadata byte" {
		t.Fatalf("exact metadata limit returned size error: %v", exactErr)
	}

	over := make([]byte, limits.MaxPrivateTreeMetadataBytes+1)
	_, err := parseTree(over)
	var limitErr *TreeLimitError
	if !errors.As(err, &limitErr) || limitErr.Metric != "tree-metadata byte" {
		t.Fatalf("parseTree() error = %v, want tree-metadata byte limit", err)
	}
}

func TestCloneInitializedRepository(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	source := filepath.Join(root, "source")
	runGit(t, root, "init", "--bare", "-q", remote)
	runGit(t, root, "init", "-q", "-b", "main", source)
	runGit(t, source, "config", "user.name", "SPAS Test")
	runGit(t, source, "config", "user.email", "spas@example.invalid")
	if err := os.WriteFile(filepath.Join(source, ".env"), []byte("TEST=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", ".env")
	runGit(t, source, "commit", "-q", "-m", "initial")
	runGit(t, source, "remote", "add", "origin", remote)
	runGit(t, source, "push", "-q", "-u", "origin", "main")
	runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")

	repository := Repository{
		Path:      filepath.Join(root, "clone"),
		Git:       gitexec.Runner{},
		SafetyDir: filepath.Join(root, "safety"),
	}
	got := publishCloneForTest(t, repository, ctx, remote, "")
	if got.Branch != "main" || got.Empty {
		t.Fatalf("Clone() = %#v", got)
	}
	paths, err := repository.TrackedPaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != ".env" {
		t.Fatalf("TrackedPaths() = %v", paths)
	}
}

func TestPreparePublishAndVerifyCloneUsesDeterministicStaging(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	source := filepath.Join(root, "source")
	runGit(t, root, "init", "--bare", "-q", remote)
	runGit(t, root, "init", "-q", "-b", "main", source)
	runGit(t, source, "config", "user.name", "SPAS Test")
	runGit(t, source, "config", "user.email", "spas@example.invalid")
	if err := os.WriteFile(filepath.Join(source, ".env"), []byte("TEST=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", ".env")
	runGit(t, source, "commit", "-q", "-m", "initial")
	runGit(t, source, "remote", "add", "origin", remote)
	runGit(t, source, "push", "-q", "-u", "origin", "main")
	runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")

	repository := Repository{
		Path:      filepath.Join(root, "clone"),
		Git:       gitexec.Runner{},
		SafetyDir: filepath.Join(root, "safety"),
	}
	result, err := repository.PrepareClone(ctx, remote, "")
	if err != nil {
		t.Fatalf("PrepareClone() error = %v", err)
	}
	if result.Branch != "main" || result.Empty || result.Head == "" {
		t.Fatalf("PrepareClone() = %#v", result)
	}
	if _, err := os.Stat(repository.StagingPath()); err != nil {
		t.Fatalf("staging clone = %v", err)
	}
	if _, err := os.Stat(repository.Path); !os.IsNotExist(err) {
		t.Fatalf("final clone exists before publish: %v", err)
	}
	if err := (&Repository{Path: repository.StagingPath(), Git: repository.Git, SafetyDir: repository.SafetyDir}).VerifyPublishedClone(ctx, remote, result); err != nil {
		t.Fatalf("VerifyPublishedClone(staging) error = %v", err)
	}
	if err := repository.PublishPreparedClone(ctx, result); err != nil {
		t.Fatalf("PublishPreparedClone() error = %v", err)
	}
	if err := repository.VerifyPublishedClone(ctx, remote, result); err != nil {
		t.Fatalf("VerifyPublishedClone() error = %v", err)
	}
	if _, err := os.Stat(repository.StagingPath()); !os.IsNotExist(err) {
		t.Fatalf("staging clone remains after publish: %v", err)
	}
}

func TestRemoveStagingCloneDoesNotRemoveFinalClone(t *testing.T) {
	t.Parallel()

	repository := Repository{Path: filepath.Join(t.TempDir(), "clone")}
	if err := os.MkdirAll(repository.StagingPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repository.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := repository.RemoveStagingClone(); err != nil {
		t.Fatalf("RemoveStagingClone() error = %v", err)
	}
	if _, err := os.Stat(repository.StagingPath()); !os.IsNotExist(err) {
		t.Fatalf("staging path remains: %v", err)
	}
	if _, err := os.Stat(repository.Path); err != nil {
		t.Fatalf("RemoveStagingClone() removed final path: %v", err)
	}
}

func TestMergeInProgressTreatsSymlinkMarkerAsPresent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(gitDir, "MERGE_HEAD")
	if err := os.Symlink("MERGE_HEAD", marker); err != nil {
		t.Skipf("cannot create a merge-marker symlink: %v", err)
	}

	inProgress, err := (Repository{Path: root}).MergeInProgress()
	if err != nil {
		t.Fatal(err)
	}
	if !inProgress {
		t.Fatal("MergeInProgress() = false for a symlink marker, want true")
	}
}

func TestMergeInProgressReturnsMarkerInspectionError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.Symlink(".git", gitDir); err != nil {
		t.Skipf("cannot create a Git-directory symlink: %v", err)
	}

	inProgress, err := (Repository{Path: root}).MergeInProgress()
	if err == nil {
		t.Fatalf("MergeInProgress() = %t, nil; want marker inspection error", inProgress)
	}
}

func TestEnsureSafetyValidatesPartialOrPromisorConfiguration(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		key     string
		value   string
		wantErr bool
	}{
		{name: "partial clone extension", key: "extensions.partialClone", value: "origin", wantErr: true},
		{name: "partial clone filter", key: "remote.origin.partialCloneFilter", value: "blob:none", wantErr: true},
		{name: "promisor remote true", key: "remote.origin.promisor", value: "true", wantErr: true},
		{name: "promisor remote false", key: "remote.origin.promisor", value: "false", wantErr: false},
		{name: "promisor remote no", key: "remote.origin.promisor", value: "no", wantErr: false},
		{name: "promisor remote malformed", key: "remote.origin.promisor", value: "not-a-boolean", wantErr: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			runGit(t, root, "init", "-q", "-b", "main")
			runGit(t, root, "config", "--local", test.key, test.value)
			repository := Repository{
				Path:      root,
				Git:       gitexec.Runner{},
				SafetyDir: filepath.Join(t.TempDir(), "safety"),
			}
			err := repository.EnsureSafety(context.Background())
			if test.wantErr && err == nil {
				t.Fatal("EnsureSafety() error = nil, want partial/promisor rejection")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("EnsureSafety() error = %v, want false promisor configuration accepted", err)
			}
		})
	}
}

func TestStageRemovalsUsesStdinForLargePathsets(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	var paths []pathmodel.Path
	for index := range 2200 {
		name := fmt.Sprintf("entry-%04d-abcdefghijklmnop.txt", index)
		if err := os.WriteFile(filepath.Join(root, name), []byte("private\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		path, err := pathmodel.Parse(name)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	runGit(t, root, "add", "--all")

	repository := Repository{
		Path:      root,
		Git:       gitexec.Runner{},
		SafetyDir: filepath.Join(t.TempDir(), "safety"),
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(root, "git-rm-record")
	t.Setenv("SPAS_PRIVATEGIT_PROXY", "record")
	t.Setenv("SPAS_PRIVATEGIT_REAL_GIT", realGit)
	t.Setenv("SPAS_PRIVATEGIT_RECORD", recordPath)
	repository.Git.Path = os.Args[0]
	if err := repository.StageRemovals(ctx, paths); err != nil {
		t.Fatalf("StageRemovals() error = %v", err)
	}
	record, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(record), "--quiet") {
		t.Fatalf("git rm arguments = %q, want --quiet", record)
	}
	t.Setenv("SPAS_PRIVATEGIT_PROXY", "")
	repository.Git.Path = ""
	tracked, err := repository.TrackedPaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracked) != 0 {
		t.Fatalf("TrackedPaths() = %d paths after removal, want none", len(tracked))
	}
}

func TestRollbackChangesUsesLiteralStdinPathspecs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	var paths []pathmodel.Path
	for _, name := range []string{"-private.env", "private,notes.env"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("private\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		path, err := pathmodel.Parse(name)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	runGit(t, root, "add", "--all")

	repository := Repository{
		Path:      root,
		Git:       gitexec.Runner{},
		SafetyDir: filepath.Join(t.TempDir(), "safety"),
	}
	if err := repository.RollbackChanges(ctx, "", paths); err != nil {
		t.Fatalf("RollbackChanges() error = %v", err)
	}
	tracked, err := repository.TrackedPaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracked) != 0 {
		t.Fatalf("TrackedPaths() = %v after rollback, want none", tracked)
	}
	for _, path := range paths {
		if _, err := os.Lstat(path.OSPath(root)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rolled-back path %q = %v, want absent", path, err)
		}
	}
}

func TestPrepareSafetyFilesResetsHooksAndAttributes(t *testing.T) {
	t.Parallel()

	repository := Repository{SafetyDir: filepath.Join(t.TempDir(), "safety")}
	if err := os.MkdirAll(repository.hooksDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository.hooksDir(), "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repository.SafetyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repository.attributesFile(), []byte("*.secret filter=unsafe\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := repository.prepareSafetyFiles(); err != nil {
		t.Fatalf("prepareSafetyFiles() error = %v", err)
	}
	entries, err := os.ReadDir(repository.hooksDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("hooks directory entries = %v, want empty", entries)
	}
	data, err := os.ReadFile(repository.attributesFile())
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("attributes file = %q, want empty", data)
	}
}

func TestParseTreeAcceptsLongFormat(t *testing.T) {
	t.Parallel()

	entries, err := parseTree([]byte("100644 blob 0123456789abcdef 3\tfile.txt\x00"))
	if err != nil {
		t.Fatalf("parseTree() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Size != 3 {
		t.Fatalf("parseTree() = %+v, want one 3-byte entry", entries)
	}
}

func TestValidateBranchNameRejectsPreviousCheckoutExpression(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "config", "user.name", "SPAS Test")
	runGit(t, root, "config", "user.email", "spas@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "tracked.txt")
	runGit(t, root, "commit", "-q", "-m", "base")
	runGit(t, root, "branch", "other")
	runGit(t, root, "switch", "-q", "other")
	runGit(t, root, "switch", "-q", "main")

	if err := ValidateBranchName(context.Background(), gitexec.Runner{}, root, "main"); err != nil {
		t.Fatalf("ValidateBranchName(main) error = %v", err)
	}
	if err := ValidateBranchName(context.Background(), gitexec.Runner{}, root, "@{-1}"); err == nil {
		t.Fatal("ValidateBranchName(@{-1}) error = nil, want previous-checkout expression rejection")
	}
}

func TestHeadRejectsNonCommitRef(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "config", "user.name", "SPAS Test")
	runGit(t, root, "config", "user.email", "spas@example.invalid")
	blob := hashBlob(t, root, "not a commit")
	runGit(t, root, "update-ref", "refs/not-a-commit", blob)
	runGit(t, root, "symbolic-ref", "HEAD", "refs/not-a-commit")
	repository := Repository{Path: root, Git: gitexec.Runner{}, SafetyDir: filepath.Join(t.TempDir(), "safety")}
	if head, err := repository.Head(context.Background()); err == nil || head != "" {
		t.Fatalf("Head() = %q, %v, want non-commit error", head, err)
	}
}

func TestRemoteHeadRejectsNonCommitRef(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	blob := hashBlob(t, root, "not a commit")
	runGit(t, root, "update-ref", "refs/remotes/origin/main", blob)
	repository := Repository{Path: root, Git: gitexec.Runner{}, SafetyDir: filepath.Join(t.TempDir(), "safety")}
	if head, err := repository.RemoteHead(context.Background(), "main"); err == nil || head != "" {
		t.Fatalf("RemoteHead() = %q, %v, want non-commit error", head, err)
	}
}

func TestHeadAndRemoteHeadRejectCorruptReferences(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	gitDir := strings.TrimSpace(gitOutput(t, root, "rev-parse", "--absolute-git-dir"))
	if err := os.MkdirAll(filepath.Join(gitDir, "refs", "heads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "refs", "heads", "main"), []byte("not-an-object\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := Repository{Path: root, Git: gitexec.Runner{}, SafetyDir: filepath.Join(t.TempDir(), "safety")}
	if _, err := repository.Head(ctx); err == nil {
		t.Fatal("Head() error = nil, want corrupt-reference error")
	}

	if err := os.MkdirAll(filepath.Join(gitDir, "refs", "remotes", "origin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "refs", "remotes", "origin", "main"), []byte("not-an-object\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RemoteHead(ctx, "main"); err == nil {
		t.Fatal("RemoteHead() error = nil, want corrupt-reference error")
	}
}

func TestCloneEmptyRequiresBranch(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	runGit(t, root, "init", "--bare", "-q", remote)
	repository := Repository{
		Path:      filepath.Join(root, "clone"),
		Git:       gitexec.Runner{},
		SafetyDir: filepath.Join(root, "safety"),
	}
	if _, err := repository.PrepareClone(context.Background(), remote, ""); !errors.Is(err, ErrEmptyNeedsBranch) {
		t.Fatalf("PrepareClone() error = %v, want ErrEmptyNeedsBranch", err)
	}
	if err := repository.RemoveStagingClone(); err != nil {
		t.Fatal(err)
	}
}

func TestCloneRejectsInvalidTreeBeforePublishingDestination(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	source := filepath.Join(root, "source")
	runGit(t, root, "init", "--bare", "-q", remote)
	runGit(t, root, "init", "-q", "-b", "main", source)
	runGit(t, source, "config", "user.name", "SPAS Test")
	runGit(t, source, "config", "user.email", "spas@example.invalid")
	pointer := "version https://git-lfs.github.com/spec/v1\n" +
		"oid sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n" +
		"size 42\n"
	if err := os.WriteFile(filepath.Join(source, "private.bin"), []byte(pointer), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "private.bin")
	runGit(t, source, "commit", "-q", "-m", "invalid private tree")
	runGit(t, source, "remote", "add", "origin", remote)
	runGit(t, source, "push", "-q", "origin", "main")
	runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")

	repository := Repository{
		Path:      filepath.Join(root, "clone"),
		Git:       gitexec.Runner{},
		SafetyDir: filepath.Join(root, "safety"),
	}
	_, err := repository.PrepareClone(ctx, remote, "")
	if err == nil || !strings.Contains(err.Error(), "Git LFS pointer") {
		t.Fatalf("PrepareClone() error = %v, want LFS rejection", err)
	}
	if cleanupErr := repository.RemoveStagingClone(); cleanupErr != nil {
		t.Fatal(cleanupErr)
	}
	if _, err := os.Stat(repository.Path); !os.IsNotExist(err) {
		t.Fatalf("failed clone published its destination: %v", err)
	}
	temporary, err := filepath.Glob(filepath.Join(root, ".spas-clone-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporary) != 0 {
		t.Fatalf("failed clone left temporary directories: %v", temporary)
	}
}

func TestStageCommitAndPush(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	runGit(t, root, "init", "--bare", "-q", remote)
	repository := Repository{
		Path:      filepath.Join(root, "clone"),
		Git:       gitexec.Runner{},
		SafetyDir: filepath.Join(root, "safety"),
	}
	publishCloneForTest(t, repository, ctx, remote, "main")
	runGit(t, repository.Path, "config", "user.name", "SPAS Test")
	runGit(t, repository.Path, "config", "user.email", "spas@example.invalid")
	if err := os.WriteFile(filepath.Join(repository.Path, ".env"), []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, _ := pathmodel.Parse(".env")
	if err := repository.Stage(ctx, []pathmodel.Path{path}); err != nil {
		t.Fatal(err)
	}
	if err := repository.Commit(ctx, "Add environment"); err != nil {
		t.Fatal(err)
	}
	if err := repository.Push(ctx, "main"); err != nil {
		t.Fatal(err)
	}
}

func TestValidateTreeRejectsNestedGitAttributes(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "config", "user.name", "SPAS Test")
	runGit(t, root, "config", "user.email", "spas@example.invalid")
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", ".gitattributes"), []byte("* filter=unsafe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "nested/.gitattributes")
	runGit(t, root, "commit", "-q", "-m", "attributes")

	repository := Repository{
		Path:      root,
		Git:       gitexec.Runner{},
		SafetyDir: filepath.Join(t.TempDir(), "safety"),
	}
	err := repository.ValidateTree(context.Background(), "HEAD")
	if err == nil || !strings.Contains(err.Error(), ".gitattributes") {
		t.Fatalf("ValidateTree() error = %v, want nested .gitattributes rejection", err)
	}
}

func TestValidateTreeRejectsUnsupportedModesAndMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		mode        string
		path        string
		content     string
		wantInError string
	}{
		{name: "symlink", mode: "120000", path: "private-link", content: "target", wantInError: "unsupported Git mode 120000"},
		{name: "gitmodules", mode: "100644", path: "nested/.gitmodules", content: "[submodule]\n", wantInError: ".gitmodules"},
		{name: "gitignore", mode: "100644", path: "nested/.gitignore", content: "!secret\n", wantInError: ".gitignore"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			runGit(t, root, "init", "-q", "-b", "main")
			runGit(t, root, "config", "user.name", "SPAS Test")
			runGit(t, root, "config", "user.email", "spas@example.invalid")
			blob := hashBlob(t, root, test.content)
			runGit(t, root, "update-index", "--add", "--cacheinfo", test.mode+","+blob+","+test.path)
			runGit(t, root, "commit", "-q", "-m", test.name)

			repository := Repository{
				Path:      root,
				Git:       gitexec.Runner{},
				SafetyDir: filepath.Join(t.TempDir(), "safety"),
			}
			err := repository.ValidateTree(context.Background(), "HEAD")
			if err == nil || !strings.Contains(err.Error(), test.wantInError) {
				t.Fatalf("ValidateTree() error = %v, want %q", err, test.wantInError)
			}
		})
	}
}

func TestValidateTreeRejectsGitLFSPointer(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "config", "user.name", "SPAS Test")
	runGit(t, root, "config", "user.email", "spas@example.invalid")
	content := "version https://git-lfs.github.com/spec/v1\n" +
		"oid sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n" +
		"size 1234\n"
	if err := os.WriteFile(filepath.Join(root, "design.psd"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "design.psd")
	runGit(t, root, "commit", "-q", "-m", "lfs pointer")

	repository := Repository{
		Path:      root,
		Git:       gitexec.Runner{},
		SafetyDir: filepath.Join(t.TempDir(), "safety"),
	}
	err := repository.ValidateTree(context.Background(), "HEAD")
	if err == nil || !strings.Contains(err.Error(), "Git LFS pointer") {
		t.Fatalf("ValidateTree() error = %v, want Git LFS rejection", err)
	}
}

func TestValidateTreeAllowsOrdinaryTextMentioningLFSVersion(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "config", "user.name", "SPAS Test")
	runGit(t, root, "config", "user.email", "spas@example.invalid")
	content := "version https://git-lfs.github.com/spec/v1\n" +
		"This document explains the pointer format without being a pointer.\n"
	if err := os.WriteFile(filepath.Join(root, "LFS-NOTES.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "LFS-NOTES.md")
	runGit(t, root, "commit", "-q", "-m", "documentation")

	repository := Repository{
		Path:      root,
		Git:       gitexec.Runner{},
		SafetyDir: filepath.Join(t.TempDir(), "safety"),
	}
	if err := repository.ValidateTree(context.Background(), "HEAD"); err != nil {
		t.Fatalf("ValidateTree() rejected ordinary text: %v", err)
	}
}

func TestValidateTreeRejectsPortableCaseConflict(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "config", "user.name", "SPAS Test")
	runGit(t, root, "config", "user.email", "spas@example.invalid")
	first := hashBlob(t, root, "first")
	second := hashBlob(t, root, "second")
	runGit(t, root, "update-index", "--add", "--cacheinfo", "100644,"+first+",Docs/Plan.md")
	runGit(t, root, "update-index", "--add", "--cacheinfo", "100644,"+second+",docs/plan.md")
	runGit(t, root, "commit", "-q", "-m", "portable conflict")

	repository := Repository{
		Path:      root,
		Git:       gitexec.Runner{},
		SafetyDir: filepath.Join(t.TempDir(), "safety"),
	}
	err := repository.ValidateTree(context.Background(), "HEAD")
	if err == nil || !strings.Contains(err.Error(), "non-portable") {
		t.Fatalf("ValidateTree() error = %v, want portability conflict", err)
	}
}

func TestVerifyOriginRejectsPushURLAndMultipleOrigins(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		configure   func(*testing.T, string, string)
		wantInError string
	}{
		{
			name: "push URL",
			configure: func(t *testing.T, root, _ string) {
				runGit(t, root, "config", "--local", "remote.origin.pushurl", "ssh://git@github.com/attacker/other.git")
			},
			wantInError: "unsupported origin push URL",
		},
		{
			name: "multiple origins",
			configure: func(t *testing.T, root, _ string) {
				runGit(t, root, "config", "--local", "--add", "remote.origin.url", "ssh://git@github.com/attacker/other.git")
			},
			wantInError: "origin changed",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			runGit(t, root, "init", "-q")
			expected := "https://github.com/getspas/private-files.git"
			runGit(t, root, "remote", "add", "origin", expected)
			test.configure(t, root, expected)
			repository := Repository{
				Path:      root,
				Git:       gitexec.Runner{},
				SafetyDir: filepath.Join(t.TempDir(), "safety"),
			}
			err := repository.VerifyOrigin(context.Background(), expected)
			if err == nil || !strings.Contains(err.Error(), test.wantInError) {
				t.Fatalf("VerifyOrigin() error = %v, want %q", err, test.wantInError)
			}
			if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsafeGitState {
				t.Fatalf("VerifyOrigin() error kind = %v, %t; want unsafe Git state", kind, ok)
			}
		})
	}
}

func TestVerifyOriginPreservesTrustedInheritedURLRewrite(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	expected := "https://github.com/acme/managed.git"
	runGit(t, root, "remote", "add", "origin", expected)

	globalConfig := filepath.Join(t.TempDir(), "gitconfig")
	redirectPrefix := "file:///trusted-mirror/"
	if _, err := (gitexec.Runner{}).Run(
		context.Background(),
		root,
		"config", "--file", globalConfig, "url."+redirectPrefix+".insteadOf", "https://github.com/",
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	repository := Repository{Path: root, Git: gitexec.Runner{}}
	if err := repository.VerifyOrigin(context.Background(), expected); err != nil {
		t.Fatalf("VerifyOrigin() rejected provider-produced raw origin with trusted rewrite: %v", err)
	}
	effective, err := repository.Git.Run(context.Background(), root, "remote", "get-url", "origin")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(effective.Stdout)); got != redirectPrefix+"acme/managed.git" {
		t.Fatalf("effective rewritten origin = %q, want trusted rewrite active", got)
	}
}

func TestStageTreatsSpecialCharactersAsLiteralPaths(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	var want []pathmodel.Path
	for _, name := range []string{"-private.env", "private,notes.env"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("A=1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		managedPath, err := pathmodel.Parse(name)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, managedPath)
	}
	repository := Repository{
		Path:      root,
		Git:       gitexec.Runner{},
		SafetyDir: filepath.Join(t.TempDir(), "safety"),
	}
	if err := repository.Stage(context.Background(), want); err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	paths, err := repository.TrackedPaths(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != len(want) {
		t.Fatalf("TrackedPaths() = %v, want %v", paths, want)
	}
	for index := range want {
		if paths[index] != want[index] {
			t.Fatalf("TrackedPaths() = %v, want %v", paths, want)
		}
	}
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

func hashBlob(t *testing.T, dir, content string) string {
	t.Helper()
	result, err := (gitexec.Runner{}).RunInput(
		context.Background(),
		dir,
		strings.NewReader(content),
		"hash-object",
		"-w",
		"--stdin",
	)
	if err != nil {
		t.Fatalf("hash Git blob: %v", err)
	}
	return strings.TrimSpace(string(result.Stdout))
}

func TestIsNonFastForwardUsesPorcelainPushStatus(t *testing.T) {
	t.Parallel()

	if !IsNonFastForward(&gitexec.ExitError{
		Operation: "push",
		ExitCode:  1,
		Stdout:    "!\trefs/heads/main:refs/heads/main\t[rejected] (non-fast-forward)",
	}) {
		t.Fatal("IsNonFastForward() = false for porcelain local rejection")
	}
	if IsNonFastForward(&gitexec.ExitError{
		Operation: "push",
		ExitCode:  1,
		Stdout:    "!\trefs/heads/main:refs/heads/main\t[remote rejected] (protected branch hook declined)",
	}) {
		t.Fatal("IsNonFastForward() = true for remote policy rejection")
	}
	if IsNonFastForward(&gitexec.ExitError{
		Operation: "push",
		ExitCode:  1,
		Stderr:    "error: failed to push some refs (non-fast-forward)",
	}) {
		t.Fatal("IsNonFastForward() = true for unstructured stderr diagnostic")
	}
}

func TestStageBypassesGitAttributeCleanFilters(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "config", "user.name", "SPAS Test")
	runGit(t, root, "config", "user.email", "spas@example.invalid")
	runGit(t, root, "commit", "--allow-empty", "-q", "-m", "initial")
	runGit(t, root, "config", "filter.spas-should-not-run.clean", "spas-command-that-does-not-exist")
	runGit(t, root, "config", "filter.spas-should-not-run.required", "true")
	if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("secret.txt filter=spas-should-not-run\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("raw private bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := Repository{
		Path:      root,
		Git:       gitexec.Runner{},
		SafetyDir: filepath.Join(t.TempDir(), "safety"),
	}
	if err := repository.EnsureSafety(ctx); err != nil {
		t.Fatal(err)
	}
	managedPath, err := pathmodel.Parse("secret.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Stage(ctx, []pathmodel.Path{managedPath}); err != nil {
		t.Fatalf("Stage() executed a clean filter: %v", err)
	}
	result, err := repository.Git.Run(ctx, root, repository.safeArgs("show", ":secret.txt")...)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(result.Stdout); got != "raw private bytes\n" {
		t.Fatalf("staged content = %q", got)
	}
}

func TestValidateManagedPathRejectsGitControlFiles(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		".gitattributes",
		"nested/.GITATTRIBUTES",
		".gitignore",
		"nested/.gitmodules",
		".spas-tmp/secret",
		".SPAS-TMP/secret",
	} {
		managedPath, err := pathmodel.Parse(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateManagedPath(managedPath); err == nil {
			t.Errorf("ValidateManagedPath(%q) error = nil", value)
		}
	}
}

func TestEnsureSafetyClearsPrivateInfoAttributes(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runGit(t, root, "init", "-q")
	attributes := filepath.Join(root, ".git", "info", "attributes")
	if err := os.WriteFile(attributes, []byte("* filter=unsafe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := Repository{
		Path:      root,
		Git:       gitexec.Runner{},
		SafetyDir: filepath.Join(t.TempDir(), "safety"),
	}
	if err := repository.EnsureSafety(context.Background()); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(attributes)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 0 {
		t.Fatalf("info/attributes was not cleared: %q", content)
	}
}

func TestStagePreservesExecutableModeWhenFileModeIsUnreliable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "config", "core.fileMode", "false")
	managedPath, err := pathmodel.Parse("script.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, managedPath.String()), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	blob := hashBlob(t, root, "old\n")
	runGit(t, root, "update-index", "--add", "--cacheinfo", "100755", blob, managedPath.String())
	if err := os.WriteFile(filepath.Join(root, managedPath.String()), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := Repository{Path: root, Git: gitexec.Runner{}, SafetyDir: filepath.Join(t.TempDir(), "safety")}
	if err := repository.Stage(ctx, []pathmodel.Path{managedPath}); err != nil {
		t.Fatal(err)
	}
	result, err := repository.Git.Run(ctx, root, "ls-files", "--stage", "--", managedPath.String())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(string(result.Stdout))[0]; got != "100755" {
		t.Fatalf("staged mode = %s, want 100755", got)
	}
}

func TestStageHonorsExecutableModeWhenFileModeIsReliable(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose a reliable executable bit")
	}
	ctx := context.Background()
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "config", "core.fileMode", "true")
	managedPath, err := pathmodel.Parse("script.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, managedPath.String()), []byte("old\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	blob := hashBlob(t, root, "old\n")
	runGit(t, root, "update-index", "--add", "--cacheinfo", "100755", blob, managedPath.String())
	if err := os.Chmod(filepath.Join(root, managedPath.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := Repository{Path: root, Git: gitexec.Runner{}, SafetyDir: filepath.Join(t.TempDir(), "safety")}
	if err := repository.Stage(ctx, []pathmodel.Path{managedPath}); err != nil {
		t.Fatal(err)
	}
	result, err := repository.Git.Run(ctx, root, "ls-files", "--stage", "--", managedPath.String())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(string(result.Stdout))[0]; got != "100644" {
		t.Fatalf("staged mode = %s, want 100644", got)
	}
}

func TestIsMergeResultOfRequiresRecordedFirstParent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "config", "user.name", "SPAS Test")
	runGit(t, root, "config", "user.email", "spas@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "base.txt")
	runGit(t, root, "commit", "-q", "-m", "base")
	runGit(t, root, "checkout", "-q", "-b", "side")
	if err := os.WriteFile(filepath.Join(root, "side.txt"), []byte("side\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "side.txt")
	runGit(t, root, "commit", "-q", "-m", "side")
	sideHead := strings.TrimSpace(gitOutput(t, root, "rev-parse", "HEAD"))
	runGit(t, root, "checkout", "-q", "main")
	if err := os.WriteFile(filepath.Join(root, "main.txt"), []byte("main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "main.txt")
	runGit(t, root, "commit", "-q", "-m", "main")
	preMerge := strings.TrimSpace(gitOutput(t, root, "rev-parse", "HEAD"))
	runGit(t, root, "merge", "-q", "--no-ff", "side", "-m", "merge")
	mergeCommit := strings.TrimSpace(gitOutput(t, root, "rev-parse", "HEAD"))
	mergeTree := strings.TrimSpace(gitOutput(t, root, "rev-parse", "HEAD^{tree}"))

	repository := Repository{Path: root, Git: gitexec.Runner{}, SafetyDir: filepath.Join(t.TempDir(), "safety")}
	valid, err := repository.IsMergeResultOf(ctx, mergeCommit, preMerge, sideHead, mergeTree)
	if err != nil {
		t.Fatal(err)
	}
	if !valid {
		t.Fatal("merge commit was not recognized as a result of its first parent")
	}
	valid, err = repository.IsMergeResultOf(ctx, preMerge, preMerge, sideHead, mergeTree)
	if err != nil {
		t.Fatal(err)
	}
	if valid {
		t.Fatal("ordinary commit was accepted as a merge result")
	}
	valid, err = repository.IsMergeResultOf(ctx, mergeCommit, preMerge, preMerge, mergeTree)
	if err != nil {
		t.Fatal(err)
	}
	if valid {
		t.Fatal("merge commit with the wrong second parent was accepted")
	}
	valid, err = repository.IsMergeResultOf(ctx, mergeCommit, preMerge, sideHead, strings.Repeat("0", len(mergeTree)))
	if err != nil {
		t.Fatal(err)
	}
	if valid {
		t.Fatal("merge commit with the wrong tree was accepted")
	}
	valid, err = repository.IsMergeResultOf(ctx, mergeCommit, preMerge, "", "")
	if err == nil || valid {
		t.Fatalf("empty merge binding = %t, %v; want rejection", valid, err)
	}
}

func TestIndexContainsDistinguishesStagedDeletion(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	managed, err := pathmodel.Parse("private.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managed.OSPath(root), []byte("private\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "private.txt")
	repository := Repository{Path: root, Git: gitexec.Runner{}, SafetyDir: filepath.Join(t.TempDir(), "safety")}
	contained, err := repository.IndexContains(ctx, managed)
	if err != nil || !contained {
		t.Fatalf("IndexContains() = %t, %v; want true", contained, err)
	}
	if err := repository.StageRemovals(ctx, []pathmodel.Path{managed}); err != nil {
		t.Fatal(err)
	}
	contained, err = repository.IndexContains(ctx, managed)
	if err != nil || contained {
		t.Fatalf("IndexContains() after removal = %t, %v; want false", contained, err)
	}
}

func TestEnsureSafetyRejectsRedirectedPrivateWorktree(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	privateRoot := filepath.Join(root, "private")
	redirectedRoot := filepath.Join(root, "redirected")
	if err := os.MkdirAll(redirectedRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "-q", privateRoot)
	runGit(t, privateRoot, "config", "--local", "core.worktree", redirectedRoot)

	repository := Repository{
		Path:      privateRoot,
		Git:       gitexec.Runner{},
		SafetyDir: filepath.Join(root, "safety"),
	}
	if err := repository.EnsureSafety(context.Background()); err == nil || !strings.Contains(err.Error(), "working tree was redirected") {
		t.Fatalf("EnsureSafety() error = %v, want redirected-worktree rejection", err)
	} else if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsafeGitState {
		t.Fatalf("EnsureSafety() error kind = %v, %t; want unsafe Git state", kind, ok)
	}
}

func TestCommitPreservesCommentPrefixedReason(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "config", "user.name", "SPAS Test")
	runGit(t, root, "config", "user.email", "spas@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "private.txt"), []byte("private\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "private.txt")

	repository := Repository{
		Path:      root,
		Git:       gitexec.Runner{},
		SafetyDir: filepath.Join(root, "safety"),
	}
	if err := repository.prepareSafetyFiles(); err != nil {
		t.Fatal(err)
	}
	const reason = "# private synchronization reason"
	if err := repository.Commit(ctx, reason); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(gitOutput(t, root, "log", "-1", "--format=%B")); got != reason {
		t.Fatalf("commit reason = %q, want %q", got, reason)
	}
}
