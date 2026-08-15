package publicgit

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getspas/spas/internal/gitexec"
	"github.com/getspas/spas/internal/pathmodel"
)

func TestMain(m *testing.M) {
	if os.Getenv("SPAS_PUBLICGIT_PROXY") != "" {
		os.Exit(runPublicGitProxy())
	}
	os.Exit(m.Run())
}

func runPublicGitProxy() int {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 1
	}
	if recordPath := os.Getenv("SPAS_PUBLICGIT_RECORD"); recordPath != "" {
		record := strings.Join(os.Args[1:], "\x00") + "\n--stdin--\n" + string(input)
		if err := os.WriteFile(recordPath, []byte(record), 0o600); err != nil {
			return 1
		}
	}
	command := exec.Command(os.Getenv("SPAS_PUBLICGIT_REAL_GIT"), os.Args[1:]...)
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

func TestDiscoverAndTrackedPaths(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runGit(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("spas\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "README.md")

	repository, err := Discover(context.Background(), gitexec.Runner{}, root)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if repository.GitDir == "" || repository.CommonDir == "" {
		t.Fatalf("Discover() directories = GitDir %q, CommonDir %q", repository.GitDir, repository.CommonDir)
	}
	gitInfo, err := os.Stat(repository.GitDir)
	if err != nil {
		t.Fatal(err)
	}
	commonInfo, err := os.Stat(repository.CommonDir)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(gitInfo, commonInfo) {
		t.Fatalf("main worktree GitDir %q and CommonDir %q are different", repository.GitDir, repository.CommonDir)
	}
	paths, err := repository.TrackedPaths(context.Background())
	if err != nil {
		t.Fatalf("TrackedPaths() error = %v", err)
	}
	if len(paths) != 1 || paths[0] != "README.md" {
		t.Fatalf("TrackedPaths() = %v", paths)
	}
}

func TestDiscoverLinkedWorktreeDiffersFromCommonDirectory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	container := t.TempDir()
	root := filepath.Join(container, "main")
	common := filepath.Join(container, "common.git")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, container, "init", "-q", "-b", "main", "--separate-git-dir", common, root)
	runGit(t, root, "config", "user.name", "SPAS Test")
	runGit(t, root, "config", "user.email", "spas@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "tracked.txt")
	runGit(t, root, "commit", "-q", "-m", "initial")
	second := filepath.Join(container, "second")
	runGit(t, root, "worktree", "add", "-q", "-b", "second", second)
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	runGit(t, second, "worktree", "prune")

	repository, err := Discover(ctx, gitexec.Runner{}, second)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := repository.WorktreeCount(ctx); err != nil || count < 1 {
		t.Fatalf("WorktreeCount() = %d, %v, want linked worktree records", count, err)
	}
	gitInfo, err := os.Stat(repository.GitDir)
	if err != nil {
		t.Fatal(err)
	}
	commonInfo, err := os.Stat(repository.CommonDir)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(gitInfo, commonInfo) {
		t.Fatalf("lone linked worktree GitDir %q unexpectedly equals CommonDir %q", repository.GitDir, repository.CommonDir)
	}
}

func TestValidateGitVersionEnforcesPatchLevelFloor(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"git version 2.43.0", "git version 2.42.9", "git version 1.99.9"} {
		if err := validateGitVersion(value); err == nil {
			t.Errorf("validateGitVersion(%q) error = nil, want floor rejection", value)
		}
	}
	for _, value := range []string{"git version 2.43.1", "git version 2.50.1 (Apple Git-155)", "git version 3.0.0"} {
		if err := validateGitVersion(value); err != nil {
			t.Errorf("validateGitVersion(%q) error = %v", value, err)
		}
	}
}

func TestInfoExcludeAndEffectiveCheck(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runGit(t, root, "init", "-q")
	repository, err := Discover(context.Background(), gitexec.Runner{}, root)
	if err != nil {
		t.Fatal(err)
	}
	exclude, err := repository.InfoExcludePath(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exclude, []byte("/.env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := pathmodelForTest(".env")
	if err != nil {
		t.Fatal(err)
	}
	ignored, err := repository.IsEffectivelyExcluded(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !ignored {
		t.Fatal(".env is not effectively excluded")
	}
}

func TestRepositoryStatusBranchCaseWorktreesAndRemoval(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "config", "user.name", "SPAS Test")
	runGit(t, root, "config", "user.email", "spas@example.invalid")
	runGit(t, root, "config", "--local", "core.ignoreCase", "true")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "tracked.txt")
	runGit(t, root, "commit", "-q", "-m", "initial")

	repository, err := Discover(ctx, gitexec.Runner{}, root)
	if err != nil {
		t.Fatal(err)
	}
	if head, err := repository.Head(ctx); err != nil || head == "" {
		t.Fatalf("Head() = %q, %v", head, err)
	}
	if branch, err := repository.Branch(ctx); err != nil || branch != "main" {
		t.Fatalf("Branch() = %q, %v", branch, err)
	}
	if value, present, err := repository.EffectiveIgnoreCase(ctx); err != nil || !present || !value {
		t.Fatalf("EffectiveIgnoreCase() = %t, %t, %v", value, present, err)
	}
	if _, err := repository.FilesystemIgnoresCase(); err != nil {
		t.Fatalf("FilesystemIgnoresCase() error = %v", err)
	}
	if count, err := repository.WorktreeCount(ctx); err != nil || count != 1 {
		t.Fatalf("WorktreeCount() = %d, %v", count, err)
	}

	path, err := pathmodel.Parse("tracked.txt")
	if err != nil {
		t.Fatal(err)
	}
	if status, err := repository.PathStatus(ctx, path); err != nil || len(status) != 0 {
		t.Fatalf("PathStatus(clean) = %q, %v", status, err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("modified\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status, err := repository.PathStatus(ctx, path); err != nil || len(status) == 0 {
		t.Fatalf("PathStatus(modified) = %q, %v", status, err)
	}
	runGit(t, root, "restore", "tracked.txt")

	second := filepath.Join(filepath.Dir(root), "second")
	runGit(t, root, "worktree", "add", "-q", "-b", "second", second)
	linked, err := Discover(ctx, gitexec.Runner{}, second)
	if err != nil {
		t.Fatal(err)
	}
	linkedGitInfo, err := os.Stat(linked.GitDir)
	if err != nil {
		t.Fatal(err)
	}
	linkedCommonInfo, err := os.Stat(linked.CommonDir)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(linkedGitInfo, linkedCommonInfo) {
		t.Fatalf("linked worktree GitDir %q unexpectedly equals CommonDir %q", linked.GitDir, linked.CommonDir)
	}
	if count, err := repository.WorktreeCount(ctx); err != nil || count != 2 {
		t.Fatalf("WorktreeCount(second) = %d, %v", count, err)
	}
	runGit(t, root, "worktree", "remove", "--force", second)

	if err := repository.RemoveTracked(ctx, []pathmodel.Path{path}); err != nil {
		t.Fatalf("RemoveTracked() error = %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "tracked.txt"))
	if err != nil {
		t.Fatalf("RemoveTracked() removed workspace file: %v", err)
	}
	if string(content) != "base\n" {
		t.Fatalf("workspace file after RemoveTracked() = %q, want preserved bytes", content)
	}
	if status, err := repository.PathStatus(ctx, path); err != nil || len(status) == 0 {
		t.Fatalf("PathStatus(removed) = %q, %v", status, err)
	}
}

func TestRemoveTrackedUsesStdinForLargePathsets(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	var paths []pathmodel.Path
	for index := range 2200 {
		name := fmt.Sprintf("entry-%04d-abcdefghijklmnop.txt", index)
		if err := os.WriteFile(filepath.Join(root, name), []byte("public\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		path, err := pathmodel.Parse(name)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	runGit(t, root, "add", "--all")

	repository, err := Discover(ctx, gitexec.Runner{}, root)
	if err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(root, "git-rm-record")
	t.Setenv("SPAS_PUBLICGIT_PROXY", "record")
	t.Setenv("SPAS_PUBLICGIT_REAL_GIT", realGit)
	t.Setenv("SPAS_PUBLICGIT_RECORD", recordPath)
	repository.Git.Path = os.Args[0]
	if err := repository.RemoveTracked(ctx, paths); err != nil {
		t.Fatalf("RemoveTracked() error = %v", err)
	}
	record, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(record), "--quiet") {
		t.Fatalf("git rm arguments = %q, want --quiet", record)
	}
	t.Setenv("SPAS_PUBLICGIT_PROXY", "")
	repository.Git.Path = ""
	tracked, err := repository.TrackedPaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracked) != 0 {
		t.Fatalf("TrackedPaths() = %d paths after removal, want none", len(tracked))
	}
}

func TestFilesystemCaseProbeUsesWorkspaceAndLeavesNoFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	commonDir := t.TempDir()
	repository := Repository{Root: root, CommonDir: commonDir}

	if _, err := repository.FilesystemIgnoresCase(); err != nil {
		t.Fatalf("FilesystemIgnoresCase() error = %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("workspace contains probe leftovers: %v", entries)
	}
	commonEntries, err := os.ReadDir(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(commonEntries) != 0 {
		t.Fatalf("Git directory was modified by workspace case probe: %v", commonEntries)
	}
}

func TestRepositoryReportsUnmergedPathsAndDetachedHead(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "config", "user.name", "SPAS Test")
	runGit(t, root, "config", "user.email", "spas@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "conflict.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "conflict.txt")
	runGit(t, root, "commit", "-q", "-m", "base")
	runGit(t, root, "switch", "-q", "-c", "other")
	if err := os.WriteFile(filepath.Join(root, "conflict.txt"), []byte("other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "commit", "-q", "-am", "other")
	runGit(t, root, "switch", "-q", "main")
	if err := os.WriteFile(filepath.Join(root, "conflict.txt"), []byte("main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "commit", "-q", "-am", "main")
	if _, err := (gitexec.Runner{}).Run(ctx, root, "merge", "other"); err == nil {
		t.Fatal("git merge unexpectedly succeeded")
	}

	repository, err := Discover(ctx, gitexec.Runner{}, root)
	if err != nil {
		t.Fatal(err)
	}
	unmerged, err := repository.UnmergedPaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(unmerged) != 1 || unmerged[0] != "conflict.txt" {
		t.Fatalf("UnmergedPaths() = %v", unmerged)
	}
	runGit(t, root, "merge", "--abort")
	head, err := repository.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "checkout", "-q", "--detach", head)
	if branch, err := repository.Branch(ctx); err != nil || branch != "" {
		t.Fatalf("Branch(detached) = %q, %v", branch, err)
	}
}

func TestHeadIsAbsentInUnbornRepository(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	repository, err := Discover(ctx, gitexec.Runner{}, root)
	if err != nil {
		t.Fatal(err)
	}
	if head, err := repository.Head(ctx); err != nil || head != "" {
		t.Fatalf("Head(unborn) = %q, %v", head, err)
	}
}

func TestHeadRejectsCorruptBranchReference(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	repository, err := Discover(ctx, gitexec.Runner{}, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repository.CommonDir, "refs", "heads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository.CommonDir, "refs", "heads", "main"), []byte("not-an-object\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Head(ctx); err == nil {
		t.Fatal("Head() error = nil, want corrupt-reference error")
	}
}

func TestHeadRejectsNonCommitRef(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	result, err := (gitexec.Runner{}).RunInput(ctx, root, strings.NewReader("not a commit"), "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "update-ref", "refs/not-a-commit", strings.TrimSpace(string(result.Stdout)))
	runGit(t, root, "symbolic-ref", "HEAD", "refs/not-a-commit")
	repository, err := Discover(ctx, gitexec.Runner{}, root)
	if err != nil {
		t.Fatal(err)
	}
	if head, err := repository.Head(ctx); err == nil || head != "" {
		t.Fatalf("Head() = %q, %v, want non-commit error", head, err)
	}
}

func TestIsEffectivelyExcludedDistinguishesMissingRule(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	repository, err := Discover(ctx, gitexec.Runner{}, root)
	if err != nil {
		t.Fatal(err)
	}
	path, err := pathmodel.Parse(".env")
	if err != nil {
		t.Fatal(err)
	}
	if excluded, err := repository.IsEffectivelyExcluded(ctx, path); err != nil || excluded {
		t.Fatalf("IsEffectivelyExcluded(no rule) = %t, %v", excluded, err)
	}
	excludePath, err := repository.InfoExcludePath(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(excludePath, []byte("/.env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if excluded, err := repository.IsEffectivelyExcluded(ctx, path); err != nil || !excluded {
		t.Fatalf("IsEffectivelyExcluded(rule) = %t, %v", excluded, err)
	}
}

func TestSwapASCIIcase(t *testing.T) {
	t.Parallel()

	if got := swapASCIIcase("AbC-123"); got != "aBc-123" {
		t.Fatalf("swapASCIIcase() = %q", got)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := (gitexec.Runner{}).Run(context.Background(), dir, args...); err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}
