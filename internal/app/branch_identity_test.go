package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSyncPreservesPrivateBranchIdentity(t *testing.T) {
	t.Parallel()
	for _, branch := range []string{"main", "main\u00a0"} {
		t.Run(branch, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			publicRoot := initializePublicRepository(t, root)
			remote := initializePrivateRemoteWithFile(t, root, "secret.txt", "private content\n")
			if branch != "main" {
				runGit(t, remote, "branch", "-m", "main", branch)
			}
			instance, _ := testApp(t, publicRoot, root, remote)
			if err := instance.Link(context.Background(), LinkOptions{Repository: "getspas/private-files", Branch: branch}); err != nil {
				t.Fatalf("Link(%q): %v", branch, err)
			}
			options := syncOptions("sync assets")
			options.Branch = branch
			if err := instance.Sync(context.Background(), options); err != nil {
				t.Fatalf("initial sync: %v", err)
			}
			for _, tag := range []string{branch, "origin/" + branch} {
				runGit(t, remote, "-c", "tag.gpgsign=false", "tag", tag, "refs/heads/"+branch)
			}
			// Check the fetch and the following invocation, which starts with
			// the potentially ambiguous tags already present in the checkout.
			for i := 0; i < 2; i++ {
				if err := instance.Sync(context.Background(), options); err != nil {
					t.Fatalf("sync with same-named tags: %v", err)
				}
			}
			state := loadState(t, instance, publicRoot)
			if state.Private.Branch != branch {
				t.Fatalf("bound branch = %q, want %q", state.Private.Branch, branch)
			}
			runGit(t, state.Private.LocalRepositoryPath, "show-ref", "--verify", "refs/tags/"+branch)
			content, err := os.ReadFile(filepath.Join(publicRoot, "secret.txt"))
			if err != nil || string(content) != "private content\n" {
				t.Fatalf("asset content = %q, %v", content, err)
			}
		})
	}
}
