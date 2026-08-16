package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getspas/spas/internal/interaction"
)

func TestLinkPublicRepositoryVerification(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot := filepath.Join(root, "public")
	runGit(t, root, "init", "-q", "-b", "main", publicRoot)
	runGit(t, publicRoot, "config", "user.name", "SPAS Test")
	runGit(t, publicRoot, "config", "user.email", "spas@example.invalid")
	if err := os.WriteFile(filepath.Join(publicRoot, "README.md"), []byte("public\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, publicRoot, "add", "README.md")
	runGit(t, publicRoot, "commit", "-q", "-m", "initial")

	remote := filepath.Join(root, "remote.git")
	runGit(t, root, "init", "--bare", "-q", remote)

	// 1. Non-interactive without AllowPublic should fail with ErrDecisionRequired
	instance, _ := testApp(t, publicRoot, root, remote)
	instance.Provider = testRepositoryProvider{remoteURL: remote, isPublic: true}
	err := instance.Link(ctx, LinkOptions{Repository: "getspas/public-assets", Branch: "main"})
	if !errors.Is(err, interaction.ErrDecisionRequired) {
		t.Fatalf("Link(public, non-interactive) error = %v, want ErrDecisionRequired", err)
	}

	// 2. Non-interactive with AllowPublic: true should succeed
	err = instance.Link(ctx, LinkOptions{
		Repository:  "getspas/public-assets",
		Branch:      "main",
		AllowPublic: true,
	})
	if err != nil {
		t.Fatalf("Link(public, AllowPublic=true) error = %v, want nil", err)
	}

	// Unlink to test interactive scenarios
	if err := instance.Unlink(ctx, UnlinkOptions{Force: true}); err != nil {
		t.Fatal(err)
	}

	// 3. Interactive prompt declined (user says 'n')
	var out bytes.Buffer
	instance.Prompt = interaction.Prompter{
		In:          strings.NewReader("n\n"),
		Out:         &out,
		Interactive: true,
	}
	err = instance.Link(ctx, LinkOptions{Repository: "getspas/public-assets", Branch: "main"})
	if err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("Link(public, declined) error = %v, want declined error", err)
	}

	// 4. Interactive prompt approved (user says 'y')
	instance.Prompt = interaction.Prompter{
		In:          strings.NewReader("y\n"),
		Out:         &out,
		Interactive: true,
	}
	err = instance.Link(ctx, LinkOptions{Repository: "getspas/public-assets", Branch: "main"})
	if err != nil {
		t.Fatalf("Link(public, approved) error = %v, want nil", err)
	}
}

func TestSyncPublicRepositoryVerification(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	_, remote, instance := initializedApp(t, root)

	// Make the provider report that the repo is publicly readable
	instance.Provider = testRepositoryProvider{remoteURL: remote, isPublic: true}

	// 1. Non-interactive sync without AllowPublic fails with ErrDecisionRequired
	instance.Prompt = interaction.Prompter{In: strings.NewReader(""), Out: instance.Out, Interactive: false}
	err := instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	})
	if !errors.Is(err, interaction.ErrDecisionRequired) {
		t.Fatalf("Sync(public, non-interactive) error = %v, want ErrDecisionRequired", err)
	}

	// 2. Non-interactive sync with AllowPublic: true succeeds
	err = instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
		AllowPublic:     true,
	})
	if err != nil {
		t.Fatalf("Sync(public, AllowPublic=true) error = %v, want nil", err)
	}

	// 3. Interactive sync approved (user says 'y')
	var out bytes.Buffer
	instance.Prompt = interaction.Prompter{
		In:          strings.NewReader("y\n"),
		Out:         &out,
		Interactive: true,
	}
	err = instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	})
	if err != nil {
		t.Fatalf("Sync(public, interactive approved) error = %v, want nil", err)
	}

	// 4. Interactive sync declined (user says 'n')
	instance.Prompt = interaction.Prompter{
		In:          strings.NewReader("n\n"),
		Out:         &out,
		Interactive: true,
	}
	err = instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	})
	if err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("Sync(public, interactive declined) error = %v, want declined error", err)
	}
}
