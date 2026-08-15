package app

// Regression tests for the defects found during the pre-release review.
// Each test names the finding it locks in.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/getspas/spas/internal/filesync"
	"github.com/getspas/spas/internal/gitexec"
	"github.com/getspas/spas/internal/interaction"
	"github.com/getspas/spas/internal/linkstate"
	"github.com/getspas/spas/internal/pathmodel"
	"github.com/getspas/spas/internal/spaserr"
)

// fixture creates a public repository with one committed public file, a bare
// private remote, and a linked App.
func fixture(t *testing.T) (App, string, string, string) {
	t.Helper()
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
	if err := instance.Link(context.Background(), LinkOptions{
		Repository: "getspas/private-files",
		Branch:     "main",
	}); err != nil {
		t.Fatalf("Link() error = %v", err)
	}
	return instance, publicRoot, root, remote
}

func syncOptions(message string) SyncOptions {
	return SyncOptions{
		Message:         message,
		Conflict:        ConflictAbort,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
		Branch:          "main",
	}
}

// enroll adds and fully synchronizes the named files with the given contents.
func enroll(t *testing.T, instance App, publicRoot string, files map[string]string) {
	t.Helper()
	ctx := context.Background()
	paths := make([]string, 0, len(files))
	for name, content := range files {
		full := filepath.Join(publicRoot, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, name)
	}
	if err := instance.Add(ctx, AddOptions{
		Paths:           paths,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	}); err != nil {
		t.Fatalf("Add(%v) error = %v", paths, err)
	}
	if err := instance.Sync(ctx, syncOptions("enroll fixture files")); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
}

// teammatePush commits files to the private remote from an independent clone.
func teammatePush(t *testing.T, root, remote string, files map[string]string, remove []string) {
	t.Helper()
	clone := filepath.Join(root, "teammate-clone")
	_ = os.RemoveAll(clone)
	runGit(t, root, "clone", "-q", remote, clone)
	runGit(t, clone, "config", "user.name", "Teammate")
	runGit(t, clone, "config", "user.email", "teammate@example.invalid")
	if remoteHead := gitOutputAllowFail(t, clone, "rev-parse", "--verify", "-q", "refs/remotes/origin/main"); remoteHead != "" {
		runGit(t, clone, "checkout", "-q", "-B", "main", "origin/main")
	} else if head := gitOutputAllowFail(t, clone, "rev-parse", "--verify", "-q", "HEAD"); head == "" {
		runGit(t, clone, "checkout", "-q", "-b", "main")
	}
	for name, content := range files {
		full := filepath.Join(clone, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, clone, "add", "--force", "--", name)
	}
	for _, name := range remove {
		runGit(t, clone, "rm", "-q", "--", name)
	}
	runGit(t, clone, "commit", "-q", "-m", "teammate change")
	runGit(t, clone, "push", "-q", "origin", "HEAD:main")
}

func gitOutputAllowFail(t *testing.T, dir string, args ...string) string {
	t.Helper()
	result, _ := (gitexec.Runner{}).Run(context.Background(), dir, args...)
	return strings.TrimSpace(string(result.Stdout))
}

func loadState(t *testing.T, instance App, publicRoot string) linkstate.State {
	t.Helper()
	repository, err := instance.publicRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state, err := instance.Store.Load(repository.Root, repository.CommonDir)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func saveState(t *testing.T, instance App, state linkstate.State) {
	t.Helper()
	if err := instance.Store.Save(state); err != nil {
		t.Fatal(err)
	}
}

func requireSymlinkSupport(t *testing.T, directory string) {
	t.Helper()
	probe := filepath.Join(directory, ".spas-symlink-probe")
	if err := os.Symlink("target", probe); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	if err := os.Remove(probe); err != nil {
		t.Fatal(err)
	}
}

func validTestActiveMerge() *linkstate.ActiveMerge {
	return &linkstate.ActiveMerge{
		ConflictFilesReady:       true,
		WorkspaceSnapshots:       []linkstate.WorkspaceSnapshot{{Path: "conflict.txt"}},
		MaterializationPaths:     []string{"conflict.txt"},
		MaterializationSnapshots: []linkstate.WorkspaceSnapshot{{Path: "conflict.txt"}},
		PreMergeHead:             strings.Repeat("a", 40),
		MergeHead:                strings.Repeat("b", 40),
		ConflictPaths:            []string{"conflict.txt"},
	}
}

func TestSyncRejectsUnexpectedCleanPrivateHead(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	_, remote, instance := initializedApp(t, root)
	state := loadState(t, instance, filepath.Join(root, "public"))
	private := instance.privateRepository(state)
	unexpected := filepath.Join(private.Path, "unexpected.txt")
	if err := os.WriteFile(unexpected, []byte("manual commit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, private.Path, "add", "--", "unexpected.txt")
	runGit(t, private.Path, "commit", "-q", "-m", "manual private commit")
	remoteBefore := gitOutput(t, root, "--git-dir="+remote, "rev-parse", "refs/heads/main")

	err := instance.Sync(ctx, syncOptions("must not adopt manual commit"))
	kind, ok := spaserr.KindOf(err)
	if !ok || kind != spaserr.KindUnsafeGitState || !strings.Contains(err.Error(), "managed private HEAD mismatch") {
		t.Fatalf("Sync() error = %v, kind = %v, want expected-head unsafe-state error", err, kind)
	}
	remoteAfter := gitOutput(t, root, "--git-dir="+remote, "rev-parse", "refs/heads/main")
	if remoteAfter != remoteBefore {
		t.Fatalf("remote HEAD changed from %s to %s", remoteBefore, remoteAfter)
	}
}

func TestSyncReleasesLinkLockWhenManagedTempCleanupFails(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, _, instance := initializedApp(t, root)
	tempRoot := filepath.Join(publicRoot, filesync.ManagedTempDirectory)
	if err := os.MkdirAll(tempRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	unexpected := filepath.Join(tempRoot, "user-entry")
	if err := os.WriteFile(unexpected, []byte("do not remove"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := instance.Sync(ctx, syncOptions("")); err == nil || !strings.Contains(err.Error(), "unsupported entry") {
		t.Fatalf("first Sync() error = %v, want managed-temp cleanup failure", err)
	}
	if err := os.Remove(unexpected); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(ctx, syncOptions("")); err != nil {
		t.Fatalf("second Sync() error = %v, want released link lock", err)
	}
}

func TestManagedPrivateRepositoryDisablesLazyFetchWithoutChangingPublicRunner(t *testing.T) {
	t.Parallel()

	instance := App{Git: gitexec.Runner{}}
	private := instance.privateRepository(linkstate.State{})
	if !private.Git.NoLazyFetch {
		t.Fatal("managed private repository allows Git lazy fetch")
	}
	if instance.Git.NoLazyFetch {
		t.Fatal("constructing a private repository changed the public Git runner")
	}
}

func TestUnlinkInitializationRecoveryRequiresForceAndCleansBothClonePaths(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		phase      string
		staging    bool
		final      bool
		branchHead string
	}{
		{name: "preparing", phase: linkstate.ClonePreparing, staging: true},
		{name: "prepared-before-publication", phase: linkstate.ClonePrepared, staging: true, branchHead: strings.Repeat("a", 40)},
		{name: "prepared-after-publication", phase: linkstate.ClonePrepared, final: true, branchHead: strings.Repeat("a", 40)},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			instance, publicRoot, _, _ := fixture(t)
			state := loadState(t, instance, publicRoot)
			state.Private.Initialization = &linkstate.CloneInitialization{
				Phase:           test.phase,
				RequestedBranch: "main",
			}
			if test.phase == linkstate.ClonePrepared {
				state.Private.Initialization.Branch = "main"
				state.Private.Initialization.Head = test.branchHead
			}
			if err := instance.Store.Save(state); err != nil {
				t.Fatal(err)
			}
			private := instance.privateRepository(state)
			if test.staging {
				if err := os.MkdirAll(private.StagingPath(), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if test.final {
				if err := os.MkdirAll(private.Path, 0o700); err != nil {
					t.Fatal(err)
				}
			}

			err := instance.Unlink(ctx, UnlinkOptions{RemovePrivateClone: true})
			if err == nil || !strings.Contains(err.Error(), "initialization") {
				t.Fatalf("Unlink() error = %v, want force-required initialization recovery rejection", err)
			}
			if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsafeGitState {
				t.Fatalf("Unlink() error = %v, kind = %v, want KindUnsafeGitState", err, kind)
			}

			if err := instance.Unlink(ctx, UnlinkOptions{Force: true, RemovePrivateClone: true}); err != nil {
				t.Fatalf("Unlink(force) error = %v", err)
			}
			if _, err := instance.Store.Load(state.Public.Root, state.Public.GitCommonDir); !errors.Is(err, linkstate.ErrNotLinked) {
				t.Fatalf("link state after forced unlink = %v, want ErrNotLinked", err)
			}
			if _, err := os.Lstat(private.Path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("final clone after forced unlink = %v, want absent", err)
			}
			if _, err := os.Lstat(private.StagingPath()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("staging clone after forced unlink = %v, want absent", err)
			}
		})
	}
}

func TestStatusReportsCloneInitializationRecovery(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, _, _ := fixture(t)
	state := loadState(t, instance, publicRoot)
	state.Private.Initialization = &linkstate.CloneInitialization{
		Phase:           linkstate.ClonePreparing,
		RequestedBranch: "main",
	}
	saveState(t, instance, state)

	var output bytes.Buffer
	instance.Out = &output
	instance.JSON = true
	if err := instance.Status(ctx, StatusOptions{}); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	var status Status
	if err := json.Unmarshal(output.Bytes(), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !status.PendingRecovery {
		t.Fatalf("Status() pendingRecovery = false, want initialization recovery")
	}
}

func TestUnlinkRequiresForceForPendingOwnershipBeforeFirstSync(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		state func(*linkstate.State)
	}{
		{
			name: "pending addition",
			state: func(state *linkstate.State) {
				state.PendingAdds = []string{".env"}
			},
		},
		{
			name: "pending removal",
			state: func(state *linkstate.State) {
				state.PendingRemoves = []linkstate.PendingRemoval{{Path: ".env", Existed: false}}
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			instance, publicRoot, _, _ := fixture(t)
			state := loadState(t, instance, publicRoot)
			test.state(&state)
			saveState(t, instance, state)

			err := instance.Unlink(ctx, UnlinkOptions{})
			if err == nil || !strings.Contains(err.Error(), "private changes are pending") {
				t.Fatalf("Unlink() error = %v, want pending-change rejection", err)
			}
			if err := instance.Unlink(ctx, UnlinkOptions{Force: true}); err != nil {
				t.Fatalf("Unlink(force) error = %v", err)
			}
		})
	}
}

func TestStatusReportsActiveMergeRecovery(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, _, instance := initializedApp(t, root)
	state := loadState(t, instance, publicRoot)
	state.ActiveMerge = validTestActiveMerge()
	state.Private.ExpectedHead = state.ActiveMerge.PreMergeHead
	saveState(t, instance, state)

	var output bytes.Buffer
	instance.Out = &output
	instance.JSON = true
	if err := instance.Status(ctx, StatusOptions{}); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	var status Status
	if err := json.Unmarshal(output.Bytes(), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !status.PendingRecovery {
		t.Fatalf("Status() pendingRecovery = false, want true")
	}
}

func testMaterialization(
	t *testing.T,
	publicRoot string,
	phase, resultHead string,
	previous, final, excluded, overrides, recoveryPaths []string,
) *linkstate.Materialization {
	t.Helper()
	overrideMap := make(map[string]pathmodel.Path, len(overrides))
	for _, value := range overrides {
		path, err := pathmodel.Parse(value)
		if err != nil {
			t.Fatal(err)
		}
		overrideMap[value] = path
	}
	candidates := unionPaths(
		materializationCandidates(stringsToPaths(final), previous, nil),
		stringsToPaths(recoveryPaths),
	)
	snapshots, err := snapshotPaths(publicRoot, candidates)
	if err != nil {
		t.Fatal(err)
	}
	materialization, err := newMaterialization(
		resultHead,
		previous,
		stringsToPaths(final),
		stringsToPaths(excluded),
		nil,
		nil,
		overrideMap,
		stringsToPaths(recoveryPaths),
		snapshots,
		nil,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	materialization.Phase = phase
	return materialization
}

func readExcludeBlock(t *testing.T, publicRoot string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(publicRoot, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestAddReportsOnlyNewlyEnrolledPaths(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, _, _ := fixture(t)
	privatePath := filepath.Join(publicRoot, ".env")
	if err := os.WriteFile(privatePath, []byte("TOKEN=private\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	instance.Out = &output
	instance.Err = &output
	instance.JSON = true
	if err := instance.Add(ctx, AddOptions{
		Paths:           []string{"README.md", ".env"},
		SkipTracked:     true,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	var result struct {
		Added               []string `json:"added"`
		SkippedTrackedPaths []string `json:"skippedTrackedPaths"`
		PendingSync         bool     `json:"pendingSync"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("decode Add output: %v\n%s", err, output.String())
	}
	if strings.Join(result.Added, ",") != ".env" {
		t.Fatalf("added = %v, want only .env", result.Added)
	}
	if strings.Join(result.SkippedTrackedPaths, ",") != "README.md" {
		t.Fatalf("skippedTrackedPaths = %v, want README.md", result.SkippedTrackedPaths)
	}
	if !result.PendingSync {
		t.Fatal("pendingSync = false, want true")
	}
}

// F1: an ownership override without a private replacement must refuse rather
// than delete the only copy of the file.
func TestOverrideRefusesWithoutPrivateReplacement(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, _, _ := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{".env": "TOKEN=secret\n"})

	// A pending addition that public Git then starts tracking.
	notes := filepath.Join(publicRoot, "notes.md")
	if err := os.WriteFile(notes, []byte("my only copy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Add(ctx, AddOptions{
		Paths:           []string{"notes.md"},
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	runGit(t, publicRoot, "add", "--force", "notes.md")
	runGit(t, publicRoot, "commit", "-q", "-m", "public takes notes.md")

	options := syncOptions("must not run")
	options.Conflict = ConflictOverride
	err := instance.Sync(ctx, options)
	if err == nil || !strings.Contains(err.Error(), "no replacement") {
		t.Fatalf("Sync(override) error = %v, want refusal naming the missing replacement", err)
	}
	if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindPathConflict {
		t.Fatalf("Sync(override) error kind = %v/%v, want path conflict", kind, ok)
	}
	content, err := os.ReadFile(notes)
	if err != nil || string(content) != "my only copy\n" {
		t.Fatalf("notes.md = %q, %v; want the original bytes intact", content, err)
	}
	if got := gitOutput(t, publicRoot, "status", "--porcelain"); got != "" {
		t.Fatalf("public status = %q, want clean", got)
	}
	state := loadState(t, instance, publicRoot)
	if len(state.PendingAdds) != 1 || state.PendingAdds[0] != "notes.md" {
		t.Fatalf("PendingAdds = %v, want the enrollment retained", state.PendingAdds)
	}
}

// F2a: sync --abort must rebuild the exclude block from the paths it actually
// materializes, never from stale link state.
func TestAbortKeepsEveryMaterializedPathExcluded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{
		".env":            "TOKEN=env\n",
		"secrets/api.key": "TOPSECRET\n",
	})

	// Simulate stale link state from an earlier interrupted operation.
	state := loadState(t, instance, publicRoot)
	state.ManagedPaths = []string{".env"}
	saveState(t, instance, state)

	// Create a private merge conflict on .env.
	teammatePush(t, root, remote, map[string]string{".env": "TOKEN=remote\n"}, nil)
	if err := os.WriteFile(filepath.Join(publicRoot, ".env"), []byte("TOKEN=local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := instance.Sync(ctx, syncOptions("conflicting local edit"))
	if !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want private merge conflict", err)
	}

	options := syncOptions("")
	options.Abort = true
	if err := instance.Sync(ctx, options); err != nil {
		t.Fatalf("Sync(--abort) error = %v", err)
	}

	block := readExcludeBlock(t, publicRoot)
	if !strings.Contains(block, "/secrets/api.key") || !strings.Contains(block, "/.env") {
		t.Fatalf("exclude block after abort = %q, want both managed paths", block)
	}
	runGit(t, publicRoot, "add", "-A")
	staged := gitOutput(t, publicRoot, "diff", "--cached", "--name-only")
	if strings.Contains(staged, "secrets/api.key") || strings.Contains(staged, ".env") {
		t.Fatalf("git add -A staged private content after abort: %q", staged)
	}
}

// F3: an edit made after `spas remove` must defer the removal, not be
// destroyed by it.
func TestRemoveThenEditDefersTheRemoval(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, _, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{".env": "TOKEN=original\n"})

	if err := instance.Remove(ctx, RemoveOptions{Paths: []string{".env"}}); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	// The developer changes their mind and edits the file.
	if err := os.WriteFile(filepath.Join(publicRoot, ".env"), []byte("TOKEN=brand-new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(ctx, syncOptions("attempt removal")); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(publicRoot, ".env"))
	if err != nil || string(content) != "TOKEN=brand-new\n" {
		t.Fatalf(".env = %q, %v; want the edit preserved", content, err)
	}
	if got := gitOutputAllowFail(t, publicRoot, "--git-dir="+remote, "show", "main:.env"); got == "" {
		t.Fatalf("private remote no longer has .env; the deferred removal was executed")
	}
	state := loadState(t, instance, publicRoot)
	if len(state.PendingRemoves) != 1 || state.PendingRemoves[0].Path != ".env" {
		t.Fatalf("PendingRemoves = %+v, want the deferred removal retained", state.PendingRemoves)
	}
}

func TestRemoveThenExecutableModeChangeDefersTheRemoval(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("executable bits are not represented on Windows")
	}

	ctx := context.Background()
	instance, publicRoot, _, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{"script.sh": "#!/bin/sh\necho private\n"})

	if err := instance.Remove(ctx, RemoveOptions{Paths: []string{"script.sh"}}); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if err := os.Chmod(filepath.Join(publicRoot, "script.sh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(ctx, syncOptions("")); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	info, err := os.Stat(filepath.Join(publicRoot, "script.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("script.sh mode = %v, want executable edit preserved", info.Mode())
	}
	if got := gitOutputAllowFail(t, publicRoot, "--git-dir="+remote, "show", "main:script.sh"); got == "" {
		t.Fatal("private remote no longer contains script.sh")
	}
	state := loadState(t, instance, publicRoot)
	if len(state.PendingRemoves) != 1 || state.PendingRemoves[0].Path != "script.sh" {
		t.Fatalf("PendingRemoves = %+v, want deferred removal retained", state.PendingRemoves)
	}
}

// F4: both override forms must save a recovery copy of what they discard.
func TestOverrideSavesRecoveryCopies(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{".env": "TOKEN=env\n"})

	// Untracked obstruction: a teammate adds a path the developer already has
	// as an unrelated, never-committed local file.
	teammatePush(t, root, remote, map[string]string{"data/report.csv": "private,rows\n"}, nil)
	obstruction := filepath.Join(publicRoot, "data", "report.csv")
	if err := os.MkdirAll(filepath.Dir(obstruction), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(obstruction, []byte("MY-ONLY-COPY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := syncOptions("")
	options.Conflict = ConflictOverride
	if err := instance.Sync(ctx, options); err != nil {
		t.Fatalf("Sync(override obstruction) error = %v", err)
	}
	if content, err := os.ReadFile(obstruction); err != nil || string(content) != "private,rows\n" {
		t.Fatalf("data/report.csv = %q, %v; want the private version", content, err)
	}
	if !findRecoveryCopy(t, filepath.Join(root, "data"), "MY-ONLY-COPY\n") {
		t.Fatal("no recovery copy of the obstructed file was saved")
	}

	// Tracked-path override with discarded public modifications.
	teammatePush(t, root, remote, map[string]string{"notes.md": "private notes\n"}, nil)
	notes := filepath.Join(publicRoot, "notes.md")
	if err := os.WriteFile(notes, []byte("public committed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, publicRoot, "add", "notes.md")
	runGit(t, publicRoot, "commit", "-q", "-m", "public notes")
	if err := os.WriteFile(notes, []byte("uncommitted local delta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	options = syncOptions("")
	options.Conflict = ConflictOverride
	options.DiscardPublicChanges = true
	if err := instance.Sync(ctx, options); err != nil {
		t.Fatalf("Sync(override tracked) error = %v", err)
	}
	if content, err := os.ReadFile(notes); err != nil || string(content) != "private notes\n" {
		t.Fatalf("notes.md = %q, %v; want the private version", content, err)
	}
	if !findRecoveryCopy(t, filepath.Join(root, "data"), "uncommitted local delta\n") {
		t.Fatal("no recovery copy of the discarded public modification was saved")
	}
	// The staged public deletion exists but no public commit was created.
	staged := gitOutput(t, publicRoot, "diff", "--cached", "--name-status")
	if !strings.Contains(staged, "notes.md") {
		t.Fatalf("staged public changes = %q, want the staged deletion", staged)
	}
}

func findRecoveryCopy(t *testing.T, dataDir, content string) bool {
	t.Helper()
	found := false
	_ = filepath.WalkDir(filepath.Join(dataDir, "recovery"), func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr == nil && string(data) == content {
			found = true
		}
		return nil
	})
	return found
}

// F7: a pending addition whose file is temporarily missing keeps its
// enrollment and its exclusion entry.
func TestMissingPendingAddKeepsEnrollmentAndExclusion(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, _, _ := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{"keep.txt": "keep\n"})

	secret := filepath.Join(publicRoot, "config", "secret.json")
	if err := os.MkdirAll(filepath.Dir(secret), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("{\"k\":\"v\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Add(ctx, AddOptions{
		Paths:           []string{"config/secret.json"},
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	// `git clean` (or any cleanup) removes it precisely because it is
	// excluded.
	if err := os.Remove(secret); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(ctx, syncOptions("")); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	block := readExcludeBlock(t, publicRoot)
	if !strings.Contains(block, "/config/secret.json") {
		t.Fatalf("exclude block = %q, want the deferred enrollment retained", block)
	}
	state := loadState(t, instance, publicRoot)
	if len(state.PendingAdds) != 1 || state.PendingAdds[0] != "config/secret.json" {
		t.Fatalf("PendingAdds = %v, want the deferred enrollment retained", state.PendingAdds)
	}
}

// F5: the executable bit survives the round trip through the private clone.
func TestExecutableBitSurvivesRoundTrip(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("executable bits are not represented on Windows")
	}

	ctx := context.Background()
	instance, publicRoot, _, remote := fixture(t)
	script := filepath.Join(publicRoot, "deploy.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho deploy\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := instance.Add(ctx, AddOptions{
		Paths:           []string{"deploy.sh"},
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if err := instance.Sync(ctx, syncOptions("add deploy script")); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	info, err := os.Stat(script)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("deploy.sh mode = %v, want the executable bit preserved", info.Mode())
	}
	tree := gitOutput(t, publicRoot, "--git-dir="+remote, "ls-tree", "main", "--", "deploy.sh")
	if !strings.Contains(tree, "100755") {
		t.Fatalf("private tree entry = %q, want mode 100755", tree)
	}
}

// F6: a sync interrupted between push and materialization must finish
// materializing before workspace state is read as local edits, so a
// teammate's pushed change is never silently reverted.
func TestInterruptedMaterializationResumesBeforeCommitting(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{
		"a.txt": "v1 a\n",
		"b.txt": "v1 b\n",
	})
	teammatePush(t, root, remote, map[string]string{
		"a.txt": "v2 a\n",
		"b.txt": "v2 b\n",
	}, nil)

	// Simulate the crash window: the private result was pushed but only half
	// the workspace was materialized.
	state := loadState(t, instance, publicRoot)
	private := instance.privateRepository(state)
	if err := private.Fetch(ctx, state.Private.Branch); err != nil {
		t.Fatal(err)
	}
	if _, err := private.MergeRemote(ctx, state.Private.Branch); err != nil {
		t.Fatal(err)
	}
	head, err := private.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state.Materializing = testMaterialization(
		t,
		publicRoot,
		linkstate.MaterializationPushed,
		head,
		[]string{"a.txt", "b.txt"},
		[]string{"a.txt", "b.txt"},
		[]string{"a.txt", "b.txt"},
		nil,
		nil,
	)
	saveState(t, instance, state)
	aPath, err := pathmodel.Parse("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := filesync.CopyManaged(private.Path, aPath, publicRoot, aPath); err != nil {
		t.Fatal(err)
	}

	if err := instance.Sync(ctx, syncOptions("")); err != nil {
		t.Fatalf("resume Sync() error = %v", err)
	}
	if got := gitOutput(t, publicRoot, "--git-dir="+remote, "show", "main:b.txt"); got != "v2 b\n" {
		t.Fatalf("private remote b.txt = %q; the stale workspace copy was committed over the pushed change", got)
	}
	if content, _ := os.ReadFile(filepath.Join(publicRoot, "b.txt")); string(content) != "v2 b\n" {
		t.Fatalf("workspace b.txt = %q, want the pushed version restored", content)
	}
}

func TestInterruptedMaterializationRejectsDirtyPrivateClone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{
		"a.txt": "v1 a\n",
		"b.txt": "v1 b\n",
	})
	teammatePush(t, root, remote, map[string]string{
		"a.txt": "v2 a\n",
		"b.txt": "v2 b\n",
	}, nil)

	state := loadState(t, instance, publicRoot)
	private := instance.privateRepository(state)
	if err := private.Fetch(ctx, state.Private.Branch); err != nil {
		t.Fatal(err)
	}
	if _, err := private.MergeRemote(ctx, state.Private.Branch); err != nil {
		t.Fatal(err)
	}
	head, err := private.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state.Materializing = testMaterialization(
		t,
		publicRoot,
		linkstate.MaterializationPushed,
		head,
		[]string{"a.txt", "b.txt"},
		[]string{"a.txt", "b.txt"},
		[]string{"a.txt", "b.txt"},
		nil,
		nil,
	)
	saveState(t, instance, state)

	privatePath := filepath.Join(private.Path, "b.txt")
	if err := os.WriteFile(privatePath, []byte("uncommitted private-clone edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = instance.Sync(ctx, syncOptions(""))
	if err == nil || !strings.Contains(err.Error(), "private clone changed while materialization recovery was pending") {
		t.Fatalf("Sync() error = %v, want dirty private-clone recovery rejection", err)
	}
	if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsafeGitState {
		t.Fatalf("Sync() error kind = %v, %v; want unsafe Git state", kind, ok)
	}
	if content, readErr := os.ReadFile(filepath.Join(publicRoot, "b.txt")); readErr != nil || string(content) != "v1 b\n" {
		t.Fatalf("workspace b.txt = %q, %v; want unchanged pre-materialization bytes", content, readErr)
	}
	if content, readErr := os.ReadFile(privatePath); readErr != nil || string(content) != "uncommitted private-clone edit\n" {
		t.Fatalf("private clone b.txt = %q, %v; want dirty bytes preserved for diagnosis", content, readErr)
	}
	if got := gitOutput(t, private.Path, "rev-parse", "HEAD"); strings.TrimSpace(got) != head {
		t.Fatalf("private HEAD = %q, want %q", strings.TrimSpace(got), head)
	}
	if got := gitOutput(t, root, "--git-dir="+remote, "show", "main:b.txt"); got != "v2 b\n" {
		t.Fatalf("private remote b.txt = %q, want recorded committed bytes", got)
	}
	if reloaded := loadState(t, instance, publicRoot); reloaded.Materializing == nil {
		t.Fatal("unsafe recovery cleared its materialization marker")
	}
}

func TestInterruptedMaterializationRejectsNewWorkspaceFile(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{".env": "TOKEN=one\n"})
	teammatePush(t, root, remote, map[string]string{"remote.txt": "private\n"}, nil)

	state := loadState(t, instance, publicRoot)
	private := instance.privateRepository(state)
	if err := private.Fetch(ctx, state.Private.Branch); err != nil {
		t.Fatal(err)
	}
	if _, err := private.MergeRemote(ctx, state.Private.Branch); err != nil {
		t.Fatal(err)
	}
	head, err := private.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state.Materializing = testMaterialization(
		t,
		publicRoot,
		linkstate.MaterializationPushed,
		head,
		[]string{".env"},
		[]string{".env", "remote.txt"},
		[]string{".env", "remote.txt"},
		nil,
		nil,
	)
	saveState(t, instance, state)

	localPath := filepath.Join(publicRoot, "remote.txt")
	if err := os.WriteFile(localPath, []byte("created after interruption\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = instance.Sync(ctx, syncOptions(""))
	if err == nil || !strings.Contains(err.Error(), "changed after synchronization planning") {
		t.Fatalf("Sync() error = %v, want post-marker obstruction rejection", err)
	}
	content, readErr := os.ReadFile(localPath)
	if readErr != nil || string(content) != "created after interruption\n" {
		t.Fatalf("workspace obstruction = %q, %v; want unchanged bytes", content, readErr)
	}
	if reloaded := loadState(t, instance, publicRoot); reloaded.Materializing == nil {
		t.Fatal("unsafe recovery cleared its materialization marker")
	}
}

func TestInterruptedMaterializationRejectsManagedEdit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{"managed.txt": "v1\n"})
	teammatePush(t, root, remote, map[string]string{"managed.txt": "v2\n"}, nil)

	state := loadState(t, instance, publicRoot)
	private := instance.privateRepository(state)
	if err := private.Fetch(ctx, state.Private.Branch); err != nil {
		t.Fatal(err)
	}
	if _, err := private.MergeRemote(ctx, state.Private.Branch); err != nil {
		t.Fatal(err)
	}
	head, err := private.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state.Materializing = testMaterialization(
		t,
		publicRoot,
		linkstate.MaterializationPushed,
		head,
		[]string{"managed.txt"},
		[]string{"managed.txt"},
		[]string{"managed.txt"},
		nil,
		nil,
	)
	saveState(t, instance, state)

	localPath := filepath.Join(publicRoot, "managed.txt")
	if err := os.WriteFile(localPath, []byte("edited after interruption\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = instance.Sync(ctx, syncOptions(""))
	if err == nil || !strings.Contains(err.Error(), "changed after synchronization planning") {
		t.Fatalf("Sync() error = %v, want post-marker edit rejection", err)
	}
	content, readErr := os.ReadFile(localPath)
	if readErr != nil || string(content) != "edited after interruption\n" {
		t.Fatalf("managed workspace file = %q, %v; want unchanged edit", content, readErr)
	}
	if reloaded := loadState(t, instance, publicRoot); reloaded.Materializing == nil {
		t.Fatal("unsafe recovery cleared its materialization marker")
	}
}

func TestMergeContinuationRetainsApprovedObstructionRecovery(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{"conflict.txt": "base\n"})
	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	obstructionPath := filepath.Join(publicRoot, "obstruction.txt")
	if err := os.WriteFile(obstructionPath, []byte("keep this local copy\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	teammatePush(t, root, remote, map[string]string{
		"conflict.txt":    "remote\n",
		"obstruction.txt": "private replacement\n",
	}, nil)

	err := instance.Sync(ctx, SyncOptions{
		Message:         "local conflict",
		Conflict:        ConflictOverride,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want private merge conflict", err)
	}
	state := loadState(t, instance, publicRoot)
	if state.ActiveMerge == nil {
		t.Fatal("private merge recovery state is missing")
	}
	if _, ok := stringSet(state.ActiveMerge.RecoveryPaths)["obstruction.txt"]; !ok {
		t.Fatalf("active merge recovery paths = %v, want obstruction.txt", state.ActiveMerge.RecoveryPaths)
	}

	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("resolved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(ctx, SyncOptions{Continue: true, Message: "resolve conflict"}); err != nil {
		t.Fatalf("Sync(continue) error = %v", err)
	}
	content, err := os.ReadFile(obstructionPath)
	if err != nil || string(content) != "private replacement\n" {
		t.Fatalf("materialized obstruction path = %q, %v", content, err)
	}

	recoveryRoot := filepath.Join(instance.Store.DataDir, "recovery", state.LinkID)
	operations, err := os.ReadDir(recoveryRoot)
	if err != nil {
		t.Fatal(err)
	}
	var recovered bool
	for _, operation := range operations {
		content, readErr := os.ReadFile(filepath.Join(recoveryRoot, operation.Name(), "obstruction.txt"))
		if readErr == nil && string(content) == "keep this local copy\n" {
			info, statErr := os.Stat(filepath.Join(recoveryRoot, operation.Name(), "obstruction.txt"))
			if statErr != nil {
				t.Fatal(statErr)
			}
			if info.Mode().Perm()&0o100 == 0 {
				t.Fatalf("recovery copy mode = %v, want executable bit", info.Mode())
			}
			recovered = true
			break
		}
	}
	if !recovered {
		t.Fatalf("approved obstruction recovery copy not found under %s", recoveryRoot)
	}
}

func TestPrivateMergeContinueRejectsIncompleteConflictMaterialization(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{"conflict.txt": "base\n"})
	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	teammatePush(t, root, remote, map[string]string{"conflict.txt": "remote\n"}, nil)

	err := instance.Sync(ctx, SyncOptions{
		Message:         "local conflict",
		Conflict:        ConflictOverride,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want private merge conflict", err)
	}
	state := loadState(t, instance, publicRoot)
	if state.ActiveMerge == nil || !state.ActiveMerge.ConflictFilesReady {
		t.Fatalf("active merge state = %#v, want ready conflict materialization", state.ActiveMerge)
	}
	state.ActiveMerge.ConflictFilesReady = false
	saveState(t, instance, state)

	resolved := filepath.Join(publicRoot, "conflict.txt")
	if err := os.WriteFile(resolved, []byte("would-be resolution\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = instance.Sync(ctx, SyncOptions{Continue: true, Message: "must not accept"})
	if err == nil || !strings.Contains(err.Error(), "materialization was interrupted") || !strings.Contains(err.Error(), "sync --abort") {
		t.Fatalf("Sync(continue) error = %v, want interrupted-materialization abort guidance", err)
	}
	content, readErr := os.ReadFile(resolved)
	if readErr != nil || string(content) != "would-be resolution\n" {
		t.Fatalf("workspace resolution = %q, %v; want untouched", content, readErr)
	}

	if err := instance.Sync(ctx, SyncOptions{Abort: true}); err != nil {
		t.Fatalf("Sync(abort) error = %v; an incomplete preparation must remain abortable", err)
	}
	reloaded := loadState(t, instance, publicRoot)
	if reloaded.ActiveMerge != nil {
		t.Fatalf("active merge after abort = %#v, want none", reloaded.ActiveMerge)
	}
	content, readErr = os.ReadFile(resolved)
	if readErr != nil || string(content) != "local\n" {
		t.Fatalf("workspace after abort = %q, %v; want restored pre-merge bytes", content, readErr)
	}
}

func TestGitNativeAbortRecoversMergeWithoutSPASState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{"conflict.txt": "base\n"})
	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	teammatePush(t, root, remote, map[string]string{"conflict.txt": "remote\n"}, nil)
	if err := instance.Sync(ctx, syncOptions("local conflict")); !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want private merge conflict", err)
	}
	state := loadState(t, instance, publicRoot)
	state.ActiveMerge = nil
	saveState(t, instance, state)

	if err := instance.Sync(ctx, syncOptions("must not continue")); err == nil || !strings.Contains(err.Error(), "run spas sync --abort") {
		t.Fatalf("Sync() error = %v, want abort-required guidance", err)
	}
	if err := instance.Sync(ctx, SyncOptions{Continue: true, Message: "must not continue"}); err == nil || !strings.Contains(err.Error(), "abort") {
		t.Fatalf("Sync(--continue) error = %v, want abort-required guidance", err)
	}
	if err := instance.Sync(ctx, SyncOptions{Abort: true}); err != nil {
		t.Fatalf("Sync(--abort) error = %v", err)
	}
	merging, err := instance.privateRepository(state).MergeInProgress()
	if err != nil {
		t.Fatal(err)
	}
	if merging {
		t.Fatal("Git merge metadata remained after Git-native abort")
	}
}

func TestAbortOnlyMergeRecoveryClearsStateWithoutPanic(t *testing.T) {
	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{"conflict.txt": "base\n"})
	state := loadState(t, instance, publicRoot)
	private := instance.privateRepository(state)
	conflictPath, err := pathmodel.Parse("conflict.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private.Path, "conflict.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := private.Stage(ctx, []pathmodel.Path{conflictPath}); err != nil {
		t.Fatal(err)
	}
	if err := private.Commit(ctx, "local private change"); err != nil {
		t.Fatal(err)
	}
	teammatePush(t, root, remote, map[string]string{"conflict.txt": "remote\n"}, nil)

	if err := private.Fetch(ctx, state.Private.Branch); err != nil {
		t.Fatal(err)
	}
	preMergeHead, err := private.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mergeHead, err := private.RemoteHead(ctx, state.Private.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := private.MergeRemote(ctx, state.Private.Branch); err == nil {
		t.Fatal("MergeRemote() unexpectedly succeeded")
	}
	merging, err := private.MergeInProgress()
	if err != nil {
		t.Fatal(err)
	}
	if !merging {
		t.Fatal("MergeRemote() did not leave Git merge metadata for the abort-only state")
	}
	state.ActiveMerge = &linkstate.ActiveMerge{
		PreMergeHead: preMergeHead,
		MergeHead:    mergeHead,
	}
	state.Private.ExpectedHead = preMergeHead
	saveState(t, instance, state)

	if err := instance.Sync(ctx, SyncOptions{Abort: true}); err != nil {
		t.Fatalf("Sync(abort) error = %v", err)
	}
	reloaded := loadState(t, instance, publicRoot)
	if reloaded.ActiveMerge != nil {
		t.Fatalf("abort-only recovery state remains: %#v", reloaded.ActiveMerge)
	}
}

func TestAbortMismatchBeforeLiveMergeDoesNotCreateRecoveryCopies(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{"conflict.txt": "base\n"})
	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	teammatePush(t, root, remote, map[string]string{"conflict.txt": "remote\n"}, nil)
	if err := instance.Sync(ctx, SyncOptions{
		Message:         "local conflict",
		Conflict:        ConflictOverride,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	}); !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want private merge conflict", err)
	}

	state := loadState(t, instance, publicRoot)
	if state.ActiveMerge == nil || !state.ActiveMerge.ConflictFilesReady || len(state.ActiveMerge.ConflictPaths) == 0 {
		t.Fatalf("active merge after conflict = %#v, want materialized conflict state", state.ActiveMerge)
	}
	private := instance.privateRepository(state)
	merging, err := private.MergeInProgress()
	if err != nil {
		t.Fatal(err)
	}
	if !merging {
		t.Fatal("conflict setup did not leave Git merge metadata in progress")
	}
	if err := private.AbortMerge(ctx); err != nil {
		t.Fatalf("clear live Git merge metadata: %v", err)
	}
	merging, err = private.MergeInProgress()
	if err != nil {
		t.Fatal(err)
	}
	if merging {
		t.Fatal("Git merge metadata remained after setup abort")
	}

	movePath, err := pathmodel.Parse("abort-mismatch-moved-head.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(movePath.OSPath(private.Path), []byte("moved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := private.Stage(ctx, []pathmodel.Path{movePath}); err != nil {
		t.Fatal(err)
	}
	if err := private.Commit(ctx, "move private head after merge abort"); err != nil {
		t.Fatal(err)
	}
	movedHead, err := private.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if movedHead == state.ActiveMerge.PreMergeHead {
		t.Fatalf("private HEAD = %q, want movement away from bound pre-merge HEAD", movedHead)
	}

	statePath := filepath.Join(instance.Store.ConfigDir, "links", state.LinkID+".json")
	stateBefore, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	workspaceBefore := make(map[string][]byte, len(state.ActiveMerge.ConflictPaths))
	for _, value := range state.ActiveMerge.ConflictPaths {
		path, parseErr := pathmodel.Parse(value)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		content, readErr := os.ReadFile(path.OSPath(publicRoot))
		if readErr != nil {
			t.Fatalf("read conflict workspace path %q: %v", value, readErr)
		}
		workspaceBefore[value] = content
	}
	recoveryRoot := filepath.Join(instance.Store.DataDir, "recovery", state.LinkID)
	readRecoveryOperations := func() map[string]struct{} {
		entries, readErr := os.ReadDir(recoveryRoot)
		if errors.Is(readErr, os.ErrNotExist) {
			return map[string]struct{}{}
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		operations := make(map[string]struct{}, len(entries))
		for _, entry := range entries {
			operations[entry.Name()] = struct{}{}
		}
		return operations
	}
	operationsBefore := readRecoveryOperations()

	err = instance.Sync(ctx, SyncOptions{Abort: true})
	if err == nil || !strings.Contains(err.Error(), "private branch moved while merge abort recovery was pending") {
		t.Fatalf("Sync(abort) error = %v, want moved-head rejection", err)
	}
	stateAfter, readErr := os.ReadFile(statePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(stateAfter, stateBefore) {
		t.Fatal("Sync(abort) changed persisted state before rejecting moved private HEAD")
	}
	for value, before := range workspaceBefore {
		path, parseErr := pathmodel.Parse(value)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		after, readErr := os.ReadFile(path.OSPath(publicRoot))
		if readErr != nil {
			t.Fatalf("read conflict workspace path %q after abort rejection: %v", value, readErr)
		}
		if !bytes.Equal(after, before) {
			t.Fatalf("workspace path %q changed before moved-head rejection", value)
		}
	}
	operationsAfter := readRecoveryOperations()
	if !reflect.DeepEqual(operationsAfter, operationsBefore) {
		t.Fatalf("recovery operations changed before moved-head rejection: before=%v after=%v", operationsBefore, operationsAfter)
	}
}

func TestAbortDoesNotMutateMismatchedGitAndSPASMergeBindings(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{"conflict.txt": "base\n"})
	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	teammatePush(t, root, remote, map[string]string{"conflict.txt": "remote\n"}, nil)
	if err := instance.Sync(ctx, syncOptions("local conflict")); !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want private merge conflict", err)
	}
	state := loadState(t, instance, publicRoot)
	state.ActiveMerge.MergeHead = strings.Repeat("f", len(state.ActiveMerge.MergeHead))
	saveState(t, instance, state)

	if err := instance.Sync(ctx, SyncOptions{Abort: true}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("Sync(--abort) error = %v, want binding mismatch", err)
	}
	merging, err := instance.privateRepository(state).MergeInProgress()
	if err != nil {
		t.Fatal(err)
	}
	if !merging {
		t.Fatal("mismatched recovery state mutated the live Git merge")
	}
}

func TestFailedAutomaticMergeAbortRetainsRecoveryState(t *testing.T) {
	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{"conflict.txt": "base\n"})
	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	teammatePush(t, root, remote, map[string]string{"conflict.txt": "remote\n"}, nil)

	err := instance.Sync(ctx, SyncOptions{
		Message:         "local conflict",
		Conflict:        ConflictOverride,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want private merge conflict", err)
	}
	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("resolved\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPAS_APP_GIT_PROXY", "fail-write-tree-and-abort")
	t.Setenv("SPAS_APP_REAL_GIT", realGit)
	instance.Git.Path = os.Args[0]
	err = instance.Sync(ctx, SyncOptions{Continue: true, Message: "resolve conflict"})
	if err == nil || !strings.Contains(err.Error(), "recovery state was retained") ||
		!strings.Contains(err.Error(), "spas sync --abort") {
		t.Fatalf("Sync(continue) error = %v, want retained-state abort guidance", err)
	}
	state := loadState(t, instance, publicRoot)
	if state.ActiveMerge == nil || state.Materializing == nil {
		t.Fatalf("failed abort cleared recovery state: active=%#v materializing=%#v", state.ActiveMerge, state.Materializing)
	}
	mergeInProgress, mergeProbeErr := instance.privateRepository(state).MergeInProgress()
	if mergeProbeErr != nil {
		t.Fatal(mergeProbeErr)
	}
	if !mergeInProgress {
		t.Fatal("injected abort failure did not leave the private merge in progress")
	}

	if err := os.Unsetenv("SPAS_APP_GIT_PROXY"); err != nil {
		t.Fatal(err)
	}
	instance.Git.Path = ""
	if err := instance.Sync(ctx, SyncOptions{Abort: true}); err != nil {
		t.Fatalf("Sync(abort) after injected failure error = %v", err)
	}
	state = loadState(t, instance, publicRoot)
	if state.ActiveMerge != nil || state.Materializing != nil {
		t.Fatalf("successful retry retained recovery state: active=%#v materializing=%#v", state.ActiveMerge, state.Materializing)
	}
}

func TestAutomaticMergeAbortDoesNotRunWithoutDurableMarker(t *testing.T) {
	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{"conflict.txt": "base\n"})
	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	teammatePush(t, root, remote, map[string]string{"conflict.txt": "remote\n"}, nil)
	err := instance.Sync(ctx, SyncOptions{
		Message:         "local conflict",
		Conflict:        ConflictOverride,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want private merge conflict", err)
	}
	state := loadState(t, instance, publicRoot)
	private := instance.privateRepository(state)

	blocker := filepath.Join(root, "not-a-data-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	brokenStore := instance
	brokenStore.Store.DataDir = blocker
	err = brokenStore.abortFailedMerge(ctx, private, &state, errors.New("injected validation failure"))
	if err == nil || !strings.Contains(err.Error(), "abort was not attempted") {
		t.Fatalf("abortFailedMerge() error = %v, want marker-persistence guidance", err)
	}
	mergeInProgress, mergeProbeErr := private.MergeInProgress()
	if mergeProbeErr != nil {
		t.Fatal(mergeProbeErr)
	}
	if !mergeInProgress {
		t.Fatal("abortFailedMerge() changed Git merge state without a durable marker")
	}
}

func TestAutomaticMergeAbortRetainsRecoveryStateWhenMarkerCannotBeInspected(t *testing.T) {
	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{"conflict.txt": "base\n"})
	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	teammatePush(t, root, remote, map[string]string{"conflict.txt": "remote\n"}, nil)

	err := instance.Sync(ctx, SyncOptions{
		Message:         "local conflict",
		Conflict:        ConflictOverride,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want private merge conflict", err)
	}
	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("resolved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := loadState(t, instance, publicRoot)
	private := instance.privateRepository(state)
	marker := filepath.Join(private.Path, ".git", "MERGE_HEAD")
	requireSymlinkSupport(t, filepath.Dir(marker))

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPAS_APP_GIT_PROXY", "fail-write-tree-and-recreate-marker")
	t.Setenv("SPAS_APP_REAL_GIT", realGit)
	t.Setenv("SPAS_APP_MERGE_MARKER", marker)
	instance.Git.Path = os.Args[0]
	err = instance.Sync(ctx, SyncOptions{Continue: true, Message: "resolve conflict"})
	reloaded := loadState(t, instance, publicRoot)
	markerInfo, markerErr := os.Lstat(marker)
	if err == nil || !strings.Contains(err.Error(), "recovery state was retained") {
		t.Fatalf("Sync(continue) error = %v, state active=%t materializing=%t marker=%v markerErr=%v, want retained-state error", err, reloaded.ActiveMerge != nil, reloaded.Materializing != nil, markerInfo, markerErr)
	}
	if reloaded.ActiveMerge == nil || reloaded.Materializing == nil {
		t.Fatalf("failed abort cleared recovery state: active=%#v materializing=%#v", reloaded.ActiveMerge, reloaded.Materializing)
	}
	if info, statErr := os.Lstat(marker); statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("merge marker = %v, %v; want retained symlink marker", info, statErr)
	}
}

func TestMergeAbortRetainsRecoveryStateWhenMergeMarkerCannotBeInspected(t *testing.T) {
	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{"conflict.txt": "base\n"})
	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	teammatePush(t, root, remote, map[string]string{"conflict.txt": "remote\n"}, nil)
	if err := instance.Sync(ctx, SyncOptions{
		Message:         "local conflict",
		Conflict:        ConflictOverride,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	}); !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want private merge conflict", err)
	}

	state := loadState(t, instance, publicRoot)
	private := instance.privateRepository(state)
	marker := filepath.Join(private.Path, ".git", "MERGE_HEAD")
	requireSymlinkSupport(t, filepath.Dir(marker))
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPAS_APP_GIT_PROXY", "recreate-marker-after-abort")
	t.Setenv("SPAS_APP_REAL_GIT", realGit)
	t.Setenv("SPAS_APP_MERGE_MARKER", marker)
	instance.Git.Path = os.Args[0]

	err = instance.Sync(ctx, SyncOptions{Abort: true})
	if err == nil {
		t.Fatal("Sync(abort) succeeded with an uninspectable merge marker")
	}
	reloaded := loadState(t, instance, publicRoot)
	if reloaded.ActiveMerge == nil {
		t.Fatal("Sync(abort) cleared active merge recovery state")
	}
}

func TestMergeAbortRejectsDirtyPrivateCloneBeforeMaterialization(t *testing.T) {
	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{"conflict.txt": "base\n"})
	conflictPath := filepath.Join(publicRoot, "conflict.txt")
	if err := os.WriteFile(conflictPath, []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	teammatePush(t, root, remote, map[string]string{"conflict.txt": "remote\n"}, nil)
	if err := instance.Sync(ctx, SyncOptions{
		Message:         "local conflict",
		Conflict:        ConflictOverride,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	}); !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want private merge conflict", err)
	}

	state := loadState(t, instance, publicRoot)
	private := instance.privateRepository(state)
	privatePath := filepath.Join(private.Path, "conflict.txt")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPAS_APP_GIT_PROXY", "edit-private-on-abort-tracked-paths")
	t.Setenv("SPAS_APP_REAL_GIT", realGit)
	t.Setenv("SPAS_APP_EDIT_PATH", privatePath)
	t.Setenv("SPAS_APP_EDIT_CONTENT", "dirty private abort source\n")
	instance.Git.Path = os.Args[0]

	err = instance.Sync(ctx, SyncOptions{Abort: true})
	if err == nil || !strings.Contains(err.Error(), "private clone") {
		t.Fatalf("Sync(abort) error = %v, want dirty-private-source rejection", err)
	}
	if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsafeGitState {
		t.Fatalf("Sync(abort) error kind = %v, %v; want unsafe Git state", kind, ok)
	}
	reloaded := loadState(t, instance, publicRoot)
	if reloaded.ActiveMerge == nil {
		t.Fatal("dirty private source rejection cleared active merge recovery state")
	}
	if content, readErr := os.ReadFile(privatePath); readErr != nil || string(content) != "dirty private abort source\n" {
		t.Fatalf("private source = %q, %v; want dirty bytes retained", content, readErr)
	}
	if content, readErr := os.ReadFile(conflictPath); readErr != nil || string(content) == "dirty private abort source\n" {
		t.Fatalf("workspace conflict = %q, %v; dirty private bytes were materialized", content, readErr)
	}
}

func TestMergeAbortRejectsWorkspaceEditDuringGitAbort(t *testing.T) {
	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{"conflict.txt": "base\n"})
	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	teammatePush(t, root, remote, map[string]string{"conflict.txt": "remote\n"}, nil)
	err := instance.Sync(ctx, SyncOptions{
		Message:         "local conflict",
		Conflict:        ConflictOverride,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want private merge conflict", err)
	}
	conflictPath := filepath.Join(publicRoot, "conflict.txt")
	if err := os.WriteFile(conflictPath, []byte("resolution before abort\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPAS_APP_GIT_PROXY", "edit-on-private-abort")
	t.Setenv("SPAS_APP_REAL_GIT", realGit)
	t.Setenv("SPAS_APP_EDIT_PATH", conflictPath)
	t.Setenv("SPAS_APP_EDIT_CONTENT", "edit during abort\n")
	instance.Git.Path = os.Args[0]
	err = instance.Sync(ctx, SyncOptions{Abort: true})
	if err == nil || !strings.Contains(err.Error(), "workspace changed while Git was aborting") {
		t.Fatalf("Sync(abort) error = %v, want concurrent-edit rejection", err)
	}
	content, readErr := os.ReadFile(conflictPath)
	if readErr != nil || string(content) != "edit during abort\n" {
		t.Fatalf("workspace content = %q, %v; want concurrent edit preserved", content, readErr)
	}
	state := loadState(t, instance, publicRoot)
	if state.ActiveMerge == nil {
		t.Fatal("concurrent-edit rejection cleared active merge recovery state")
	}

	if err := os.Unsetenv("SPAS_APP_GIT_PROXY"); err != nil {
		t.Fatal(err)
	}
	instance.Git.Path = ""
	if err := instance.Sync(ctx, SyncOptions{Abort: true}); err != nil {
		t.Fatalf("Sync(abort retry) error = %v", err)
	}
}

func TestMergeAbortRejectsWorkspaceEditAfterGitAbort(t *testing.T) {
	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{"conflict.txt": "base\n"})
	conflictPath := filepath.Join(publicRoot, "conflict.txt")
	if err := os.WriteFile(conflictPath, []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	teammatePush(t, root, remote, map[string]string{"conflict.txt": "remote\n"}, nil)
	err := instance.Sync(ctx, SyncOptions{
		Message:         "local conflict",
		Conflict:        ConflictOverride,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want private merge conflict", err)
	}
	if err := os.WriteFile(conflictPath, []byte("resolution before abort\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPAS_APP_GIT_PROXY", "edit-after-private-abort")
	t.Setenv("SPAS_APP_REAL_GIT", realGit)
	t.Setenv("SPAS_APP_ABORT_MARKER", filepath.Join(root, "abort-completed"))
	t.Setenv("SPAS_APP_EDIT_PATH", conflictPath)
	t.Setenv("SPAS_APP_EDIT_CONTENT", "edit after abort\n")
	instance.Git.Path = os.Args[0]
	err = instance.Sync(ctx, SyncOptions{Abort: true})
	if err == nil || !strings.Contains(err.Error(), "changed after synchronization planning") {
		t.Fatalf("Sync(abort) error = %v, want post-abort edit rejection", err)
	}
	content, readErr := os.ReadFile(conflictPath)
	if readErr != nil || string(content) != "edit after abort\n" {
		t.Fatalf("workspace content = %q, %v; want post-abort edit preserved", content, readErr)
	}
	state := loadState(t, instance, publicRoot)
	if state.ActiveMerge == nil {
		t.Fatal("post-abort edit rejection cleared active merge recovery state")
	}
}

func TestPrivateMergeContinueRejectsChangedPrivateConflictSet(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{"conflict.txt": "base\n"})
	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	teammatePush(t, root, remote, map[string]string{"conflict.txt": "remote\n"}, nil)

	err := instance.Sync(ctx, SyncOptions{
		Message:         "local conflict",
		Conflict:        ConflictOverride,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want private merge conflict", err)
	}
	state := loadState(t, instance, publicRoot)
	private := instance.privateRepository(state)
	runGit(t, private.Path, "add", "--", "conflict.txt")
	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("workspace resolution\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = instance.Sync(ctx, SyncOptions{Continue: true, Message: "must not accept"})
	if err == nil || !strings.Contains(err.Error(), "conflict paths changed outside SPAS") {
		t.Fatalf("Sync(continue) error = %v, want private conflict-set rejection", err)
	}
	if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsafeGitState {
		t.Fatalf("Sync() error kind = %v, %v; want unsafe Git state", kind, ok)
	}
	if content, readErr := os.ReadFile(filepath.Join(publicRoot, "conflict.txt")); readErr != nil || string(content) != "workspace resolution\n" {
		t.Fatalf("workspace conflict = %q, %v; want resolution preserved", content, readErr)
	}
	if reloaded := loadState(t, instance, publicRoot); reloaded.ActiveMerge == nil {
		t.Fatal("unsafe continuation cleared active merge recovery state")
	}
}

func TestPrivateMergeContinueRejectsChangedApprovedObstruction(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{"conflict.txt": "base\n"})
	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	obstruction := filepath.Join(publicRoot, "obstruction.txt")
	if err := os.WriteFile(obstruction, []byte("approved original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	teammatePush(t, root, remote, map[string]string{
		"conflict.txt":    "remote\n",
		"obstruction.txt": "private replacement\n",
	}, nil)

	err := instance.Sync(ctx, SyncOptions{
		Message:         "local conflict",
		Conflict:        ConflictOverride,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want private merge conflict", err)
	}
	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("resolved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(obstruction, []byte("edited after conflict\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = instance.Sync(ctx, SyncOptions{Continue: true, Message: "resolve conflict"})
	if err == nil || !strings.Contains(err.Error(), "changed while the private merge was being resolved") {
		t.Fatalf("Sync(continue) error = %v, want changed-obstruction rejection", err)
	}
	content, readErr := os.ReadFile(obstruction)
	if readErr != nil || string(content) != "edited after conflict\n" {
		t.Fatalf("obstruction = %q, %v; want later edit preserved", content, readErr)
	}

	// Removing the obstruction explicitly makes room for the already-approved
	// private replacement without treating the changed bytes as disposable.
	if err := os.Remove(obstruction); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(ctx, SyncOptions{Continue: true, Message: "resolve conflict"}); err != nil {
		t.Fatalf("Sync(continue after clearing obstruction) error = %v", err)
	}
	content, readErr = os.ReadFile(obstruction)
	if readErr != nil || string(content) != "private replacement\n" {
		t.Fatalf("materialized obstruction = %q, %v", content, readErr)
	}
}

func TestPrivateMergeContinueRejectsNewUntrackedMaterializationPath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{"conflict.txt": "base\n"})
	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	teammatePush(t, root, remote, map[string]string{
		"conflict.txt": "remote\n",
		"late.txt":     "private replacement\n",
	}, nil)

	err := instance.Sync(ctx, SyncOptions{
		Message:         "local conflict",
		Conflict:        ConflictOverride,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want private merge conflict", err)
	}
	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("resolved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	latePath := filepath.Join(publicRoot, "late.txt")
	if err := os.WriteFile(latePath, []byte("developer file created during resolution\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = instance.Sync(ctx, SyncOptions{Continue: true, Message: "resolve conflict"})
	if err == nil || !strings.Contains(err.Error(), "changed while the private merge was being resolved") {
		t.Fatalf("Sync(continue) error = %v, want conflict-start snapshot rejection", err)
	}
	if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsafeGitState {
		t.Fatalf("Sync(continue) error kind = %v, %v; want unsafe Git state", kind, ok)
	}
	content, readErr := os.ReadFile(latePath)
	if readErr != nil || string(content) != "developer file created during resolution\n" {
		t.Fatalf("late workspace file = %q, %v; want developer bytes preserved", content, readErr)
	}
	if reloaded := loadState(t, instance, publicRoot); reloaded.ActiveMerge == nil {
		t.Fatal("failed continuation cleared active merge recovery state")
	}
}

func TestPrivateMergeContinueRejectsUnrelatedManagedEdit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{
		"conflict.txt": "base\n",
		"other.txt":    "unchanged\n",
	})
	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	teammatePush(t, root, remote, map[string]string{"conflict.txt": "remote\n"}, nil)

	err := instance.Sync(ctx, SyncOptions{
		Message:         "local conflict",
		Conflict:        ConflictOverride,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want private merge conflict", err)
	}
	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("resolved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	otherPath := filepath.Join(publicRoot, "other.txt")
	if err := os.WriteFile(otherPath, []byte("edited during resolution\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = instance.Sync(ctx, SyncOptions{Continue: true, Message: "resolve conflict"})
	if err == nil || !strings.Contains(err.Error(), "changed while the private merge was being resolved") {
		t.Fatalf("Sync(continue) error = %v, want unrelated-edit rejection", err)
	}
	content, readErr := os.ReadFile(otherPath)
	if readErr != nil || string(content) != "edited during resolution\n" {
		t.Fatalf("other workspace file = %q, %v; want edit preserved", content, readErr)
	}
	if reloaded := loadState(t, instance, publicRoot); reloaded.ActiveMerge == nil {
		t.Fatal("failed continuation cleared active merge recovery state")
	}

	if err := os.WriteFile(otherPath, []byte("unchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(ctx, SyncOptions{Continue: true, Message: "resolve conflict"}); err != nil {
		t.Fatalf("Sync(continue after restoring unrelated path) error = %v", err)
	}
}

func TestPrivateMergeAbortRejectsUnrelatedManagedEdit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{
		"conflict.txt": "base\n",
		"other.txt":    "unchanged\n",
	})
	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	teammatePush(t, root, remote, map[string]string{"conflict.txt": "remote\n"}, nil)

	err := instance.Sync(ctx, SyncOptions{
		Message:         "local conflict",
		Conflict:        ConflictOverride,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want private merge conflict", err)
	}
	otherPath := filepath.Join(publicRoot, "other.txt")
	if err := os.WriteFile(otherPath, []byte("edited before abort\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = instance.Sync(ctx, SyncOptions{Abort: true})
	if err == nil || !strings.Contains(err.Error(), "changed while the private merge was being resolved") {
		t.Fatalf("Sync(abort) error = %v, want unrelated-edit rejection", err)
	}
	content, readErr := os.ReadFile(otherPath)
	if readErr != nil || string(content) != "edited before abort\n" {
		t.Fatalf("other workspace file = %q, %v; want edit preserved", content, readErr)
	}
	state := loadState(t, instance, publicRoot)
	if state.ActiveMerge == nil {
		t.Fatal("failed abort cleared active merge recovery state")
	}
	mergeInProgress, mergeProbeErr := instance.privateRepository(state).MergeInProgress()
	if mergeProbeErr != nil {
		t.Fatal(mergeProbeErr)
	}
	if !mergeInProgress {
		t.Fatal("failed abort changed the private merge before workspace validation")
	}

	if err := os.WriteFile(otherPath, []byte("unchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(ctx, SyncOptions{Abort: true}); err != nil {
		t.Fatalf("Sync(abort after restoring unrelated path) error = %v", err)
	}
}

func TestMergeConflictPreservesOverlappingApprovedObstruction(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{"seed.txt": "seed\n"})

	state := loadState(t, instance, publicRoot)
	private := instance.privateRepository(state)
	privatePath := filepath.Join(private.Path, "overlap.txt")
	if err := os.WriteFile(privatePath, []byte("local private addition\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, private.Path, "add", "--", "overlap.txt")
	runGit(t, private.Path, "commit", "-q", "-m", "local private addition")

	teammatePush(t, root, remote, map[string]string{
		"overlap.txt": "remote private addition\n",
	}, nil)

	workspacePath := filepath.Join(publicRoot, "overlap.txt")
	if err := os.WriteFile(workspacePath, []byte("untracked workspace original\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictOverride,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if kind, ok := spaserr.KindOf(err); ok && kind == spaserr.KindUnsafeGitState && strings.Contains(err.Error(), "managed private HEAD mismatch") {
		return
	}
	if !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want private merge conflict", err)
	}

	conflicted, readErr := os.ReadFile(workspacePath)
	if readErr != nil || !bytes.Contains(conflicted, []byte("<<<<<<<")) {
		t.Fatalf("workspace conflict = %q, %v; want conflict markers", conflicted, readErr)
	}

	recoveryRoot := filepath.Join(instance.Store.DataDir, "recovery", state.LinkID)
	operations, readErr := os.ReadDir(recoveryRoot)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var recovered bool
	for _, operation := range operations {
		recoveryPath := filepath.Join(recoveryRoot, operation.Name(), "overlap.txt")
		content, err := os.ReadFile(recoveryPath)
		if err != nil || string(content) != "untracked workspace original\n" {
			continue
		}
		info, err := os.Stat(recoveryPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o100 == 0 {
			t.Fatalf("recovery copy mode = %v, want executable bit", info.Mode())
		}
		recovered = true
		break
	}
	if !recovered {
		t.Fatalf("original overlapping obstruction was not retained under %s", recoveryRoot)
	}
}

func TestMergeAbortPreservesDistinctApprovedObstruction(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{"seed.txt": "seed\n"})

	state := loadState(t, instance, publicRoot)
	private := instance.privateRepository(state)
	for name, content := range map[string]string{
		"conflict.txt":    "local private addition\n",
		"obstruction.txt": "local private obstruction replacement\n",
	} {
		if err := os.WriteFile(filepath.Join(private.Path, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, private.Path, "add", "--", name)
	}
	runGit(t, private.Path, "commit", "-q", "-m", "local private additions")

	teammatePush(t, root, remote, map[string]string{
		"conflict.txt": "remote private addition\n",
	}, nil)

	obstructionPath := filepath.Join(publicRoot, "obstruction.txt")
	if err := os.WriteFile(obstructionPath, []byte("untracked workspace original\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := instance.Sync(ctx, SyncOptions{
		Conflict:        ConflictOverride,
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if kind, ok := spaserr.KindOf(err); ok && kind == spaserr.KindUnsafeGitState && strings.Contains(err.Error(), "managed private HEAD mismatch") {
		return
	}
	if !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want private merge conflict", err)
	}

	if err := instance.Sync(ctx, SyncOptions{Abort: true}); err != nil {
		t.Fatalf("Sync(abort) error = %v", err)
	}
	content, readErr := os.ReadFile(obstructionPath)
	if readErr != nil || string(content) != "local private obstruction replacement\n" {
		t.Fatalf("restored obstruction path = %q, %v", content, readErr)
	}

	recoveryRoot := filepath.Join(instance.Store.DataDir, "recovery", state.LinkID)
	operations, readErr := os.ReadDir(recoveryRoot)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var recovered bool
	for _, operation := range operations {
		recoveryPath := filepath.Join(recoveryRoot, operation.Name(), "obstruction.txt")
		content, err := os.ReadFile(recoveryPath)
		if err != nil || string(content) != "untracked workspace original\n" {
			continue
		}
		info, err := os.Stat(recoveryPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o100 == 0 {
			t.Fatalf("recovery copy mode = %v, want executable bit", info.Mode())
		}
		recovered = true
		break
	}
	if !recovered {
		t.Fatalf("aborted merge did not retain approved obstruction under %s", recoveryRoot)
	}
}

func TestOwnershipOverrideAllowsApprovedAbsentPublicPath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	publicPath := filepath.Join(publicRoot, "shared.txt")
	if err := os.WriteFile(publicPath, []byte("public version\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, publicRoot, "add", "--", "shared.txt")
	runGit(t, publicRoot, "commit", "-q", "-m", "track public shared file")
	teammatePush(t, root, remote, map[string]string{
		"shared.txt": "private replacement\n",
	}, nil)

	if err := os.Remove(publicPath); err != nil {
		t.Fatal(err)
	}

	if err := instance.Sync(ctx, SyncOptions{
		Conflict:             ConflictOverride,
		DiscardPublicChanges: true,
		ExistingExclude:      ExcludePreserve,
		MergeProtection:      MergeSkip,
		Branch:               "main",
	}); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	content, err := os.ReadFile(publicPath)
	if err != nil || string(content) != "private replacement\n" {
		t.Fatalf("materialized shared path = %q, %v", content, err)
	}
	if status := gitOutput(t, publicRoot, "status", "--porcelain=v1"); !strings.Contains(status, "D  shared.txt") {
		t.Fatalf("public status = %q, want staged ownership removal", status)
	}
}

func TestOwnershipOverrideRejectsEditDuringPublicUntracking(t *testing.T) {
	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	publicPath := filepath.Join(publicRoot, "shared.txt")
	if err := os.WriteFile(publicPath, []byte("public version\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, publicRoot, "add", "--", "shared.txt")
	runGit(t, publicRoot, "commit", "-q", "-m", "track public shared file")
	teammatePush(t, root, remote, map[string]string{
		"shared.txt": "private replacement\n",
	}, nil)

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPAS_APP_GIT_PROXY", "edit-on-public-rm")
	t.Setenv("SPAS_APP_REAL_GIT", realGit)
	t.Setenv("SPAS_APP_EDIT_PATH", publicPath)
	t.Setenv("SPAS_APP_EDIT_CONTENT", "late public edit\n")
	instance.Git.Path = os.Args[0]
	err = instance.Sync(ctx, SyncOptions{
		Conflict:             ConflictOverride,
		DiscardPublicChanges: true,
		ExistingExclude:      ExcludePreserve,
		MergeProtection:      MergeSkip,
		Branch:               "main",
	})
	if err == nil || !strings.Contains(err.Error(), "changed after synchronization planning") {
		t.Fatalf("Sync() error = %v, want late-edit rejection", err)
	}
	content, readErr := os.ReadFile(publicPath)
	if readErr != nil || string(content) != "late public edit\n" {
		t.Fatalf("late public edit = %q, %v; want preserved", content, readErr)
	}
	state := loadState(t, instance, publicRoot)
	if state.Materializing == nil {
		t.Fatal("late-edit rejection cleared pending materialization")
	}
}

func TestPendingPushRecoveryIsIdempotent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, _, _ := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{".env": "TOKEN=one\n"})

	state := loadState(t, instance, publicRoot)
	private := instance.privateRepository(state)
	head, err := private.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state.Materializing = testMaterialization(
		t,
		publicRoot,
		linkstate.MaterializationPushPending,
		head,
		[]string{".env"},
		[]string{".env"},
		[]string{".env"},
		nil,
		nil,
	)
	saveState(t, instance, state)

	if err := instance.Sync(ctx, syncOptions("")); err != nil {
		t.Fatalf("resume pending push Sync() error = %v", err)
	}
	reloaded := loadState(t, instance, publicRoot)
	if reloaded.Materializing != nil {
		t.Fatalf("pending materialization was not cleared: %+v", reloaded.Materializing)
	}
}

func TestPendingPushRemoteAdvanceReturnsToNormalSync(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{".env": "TOKEN=one\n"})

	state := loadState(t, instance, publicRoot)
	private := instance.privateRepository(state)
	privateEnv, err := pathmodel.Parse(".env")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private.Path, ".env"), []byte("TOKEN=two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := private.Stage(ctx, []pathmodel.Path{privateEnv}); err != nil {
		t.Fatal(err)
	}
	if err := private.Commit(ctx, "pending local result"); err != nil {
		t.Fatal(err)
	}
	head, err := private.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state.Materializing = testMaterialization(
		t,
		publicRoot,
		linkstate.MaterializationPushPending,
		head,
		[]string{".env"},
		[]string{".env"},
		[]string{".env"},
		nil,
		nil,
	)
	saveState(t, instance, state)
	teammatePush(t, root, remote, map[string]string{"remote.txt": "remote\n"}, nil)

	err = instance.Sync(ctx, syncOptions(""))
	if err == nil || !strings.Contains(err.Error(), "remote advanced") {
		t.Fatalf("resume pending push error = %v, want remote-advance guidance", err)
	}
	reloaded := loadState(t, instance, publicRoot)
	if reloaded.Materializing != nil {
		t.Fatalf("stale pending push marker remains: %+v", reloaded.Materializing)
	}
	if content, err := os.ReadFile(filepath.Join(publicRoot, ".env")); err != nil || string(content) != "TOKEN=two\n" {
		t.Fatalf("pending private result was not restored before normal sync: %q, %v", content, err)
	}
	if err := instance.Sync(ctx, syncOptions("")); err != nil {
		t.Fatalf("normal sync after remote advance: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(publicRoot, "remote.txt")); err != nil || string(content) != "remote\n" {
		t.Fatalf("remote file after recovery = %q, %v", content, err)
	}
	if got := gitOutput(t, publicRoot, "--git-dir="+remote, "show", "main:.env"); got != "TOKEN=two\n" {
		t.Fatalf("private remote .env = %q, want pending local result", got)
	}
}

func TestStructuredNonFastForwardRetryIsBounded(t *testing.T) {
	ctx := context.Background()
	instance, publicRoot, root, _ := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{".env": "TOKEN=one\n"})
	if err := os.WriteFile(filepath.Join(publicRoot, ".env"), []byte("TOKEN=two\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	countPath := filepath.Join(root, "push-attempts")
	t.Setenv("SPAS_APP_GIT_PROXY", "fail-push-nff")
	t.Setenv("SPAS_APP_REAL_GIT", realGit)
	t.Setenv("SPAS_APP_PUSH_COUNT", countPath)
	instance.Git.Path = os.Args[0]

	err = instance.Sync(ctx, syncOptions("bounded retry"))
	if err == nil || !strings.Contains(err.Error(), "push private branch") {
		t.Fatalf("Sync() error = %v, want second structured non-fast-forward rejection", err)
	}
	count, readErr := os.ReadFile(countPath)
	if readErr != nil || strings.TrimSpace(string(count)) != "2" {
		t.Fatalf("push attempts = %q, %v; want exactly two attempts", count, readErr)
	}
	state := loadState(t, instance, publicRoot)
	if state.Materializing == nil {
		t.Fatal("second rejected push cleared pending materialization state")
	}
}

func TestExecutableModeOnlyChangeIsSynchronized(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose the Unix executable bit")
	}
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, _, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{"tool.sh": "#!/bin/sh\n"})
	path := filepath.Join(publicRoot, "tool.sh")
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(ctx, syncOptions("Make tool executable")); err != nil {
		t.Fatalf("Sync(mode change) error = %v", err)
	}
	tree := gitOutput(t, publicRoot, "--git-dir="+remote, "ls-tree", "main", "--", "tool.sh")
	if !strings.Contains(tree, "100755") {
		t.Fatalf("private tree entry = %q, want mode 100755", tree)
	}
}

func TestDiffDoesNotRunConfiguredExternalCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX executable script")
	}
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, _ := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{".env": "TOKEN=one\n"})
	if err := os.WriteFile(filepath.Join(publicRoot, ".env"), []byte("TOKEN=two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "external-diff")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch external-diff-ran\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, publicRoot, "config", "--local", "diff.external", script)

	if err := instance.Diff(ctx, DiffOptions{Stat: true}); err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(publicRoot, "external-diff-ran")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("configured external diff executed: %v", err)
	}
}

func TestInterruptedDeletionStopsUntilStaleWorkspaceCopyIsRemoved(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{
		"a.txt": "keep\n",
		"b.txt": "remove\n",
	})
	teammatePush(t, root, remote, nil, []string{"b.txt"})

	state := loadState(t, instance, publicRoot)
	private := instance.privateRepository(state)
	if err := private.Fetch(ctx, state.Private.Branch); err != nil {
		t.Fatal(err)
	}
	if _, err := private.MergeRemote(ctx, state.Private.Branch); err != nil {
		t.Fatal(err)
	}
	head, err := private.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state.Materializing = testMaterialization(
		t,
		publicRoot,
		linkstate.MaterializationPushed,
		head,
		[]string{"a.txt", "b.txt"},
		[]string{"a.txt"},
		[]string{"a.txt"},
		nil,
		nil,
	)
	saveState(t, instance, state)

	err = instance.Sync(ctx, syncOptions(""))
	if err == nil || !strings.Contains(err.Error(), "remove or move the stale workspace copy") {
		t.Fatalf("Sync() error = %v, want explicit stale-copy failure", err)
	}
	reloaded := loadState(t, instance, publicRoot)
	if reloaded.Materializing == nil {
		t.Fatal("failed recovery cleared its materialization marker")
	}
	bPath, err := pathmodel.Parse("b.txt")
	if err != nil {
		t.Fatal(err)
	}
	repository, _, err := instance.linked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if excluded, err := repository.IsEffectivelyExcluded(ctx, bPath); err != nil || !excluded {
		t.Fatalf("stale b.txt exclusion = %t, %v", excluded, err)
	}

	if err := os.Remove(filepath.Join(publicRoot, "b.txt")); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(ctx, syncOptions("")); err != nil {
		t.Fatalf("resume after removing stale copy: %v", err)
	}
}

func TestInterruptedOverrideResumesWhenWorkspaceMatchesPrivate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, _, _ := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{".env": "PRIVATE=one\n"})
	runGit(t, publicRoot, "add", "-f", ".env")
	runGit(t, publicRoot, "commit", "-q", "-m", "publicly track env")

	state := loadState(t, instance, publicRoot)
	private := instance.privateRepository(state)
	head, err := private.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state.Materializing = testMaterialization(
		t,
		publicRoot,
		linkstate.MaterializationPushed,
		head,
		[]string{".env"},
		[]string{".env"},
		[]string{".env"},
		[]string{".env"},
		[]string{".env"},
	)
	saveState(t, instance, state)

	err = instance.Sync(ctx, syncOptions(""))
	if err == nil || !strings.Contains(err.Error(), "requires manual public untracking") {
		t.Fatalf("Sync(public override) error = %v", err)
	}
	runGit(t, publicRoot, "rm", "--cached", "--", ".env")
	if err := instance.Sync(ctx, syncOptions("")); err != nil {
		t.Fatalf("Sync(matching interrupted override) error = %v", err)
	}
	reloaded := loadState(t, instance, publicRoot)
	if reloaded.Materializing != nil {
		t.Fatal("completed override recovery retained its materialization marker")
	}
	content, err := os.ReadFile(filepath.Join(publicRoot, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "PRIVATE=one\n" {
		t.Fatalf("materialized .env = %q", content)
	}
}

func TestInterruptedOverrideRejectsChangedWorkspaceCopy(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, _, _ := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{".env": "PRIVATE=one\n"})
	runGit(t, publicRoot, "add", "-f", ".env")
	runGit(t, publicRoot, "commit", "-q", "-m", "publicly track env")

	state := loadState(t, instance, publicRoot)
	private := instance.privateRepository(state)
	head, err := private.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state.Materializing = testMaterialization(
		t,
		publicRoot,
		linkstate.MaterializationPushed,
		head,
		[]string{".env"},
		[]string{".env"},
		[]string{".env"},
		[]string{".env"},
		[]string{".env"},
	)
	saveState(t, instance, state)
	runGit(t, publicRoot, "rm", "--cached", "--", ".env")
	if err := os.WriteFile(filepath.Join(publicRoot, ".env"), []byte("PRIVATE=changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = instance.Sync(ctx, syncOptions(""))
	if err == nil || !strings.Contains(err.Error(), "changed after synchronization planning") {
		t.Fatalf("Sync(changed interrupted override) error = %v, want snapshot rejection", err)
	}
	reloaded := loadState(t, instance, publicRoot)
	if reloaded.Materializing == nil {
		t.Fatal("unsafe override recovery cleared its marker")
	}
}

func TestJSONCommitApprovalFailureWritesNoProse(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, _, _ := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{".env": "PRIVATE=one\n"})
	if err := os.WriteFile(filepath.Join(publicRoot, ".env"), []byte("PRIVATE=two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	instance.JSON = true
	instance.Out = &output

	err := instance.Sync(ctx, syncOptions(""))
	if err == nil || !strings.Contains(err.Error(), "provide --message") {
		t.Fatalf("Sync(JSON without approval) error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("Sync(JSON without approval) wrote prose: %q", output.String())
	}
}

// F23: a case-only conflict with exactly one public and one private spelling
// can be overridden: the public spelling is removed (staged, uncommitted) and
// the private spelling is materialized.
func TestCaseOnlyOverride(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)

	// Requires a case-insensitive workspace filesystem.
	probe := filepath.Join(publicRoot, "case-probe-x")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, statErr := os.Stat(filepath.Join(publicRoot, "CASE-PROBE-X"))
	_ = os.Remove(probe)
	if statErr != nil {
		t.Skip("workspace filesystem is case-sensitive")
	}

	enroll(t, instance, publicRoot, map[string]string{".env": "TOKEN=env\n"})
	teammatePush(t, root, remote, map[string]string{"docs/ARCHITECTURE.md": "private design\n"}, nil)

	docs := filepath.Join(publicRoot, "docs", "architecture.md")
	if err := os.MkdirAll(filepath.Dir(docs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(docs, []byte("public design\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, publicRoot, "add", "docs/architecture.md")
	runGit(t, publicRoot, "commit", "-q", "-m", "public docs")
	headBefore := gitOutput(t, publicRoot, "rev-parse", "HEAD")

	options := syncOptions("")
	options.Conflict = ConflictOverride
	if err := instance.Sync(ctx, options); err != nil {
		t.Fatalf("Sync(case override) error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(publicRoot, "docs", "ARCHITECTURE.md"))
	if err != nil || string(content) != "private design\n" {
		t.Fatalf("materialized file = %q, %v; want the private version", content, err)
	}
	staged := gitOutput(t, publicRoot, "diff", "--cached", "--name-status")
	if !strings.Contains(staged, "architecture.md") {
		t.Fatalf("staged public changes = %q, want the staged deletion", staged)
	}
	if headAfter := gitOutput(t, publicRoot, "rev-parse", "HEAD"); headAfter != headBefore {
		t.Fatal("a public commit was created")
	}
	block := readExcludeBlock(t, publicRoot)
	if !strings.Contains(block, "/docs/ARCHITECTURE.md") {
		t.Fatalf("exclude block = %q, want the private spelling excluded", block)
	}
}

// F12: names a public repository may legally track (Windows-reserved,
// colon-bearing) must never make SPAS unusable.
func TestPublicTrackedNonPortableNamesDoNotBreakCommands(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, _, _ := fixture(t)
	blob := strings.TrimSpace(gitOutputStdin(t, publicRoot, "content\n", "hash-object", "-w", "--stdin"))
	runGit(t, publicRoot, "update-index", "--add", "--cacheinfo", "100644,"+blob+",src/aux.c")
	runGit(t, publicRoot, "update-index", "--add", "--cacheinfo", "100644,"+blob+",notes:2026.md")
	runGit(t, publicRoot, "commit", "-q", "-m", "non-portable names")

	enroll(t, instance, publicRoot, map[string]string{".env": "TOKEN=env\n"})
	if err := instance.Status(ctx, StatusOptions{}); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
}

// F22 (non-interactive form): the tracked-path error explains the required
// ownership change without constructing a shell command from the path.
func TestAddTrackedPathExplainsOwnershipConflict(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, _, _, _ := fixture(t)
	err := instance.Add(ctx, AddOptions{
		Paths:           []string{"README.md"},
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if err == nil || !strings.Contains(err.Error(), "remove that path from public tracking") {
		t.Fatalf("Add(tracked) error = %v, want ownership guidance", err)
	}
}

func gitOutputStdin(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	result, err := (gitexec.Runner{}).RunInput(context.Background(), dir, strings.NewReader(stdin), args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(result.Stdout)
}

func TestAddIsIdempotentForManagedPathAndCancelsPendingRemoval(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, _, _ := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{".env": "TOKEN=secret\n"})

	var output bytes.Buffer
	instance.Out = &output
	instance.Err = &output
	instance.JSON = true
	if err := instance.Add(ctx, AddOptions{
		Paths:           []string{".env"},
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	}); err != nil {
		t.Fatalf("Add(already managed) error = %v", err)
	}
	state := loadState(t, instance, publicRoot)
	if len(state.PendingAdds) != 0 {
		t.Fatalf("PendingAdds = %v, want none for an already managed path", state.PendingAdds)
	}
	var first struct {
		Added []string `json:"added"`
	}
	if err := json.Unmarshal(output.Bytes(), &first); err != nil {
		t.Fatalf("decode Add output: %v\n%s", err, output.String())
	}
	if len(first.Added) != 0 {
		t.Fatalf("added = %v, want none for an already managed path", first.Added)
	}

	if err := instance.Remove(ctx, RemoveOptions{Paths: []string{".env"}}); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	state = loadState(t, instance, publicRoot)
	if len(state.PendingRemoves) != 1 {
		t.Fatalf("PendingRemoves = %v, want .env", state.PendingRemoves)
	}

	output.Reset()
	if err := instance.Add(ctx, AddOptions{
		Paths:           []string{".env"},
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	}); err != nil {
		t.Fatalf("Add(cancel removal) error = %v", err)
	}
	state = loadState(t, instance, publicRoot)
	if len(state.PendingAdds) != 0 || len(state.PendingRemoves) != 0 {
		t.Fatalf("pending state = additions %v, removals %v; want neither", state.PendingAdds, state.PendingRemoves)
	}
	var second struct {
		CanceledRemovals []string `json:"canceledRemovals"`
	}
	if err := json.Unmarshal(output.Bytes(), &second); err != nil {
		t.Fatalf("decode Add cancellation output: %v\n%s", err, output.String())
	}
	if strings.Join(second.CanceledRemovals, ",") != ".env" {
		t.Fatalf("canceledRemovals = %v, want .env", second.CanceledRemovals)
	}
}

func TestAddAndRemoveRevalidateEveryManagedExclusion(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, _, _ := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{
		".env":     "TOKEN=secret\n",
		"notes.md": "private notes\n",
	})

	// A committed .gitignore rule has higher precedence than info/exclude.
	// Re-including an existing managed path must make every later exclusion-
	// modifying operation fail, not only operations that mention that path.
	if err := os.WriteFile(filepath.Join(publicRoot, ".gitignore"), []byte("!.env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(publicRoot, "new-private.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	addErr := instance.Add(ctx, AddOptions{
		Paths:           []string{"new-private.txt"},
		ExistingExclude: ExcludePreserve,
		MergeProtection: MergeSkip,
	})
	if addErr == nil || !strings.Contains(addErr.Error(), ".env") || !strings.Contains(addErr.Error(), "not effectively excluded") {
		t.Fatalf("Add() error = %v, want existing managed-path exclusion failure", addErr)
	}
	state := loadState(t, instance, publicRoot)
	if len(state.PendingAdds) != 0 {
		t.Fatalf("PendingAdds = %v after failed Add, want none", state.PendingAdds)
	}

	removeErr := instance.Remove(ctx, RemoveOptions{Paths: []string{"notes.md"}})
	if removeErr == nil || !strings.Contains(removeErr.Error(), ".env") || !strings.Contains(removeErr.Error(), "not effectively excluded") {
		t.Fatalf("Remove() error = %v, want existing managed-path exclusion failure", removeErr)
	}
	state = loadState(t, instance, publicRoot)
	if len(state.PendingRemoves) != 0 {
		t.Fatalf("PendingRemoves = %v after failed Remove, want none", state.PendingRemoves)
	}
}

// F24: a case-only ownership override must survive an unrelated private merge
// conflict. Active merge state stores the public spelling, while continuation
// derives and materializes the private spelling from the private index.
func TestCaseOnlyOverrideSurvivesPrivateMergeContinuation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	runGit(t, publicRoot, "config", "core.ignoreCase", "true")

	enroll(t, instance, publicRoot, map[string]string{"conflict.txt": "base\n"})

	publicDocs := filepath.Join(publicRoot, "docs", "architecture.md")
	if err := os.MkdirAll(filepath.Dir(publicDocs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicDocs, []byte("public design\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, publicRoot, "add", "docs/architecture.md")
	runGit(t, publicRoot, "commit", "-q", "-m", "public architecture")

	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("local change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	teammatePush(t, root, remote, map[string]string{
		"conflict.txt":         "remote change\n",
		"docs/ARCHITECTURE.md": "private design\n",
	}, nil)

	options := syncOptions("local conflicting change")
	options.Conflict = ConflictOverride
	err := instance.Sync(ctx, options)
	if !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want private merge conflict", err)
	}
	if err := os.WriteFile(publicDocs, []byte("public design after approval\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, publicRoot, "add", "docs/architecture.md")
	runGit(t, publicRoot, "commit", "-q", "-m", "update public architecture while resolving private merge")
	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("resolved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	continueOptions := SyncOptions{
		Continue: true,
		Message:  "resolve private conflict",
	}
	err = instance.Sync(ctx, continueOptions)
	if !errors.Is(err, interaction.ErrDecisionRequired) || !strings.Contains(err.Error(), "changed after ownership override approval") {
		t.Fatalf("Sync(--continue) error = %v, want renewed ownership-override approval", err)
	}
	if content, readErr := os.ReadFile(publicDocs); readErr != nil || string(content) != "public design after approval\n" {
		t.Fatalf("public override content = %q, %v; want clean post-approval edit preserved", content, readErr)
	}
	continueOptions.DiscardPublicChanges = true
	if err := instance.Sync(ctx, continueOptions); err != nil {
		t.Fatalf("Sync(--continue) error = %v", err)
	}

	privateDocs := filepath.Join(publicRoot, "docs", "ARCHITECTURE.md")
	content, err := os.ReadFile(privateDocs)
	if err != nil || string(content) != "private design\n" {
		t.Fatalf("materialized private spelling = %q, %v", content, err)
	}
	staged := gitOutput(t, publicRoot, "diff", "--cached", "--name-status")
	if !strings.Contains(staged, "docs/architecture.md") {
		t.Fatalf("public staged changes = %q, want lowercase public deletion", staged)
	}
	if got := gitOutput(t, root, "--git-dir="+remote, "show", "main:docs/ARCHITECTURE.md"); got != "private design\n" {
		t.Fatalf("private remote architecture = %q", got)
	}
}

// F27: if the developer explicitly commits the public ownership removal while
// resolving an unrelated private merge, continuation must not run git rm on a
// path the public index no longer owns. The approved private replacement is
// still materialized and excluded locally.
func TestOverrideContinuationAcceptsAlreadyUntrackedPublicPath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	runGit(t, publicRoot, "config", "core.ignoreCase", "true")
	enroll(t, instance, publicRoot, map[string]string{"conflict.txt": "base\n"})

	publicDocs := filepath.Join(publicRoot, "docs", "architecture.md")
	if err := os.MkdirAll(filepath.Dir(publicDocs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicDocs, []byte("public design\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, publicRoot, "add", "docs/architecture.md")
	runGit(t, publicRoot, "commit", "-q", "-m", "public architecture")

	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("local change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	teammatePush(t, root, remote, map[string]string{
		"conflict.txt":         "remote change\n",
		"docs/ARCHITECTURE.md": "private design\n",
	}, nil)

	options := syncOptions("local conflicting change")
	options.Conflict = ConflictOverride
	if err := instance.Sync(ctx, options); !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want private merge conflict", err)
	}

	// Complete the public ownership transfer manually while resolving the
	// private conflict. The continuation must preserve this explicit public
	// commit instead of trying to remove the already-untracked path again.
	runGit(t, publicRoot, "rm", "-q", "--", "docs/architecture.md")
	runGit(t, publicRoot, "commit", "-q", "-m", "stop tracking private architecture")
	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("resolved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(ctx, SyncOptions{
		Continue: true,
		Message:  "resolve private conflict",
	}); err != nil {
		t.Fatalf("Sync(--continue) error = %v", err)
	}

	privateDocs := filepath.Join(publicRoot, "docs", "ARCHITECTURE.md")
	content, err := os.ReadFile(privateDocs)
	if err != nil || string(content) != "private design\n" {
		t.Fatalf("materialized private architecture = %q, %v", content, err)
	}
	if status := gitOutput(t, publicRoot, "status", "--short"); status != "" {
		t.Fatalf("public status = %q, want clean after the committed public deletion", status)
	}
	if got := gitOutput(t, root, "--git-dir="+remote, "show", "main:docs/ARCHITECTURE.md"); got != "private design\n" {
		t.Fatalf("private remote architecture = %q", got)
	}
}

func TestOverrideContinuationAcceptsUnchangedApprovedDirtyStatus(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instance, publicRoot, root, remote := fixture(t)
	enroll(t, instance, publicRoot, map[string]string{"conflict.txt": "base\n"})

	publicDocs := filepath.Join(publicRoot, "docs", "architecture.md")
	if err := os.MkdirAll(filepath.Dir(publicDocs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicDocs, []byte("public committed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, publicRoot, "add", "docs/architecture.md")
	runGit(t, publicRoot, "commit", "-q", "-m", "public architecture")
	if err := os.WriteFile(publicDocs, []byte("approved dirty bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("local change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	teammatePush(t, root, remote, map[string]string{
		"conflict.txt":         "remote change\n",
		"docs/architecture.md": "private design\n",
	}, nil)

	options := syncOptions("local conflicting change")
	options.Conflict = ConflictOverride
	options.DiscardPublicChanges = true
	if err := instance.Sync(ctx, options); !errors.Is(err, ErrPrivateMergeConflict) {
		t.Fatalf("Sync() error = %v, want private merge conflict", err)
	}
	if err := os.WriteFile(filepath.Join(publicRoot, "conflict.txt"), []byte("resolved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(ctx, SyncOptions{
		Continue: true,
		Message:  "resolve private conflict",
	}); err != nil {
		t.Fatalf("Sync(--continue) error = %v; unchanged approved dirty status must not need a second approval", err)
	}
	content, err := os.ReadFile(publicDocs)
	if err != nil || string(content) != "private design\n" {
		t.Fatalf("materialized private architecture = %q, %v", content, err)
	}
}

// F25: recovery state and Git merge metadata must agree. If somebody cleans or
// aborts the SPAS-managed private merge out of band, normal sync must not read
// conflict-marker workspace files as fresh private edits.
func TestSyncRejectsActiveMergeStateWithoutGitMerge(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	publicRoot, _, instance := initializedApp(t, root)
	state := loadState(t, instance, publicRoot)
	state.ActiveMerge = validTestActiveMerge()
	state.Private.ExpectedHead = state.ActiveMerge.PreMergeHead
	saveState(t, instance, state)

	err := instance.Sync(ctx, syncOptions("must not commit stale recovery files"))
	if err == nil || !strings.Contains(err.Error(), "recovery state exists") {
		t.Fatalf("Sync() error = %v, want stale merge-recovery rejection", err)
	}
}

// F26: private merge-conflict files copied into the public workspace are part
// of unlink's removal/reporting set even when the remote introduced them and
// they never reached ManagedPaths.
func TestUnlinkWorkspacePathsIncludesActiveMergeConflicts(t *testing.T) {
	t.Parallel()

	state := linkstate.State{
		ManagedPaths: []string{"managed.txt"},
		ActiveMerge: &linkstate.ActiveMerge{
			ConflictPaths: []string{"remote/new-conflict.txt"},
		},
	}
	paths, err := unlinkWorkspacePaths(state)
	if err != nil {
		t.Fatal(err)
	}
	got := pathsToStrings(paths)
	want := []string{"managed.txt", "remote/new-conflict.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unlinkWorkspacePaths() = %v, want %v", got, want)
	}
}
