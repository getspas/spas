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

func createRemoteWithInitialCommit(t *testing.T, root, remote string) {
	t.Helper()
	source := filepath.Join(root, "remote-source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "--bare", "-q", remote)
	runGit(t, source, "init", "-q", "-b", "main")
	runGit(t, source, "config", "user.name", "Source Test")
	runGit(t, source, "config", "user.email", "source@example.invalid")
	if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "file.txt")
	runGit(t, source, "commit", "-q", "-m", "initial")
	runGit(t, source, "remote", "add", "origin", remote)
	runGit(t, source, "push", "-q", "origin", "main")
	runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
}

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

	// 2. Interactive prompt declined (user says 'n')
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

	// 3. Interactive prompt approved (user says 'y')
	instance.Prompt = interaction.Prompter{
		In:          strings.NewReader("y\n"),
		Out:         &out,
		Interactive: true,
	}
	err = instance.Link(ctx, LinkOptions{Repository: "getspas/public-assets", Branch: "main"})
	if err != nil {
		t.Fatalf("Link(public, approved) error = %v, want nil", err)
	}
	_, state, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Private.PublicApproved {
		t.Fatal("Link(public, approved) did not set PublicApproved = true")
	}

	// Unlink to test AllowPublic flag
	if err := instance.Unlink(ctx, UnlinkOptions{Force: true}); err != nil {
		t.Fatal(err)
	}

	// 4. Non-interactive with AllowPublic: true should succeed and persist approval
	instance.Prompt = interaction.Prompter{In: strings.NewReader(""), Out: &out, Interactive: false}
	err = instance.Link(ctx, LinkOptions{
		Repository:  "getspas/public-assets",
		Branch:      "main",
		AllowPublic: true,
	})
	if err != nil {
		t.Fatalf("Link(public, AllowPublic=true) error = %v, want nil", err)
	}
	_, state, err = instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Private.PublicApproved {
		t.Fatal("Link(public, AllowPublic=true) did not set PublicApproved = true")
	}
}

func TestSyncPublicRepositoryVerification(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	_, remote, instance := initializedApp(t, root)

	var probeCalls int
	instance.Provider = testRepositoryProvider{remoteURL: remote, isPublic: true, probeCalls: &probeCalls}

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
	if probeCalls != 1 {
		t.Fatalf("probeCalls = %d, want 1", probeCalls)
	}

	// 2. Interactive sync declined (user says 'n')
	var out bytes.Buffer
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
	if probeCalls != 2 {
		t.Fatalf("probeCalls = %d, want 2", probeCalls)
	}

	// 3. Interactive sync approved (user says 'y')
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
	if probeCalls != 3 {
		t.Fatalf("probeCalls = %d, want 3", probeCalls)
	}
	_, state, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Private.PublicApproved {
		t.Fatal("Sync(public, approved) did not persist PublicApproved = true")
	}

	// 4. Subsequent sync non-interactively without AllowPublic succeeds with 0 new probe calls
	instance.Prompt = interaction.Prompter{In: strings.NewReader(""), Out: instance.Out, Interactive: false}
	err = instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	})
	if err != nil {
		t.Fatalf("Sync(public, approved subsequent) error = %v, want nil", err)
	}
	if probeCalls != 3 {
		t.Fatalf("probeCalls after subsequent sync = %d, want 3 (0 additional probes)", probeCalls)
	}
}

func TestLinkPublicApprovalPersistsToSync(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot := initializePublicRepository(t, root)
	remote := filepath.Join(root, "remote.git")
	createRemoteWithInitialCommit(t, root, remote)
	var probeCalls int
	instance, _ := testApp(t, publicRoot, root, remote)
	instance.Provider = testRepositoryProvider{remoteURL: remote, isPublic: true, probeCalls: &probeCalls}

	// Link with AllowPublic: true
	err := instance.Link(ctx, LinkOptions{
		Repository:  "getspas/public-assets",
		Branch:      "main",
		AllowPublic: true,
	})
	if err != nil {
		t.Fatalf("Link(AllowPublic=true) error = %v", err)
	}
	if probeCalls != 0 {
		t.Fatalf("probeCalls during Link(AllowPublic=true) = %d, want 0", probeCalls)
	}

	// Sync non-interactively without AllowPublic flag — should succeed and perform zero probe calls
	err = instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	})
	if err != nil {
		t.Fatalf("Sync() after Link(AllowPublic=true) error = %v", err)
	}
	if probeCalls != 0 {
		t.Fatalf("probeCalls during Sync after Link(AllowPublic=true) = %d, want 0", probeCalls)
	}
}

func TestSyncPublicApprovalWithFlagPersists(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	_, remote, instance := initializedApp(t, root)

	var probeCalls int
	instance.Provider = testRepositoryProvider{remoteURL: remote, isPublic: true, probeCalls: &probeCalls}

	// Sync with AllowPublic: true in non-interactive mode
	instance.Prompt = interaction.Prompter{In: strings.NewReader(""), Out: instance.Out, Interactive: false}
	err := instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
		AllowPublic:     true,
	})
	if err != nil {
		t.Fatalf("Sync(AllowPublic=true) error = %v", err)
	}
	if probeCalls != 0 {
		t.Fatalf("probeCalls during Sync(AllowPublic=true) = %d, want 0", probeCalls)
	}

	// Second sync without AllowPublic flag — should succeed with 0 probe calls
	err = instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	})
	if err != nil {
		t.Fatalf("Second Sync() error = %v", err)
	}
	if probeCalls != 0 {
		t.Fatalf("probeCalls after second sync = %d, want 0", probeCalls)
	}
}

func TestPrivateRepositoryProbesEverySync(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot := initializePublicRepository(t, root)
	remote := filepath.Join(root, "remote.git")
	createRemoteWithInitialCommit(t, root, remote)
	var probeCalls int
	instance, _ := testApp(t, publicRoot, root, remote)
	// isPublic is false, but remoteURL is valid bare repo so git ls-remote succeeds
	instance.Provider = testRepositoryProvider{remoteURL: remote, isPublic: false, probeCalls: &probeCalls}

	if err := instance.Link(ctx, LinkOptions{
		Repository: "getspas/private-assets",
		Branch:     "main",
	}); err != nil {
		t.Fatalf("Link() error = %v", err)
	}
	if probeCalls != 1 {
		t.Fatalf("probeCalls after Link = %d, want 1", probeCalls)
	}

	// Sync 1
	if err := instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	}); err != nil {
		t.Fatalf("Sync 1 error = %v", err)
	}
	if probeCalls != 2 {
		t.Fatalf("probeCalls after Sync 1 = %d, want 2", probeCalls)
	}

	// Sync with DryRun — should NOT probe
	if err := instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
		DryRun:          true,
	}); err != nil {
		t.Fatalf("Sync DryRun error = %v", err)
	}
	if probeCalls != 2 {
		t.Fatalf("probeCalls after Sync DryRun = %d, want 2", probeCalls)
	}

	// Sync 2
	if err := instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeEnable,
	}); err != nil {
		t.Fatalf("Sync 2 error = %v", err)
	}
	if probeCalls != 3 {
		t.Fatalf("probeCalls after Sync 2 = %d, want 3", probeCalls)
	}
}

func TestLinkNetworkAccessReporting(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Case 1: normal Link without AllowPublic probes GitHub -> networkAccess: true
	{
		root := t.TempDir()
		publicRoot := initializePublicRepository(t, root)
		remote := filepath.Join(root, "remote.git")
		runGit(t, root, "init", "--bare", "-q", remote)
		instance, out := testApp(t, publicRoot, root, remote)
		instance.JSON = true
		instance.Provider = testRepositoryProvider{remoteURL: remote, isPublic: false}
		if err := instance.Link(ctx, LinkOptions{
			Repository: "getspas/private-assets",
			Branch:     "main",
		}); err != nil {
			t.Fatalf("Link() error = %v", err)
		}
		if !strings.Contains(out.String(), `"networkAccess":true`) && !strings.Contains(out.String(), `"networkAccess": true`) {
			t.Fatalf("Link output %s does not contain networkAccess: true", out.String())
		}
	}

	// Case 2: Link with AllowPublic skips probe -> networkAccess: false
	{
		root := t.TempDir()
		publicRoot := initializePublicRepository(t, root)
		remote := filepath.Join(root, "remote.git")
		runGit(t, root, "init", "--bare", "-q", remote)

		instance, out := testApp(t, publicRoot, root, remote)
		instance.JSON = true
		instance.Provider = testRepositoryProvider{remoteURL: remote, isPublic: false}
		if err := instance.Link(ctx, LinkOptions{
			Repository:  "getspas/private-assets",
			Branch:      "main",
			AllowPublic: true,
		}); err != nil {
			t.Fatalf("Link(AllowPublic=true) error = %v", err)
		}
		if !strings.Contains(out.String(), `"networkAccess":false`) && !strings.Contains(out.String(), `"networkAccess": false`) {
			t.Fatalf("Link(AllowPublic=true) output %s does not contain networkAccess: false", out.String())
		}
	}
	// Case 3: Link with DryRun skips probe -> networkAccess: false
	{
		root := t.TempDir()
		publicRoot := initializePublicRepository(t, root)
		remote := filepath.Join(root, "remote.git")
		runGit(t, root, "init", "--bare", "-q", remote)
		instance, out := testApp(t, publicRoot, root, remote)
		instance.JSON = true
		instance.Provider = testRepositoryProvider{remoteURL: remote, isPublic: false}
		if err := instance.Link(ctx, LinkOptions{
			Repository: "getspas/private-assets",
			Branch:     "main",
			DryRun:     true,
		}); err != nil {
			t.Fatalf("Link(DryRun=true) error = %v", err)
		}
		if !strings.Contains(out.String(), `"networkAccess":false`) && !strings.Contains(out.String(), `"networkAccess": false`) {
			t.Fatalf("Link(DryRun=true) output %s does not contain networkAccess: false", out.String())
		}
	}
}
