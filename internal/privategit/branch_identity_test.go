package privategit

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/getspas/spas/internal/gitexec"
)

func TestClonePreservesBranchIdentity(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, branch, tag, requested string
	}{
		{name: "ordinary default", branch: "main"},
		{name: "requested with matching tag", branch: "main", tag: "main", requested: "main"},
		{name: "default with matching tag", branch: "main", tag: "main"},
		{name: "default with remote-like tag", branch: "main", tag: "origin/main"},
		{name: "Unicode default", branch: "main\u00a0"},
		{name: "Unicode requested", branch: "main\u00a0", requested: "main\u00a0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			source, remote := createBranchRemote(t, root, test.branch)
			if test.tag != "" {
				runGit(t, source, "-c", "tag.gpgsign=false", "tag", test.tag)
				runGit(t, source, "push", "-q", "origin", "--tags")
			}
			repository := Repository{Path: filepath.Join(root, "clone"), SafetyDir: filepath.Join(root, "safety")}
			result := publishCloneForTest(t, repository, context.Background(), remote, test.requested)
			if result.Branch != test.branch || result.Empty {
				t.Fatalf("clone result = %+v, want branch %q", result, test.branch)
			}
			if branch, err := repository.Branch(context.Background()); err != nil || branch != test.branch {
				t.Fatalf("Branch() = %q, %v, want %q", branch, err, test.branch)
			}
			if err := repository.ValidateBranch(context.Background(), test.branch); err != nil {
				t.Fatalf("literal branch rejected: %v", err)
			}
		})
	}
}

func TestFetchedTagPreservesPrivateBranchIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source, remote := createBranchRemote(t, root, "main")
	repository := Repository{Path: filepath.Join(root, "clone"), SafetyDir: filepath.Join(root, "safety")}
	result := publishCloneForTest(t, repository, context.Background(), remote, "main")
	runGit(t, source, "-c", "tag.gpgsign=false", "tag", "main")
	runGit(t, source, "push", "-q", "origin", "--tags")
	if err := repository.Fetch(context.Background(), "main"); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository.Path, "show-ref", "--verify", "refs/tags/main")
	if err := repository.verifyPreparedResult(context.Background(), result); err != nil {
		t.Fatalf("fetched tag changed the bound branch: %v", err)
	}
}

func TestPrivateUnbornUnicodeBranchRemainsUnborn(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source, _ := createBranchRemote(t, root, "main")
	runGit(t, source, "symbolic-ref", "HEAD", "refs/heads/main\u00a0")
	repository := Repository{Path: source, Git: gitexec.Runner{}}
	if head, err := repository.Head(context.Background()); err != nil || head != "" {
		t.Fatalf("Head() = %q, %v, want unborn branch", head, err)
	}
	if branch, err := repository.Branch(context.Background()); err != nil || branch != "main\u00a0" {
		t.Fatalf("Branch() = %q, %v", branch, err)
	}
}

func createBranchRemote(t *testing.T, root, branch string) (string, string) {
	t.Helper()
	source := filepath.Join(root, "source")
	remote := filepath.Join(root, "remote.git")
	runGit(t, root, "init", "--bare", "-q", remote)
	runGit(t, root, "init", "-q", "-b", branch, source)
	runGit(t, source, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "initial")
	runGit(t, source, "remote", "add", "origin", remote)
	runGit(t, source, "push", "-q", "origin", "HEAD:refs/heads/"+branch)
	runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/"+branch)
	return source, remote
}
