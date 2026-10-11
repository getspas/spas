package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/getspas/spas/internal/interaction"
	"github.com/getspas/spas/internal/linkstate"
	"github.com/getspas/spas/internal/lock"
	"github.com/getspas/spas/internal/spaserr"
)

func TestUnlinkRequiresSeparateUnsynchronizedDiscardApproval(t *testing.T) {
	t.Parallel()
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "force"}[force], func(t *testing.T) {
			workspace, _, instance := initializedApp(t, t.TempDir())
			path := filepath.Join(workspace, "docs", "ARCHITECTURE.md")
			if err := os.WriteFile(path, []byte("unsynchronized work\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			instance.Prompt.AssumeYes = true
			err := instance.Unlink(context.Background(), UnlinkOptions{Force: force, RemoveFiles: true, ApproveRemoveFiles: true})
			if !errors.Is(err, interaction.ErrDecisionRequired) || !strings.Contains(err.Error(), "docs/ARCHITECTURE.md") || !strings.Contains(err.Error(), "--discard-unsynced-changes") {
				t.Fatalf("Unlink = %v, want named separate discard decision", err)
			}
			if content, err := os.ReadFile(path); err != nil || string(content) != "unsynchronized work\n" {
				t.Fatalf("workspace file = %q, %v", content, err)
			}
			if _, _, err := instance.linked(context.Background()); err != nil {
				t.Fatalf("link lost: %v", err)
			}
		})
	}
}

func TestUnlinkRequiresDiscardForPendingAdditionMatchingPreviouslySkippedRemote(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace := initializePublicRepository(t, root)
	remote := initializePrivateRemoteWithFile(t, root, ".env", "remote contents\n")
	path := filepath.Join(workspace, ".env")
	if err := os.WriteFile(path, []byte("unenrolled local file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	instance, _ := testApp(t, workspace, root, remote)
	if err := instance.Link(t.Context(), LinkOptions{Repository: "getspas/private-files"}); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(t.Context(), SyncOptions{Conflict: ConflictSkip, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("remote contents\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Add(t.Context(), AddOptions{Paths: []string{".env"}, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip}); err != nil {
		t.Fatal(err)
	}
	err := instance.Unlink(t.Context(), UnlinkOptions{Force: true, RemoveFiles: true, ApproveRemoveFiles: true})
	if !errors.Is(err, interaction.ErrDecisionRequired) || !strings.Contains(err.Error(), ".env") {
		t.Fatalf("Unlink = %v, want pending-addition discard approval", err)
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "remote contents\n" {
		t.Fatalf("pending addition = %q, %v", content, err)
	}
}

func TestUnlinkUnsynchronizedApprovalRoutes(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"explicit", "interactive", "both-prompts", "declined", "mode-only", "pending-addition", "uninitialized-addition"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "mode-only" && runtime.GOOS == "windows" {
				t.Skip("Windows does not support executable permission changes")
			}
			root := t.TempDir()
			workspace, remote, instance := initializedApp(t, root)
			path := filepath.Join(workspace, "docs", "ARCHITECTURE.md")
			options := UnlinkOptions{RemoveFiles: true, ApproveRemoveFiles: true, DiscardUnsyncedChanges: true}
			if mode == "uninitialized-addition" {
				if err := instance.Unlink(t.Context(), UnlinkOptions{}); err != nil {
					t.Fatal(err)
				}
				if err := instance.Link(t.Context(), LinkOptions{Repository: "getspas/private-files", Branch: "main"}); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "mode-only":
				if err := os.Chmod(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "pending-addition", "uninitialized-addition":
				path = filepath.Join(workspace, "new.txt")
				if err := os.WriteFile(path, []byte("new work\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := instance.Add(t.Context(), AddOptions{Paths: []string{"new.txt"}, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip}); err != nil {
					t.Fatal(err)
				}
				options.Force = true
			default:
				if err := os.WriteFile(path, []byte("new work\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			beforeRemote := gitOutput(t, root, "--git-dir="+remote, "rev-parse", "main")
			var prompts bytes.Buffer
			if mode != "explicit" {
				options.DiscardUnsyncedChanges = false
				response := "y\n"
				if mode == "both-prompts" {
					options.ApproveRemoveFiles = false
					response = "y\ny\n"
				}
				if mode == "declined" {
					response = "n\n"
				}
				instance.Prompt = interaction.Prompter{In: strings.NewReader(response), Out: &prompts, Interactive: true, AssumeYes: true}
			}
			err := instance.Unlink(t.Context(), options)
			if mode == "declined" {
				if err == nil || !strings.Contains(err.Error(), "discard declined") {
					t.Fatalf("Unlink = %v", err)
				}
				if _, err := os.Stat(path); err != nil {
					t.Fatal(err)
				}
				if _, _, err := instance.linked(t.Context()); err != nil {
					t.Fatal(err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("cleanup target remains: %v", err)
				}
			}
			if mode != "explicit" && (!strings.Contains(prompts.String(), filepath.ToSlash(strings.TrimPrefix(path, workspace+string(os.PathSeparator)))) || !strings.Contains(prompts.String(), "Discard unsynchronized")) {
				t.Fatalf("discard prompt = %s", &prompts)
			}
			if got := gitOutput(t, root, "--git-dir="+remote, "rev-parse", "main"); got != beforeRemote {
				t.Fatal("unlink changed remote")
			}
		})
	}
}

func TestUnlinkPreflightRefusesAllCleanup(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"tracked", "malformed", "symlink", "directory", "symlink-parent"} {
		t.Run(mode, func(t *testing.T) {
			workspace, _, instance := initializedApp(t, t.TempDir())
			path := filepath.Join(workspace, "docs", "ARCHITECTURE.md")
			repository, state, err := instance.linked(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			excludePath, err := repository.InfoExcludePath(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "tracked":
				runGit(t, workspace, "add", "-f", "docs/ARCHITECTURE.md")
			case "malformed":
				if err := os.WriteFile(excludePath, []byte("# BEGIN SPAS link="+state.LinkID+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink", "directory":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if mode == "symlink" {
					if err := os.Symlink(filepath.Join(workspace, "README.md"), path); err != nil {
						t.Skipf("symlink unavailable: %v", err)
					}
				} else if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink-parent":
				if err := os.Rename(filepath.Join(workspace, "docs"), filepath.Join(workspace, "saved-docs")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(workspace, "saved-docs"), filepath.Join(workspace, "docs")); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}
			before, err := os.ReadFile(excludePath)
			if err != nil {
				t.Fatal(err)
			}
			err = instance.Unlink(t.Context(), UnlinkOptions{Force: true, RemoveFiles: true, ApproveRemoveFiles: true, DiscardUnsyncedChanges: true})
			if err == nil {
				t.Fatal("unsafe cleanup succeeded")
			}
			if mode == "tracked" {
				if kind, _ := spaserr.KindOf(err); kind != spaserr.KindPathConflict || !strings.Contains(err.Error(), "docs/ARCHITECTURE.md") {
					t.Fatalf("ownership error: %v", err)
				}
			}
			if mode == "malformed" {
				if kind, _ := spaserr.KindOf(err); kind != spaserr.KindExclusionValidation {
					t.Fatalf("exclude error: %v", err)
				}
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("target deleted: %v", err)
			}
			if after, err := os.ReadFile(excludePath); err != nil || !bytes.Equal(before, after) {
				t.Fatalf("exclusions changed: %v", err)
			}
			if _, _, err := instance.linked(t.Context()); err != nil {
				t.Fatalf("link lost: %v", err)
			}
		})
	}
}

func TestUnlinkRejectsChangesAfterApproval(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"contents", "discard-contents", "executable", "symlink", "directory", "tracked", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "executable" && runtime.GOOS == "windows" {
				t.Skip("executable permissions unavailable")
			}
			workspace, _, instance := initializedApp(t, t.TempDir())
			path := filepath.Join(workspace, "docs", "ARCHITECTURE.md")
			repository, state, err := instance.linked(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if mode == "discard-contents" {
				if err := os.WriteFile(path, []byte("approved edit\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			reader := &mutateBeforeFinalByteReader{data: []byte("y\n"), mutate: func() error {
				switch mode {
				case "contents", "discard-contents":
					return os.WriteFile(path, []byte("later edit\n"), 0o600)
				case "executable":
					return os.Chmod(path, 0o700)
				case "tracked":
					runGit(t, workspace, "add", "-f", "docs/ARCHITECTURE.md")
					return nil
				case "malformed":
					excludePath, err := repository.InfoExcludePath(t.Context())
					if err != nil {
						return err
					}
					return os.WriteFile(excludePath, []byte("# END SPAS link="+state.LinkID+"\n"), 0o600)
				default:
					if err := os.Remove(path); err != nil {
						return err
					}
					if mode == "directory" {
						return os.Mkdir(path, 0o700)
					}
					return os.Symlink(filepath.Join(workspace, "README.md"), path)
				}
			}}
			var prompts bytes.Buffer
			instance.Prompt = interaction.Prompter{In: reader, Out: &prompts, Interactive: true}
			err = instance.Unlink(t.Context(), UnlinkOptions{RemoveFiles: true, ApproveRemoveFiles: mode == "discard-contents"})
			if reader.mutateErr != nil {
				if mode == "symlink" {
					t.Skipf("symlink unavailable: %v", reader.mutateErr)
				}
				t.Fatalf("mutation failed: %v", reader.mutateErr)
			}
			if err == nil {
				t.Fatalf("cleanup accepted later %s change", mode)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("changed file deleted: %v", err)
			}
			if _, _, err := instance.linked(t.Context()); err != nil {
				t.Fatalf("link lost: %v", err)
			}
		})
	}
}

func TestUnlinkPartialFileCleanupCanRetry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix directory permissions")
	}
	t.Parallel()
	workspace, _, instance := initializedApp(t, t.TempDir())
	parent := filepath.Join(workspace, "z-last")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "asset.txt")
	if err := os.WriteFile(path, []byte("pending work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Add(t.Context(), AddOptions{Paths: []string{"z-last/asset.txt"}, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	// Check the effective permission boundary so privileged hosts do not make
	// the intended failure disappear.
	probe := filepath.Join(parent, "probe")
	if file, err := os.Create(probe); err == nil {
		_ = file.Close()
		_ = os.Remove(probe)
		t.Skip("directory permissions do not prevent writes on this host")
	}
	options := UnlinkOptions{Force: true, RemoveFiles: true, ApproveRemoveFiles: true, DiscardUnsyncedChanges: true}
	if err := instance.Unlink(t.Context(), options); err == nil || !strings.Contains(err.Error(), "asset.txt") {
		t.Fatalf("Unlink = %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "docs", "ARCHITECTURE.md")); !os.IsNotExist(err) {
		t.Fatalf("earlier target was not removed: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("failed target lost: %v", err)
	}
	if _, _, err := instance.linked(t.Context()); err != nil {
		t.Fatalf("link lost: %v", err)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := instance.Unlink(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("retry left target: %v", err)
	}
}

func TestUnlinkCleanupCanRetryAfterMetadataFailure(t *testing.T) {
	t.Parallel()
	workspace, _, instance := initializedApp(t, t.TempDir())
	repository, _, err := instance.linked(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	excludePath, err := repository.InfoExcludePath(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	excludeLock, err := lock.AcquirePath(excludePath+".spas.lock", "test exclude lock")
	if err != nil {
		t.Fatal(err)
	}
	defer excludeLock.Release()
	options := UnlinkOptions{RemoveFiles: true, ApproveRemoveFiles: true}
	err = instance.Unlink(t.Context(), options)
	if kind, _ := spaserr.KindOf(err); kind != spaserr.KindLockHeld {
		t.Fatalf("Unlink = %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "docs", "ARCHITECTURE.md")); !os.IsNotExist(err) {
		t.Fatalf("cleanup did not occur: %v", err)
	}
	if _, _, err := instance.linked(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := excludeLock.Release(); err != nil {
		t.Fatal(err)
	}
	if err := instance.Unlink(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if _, _, err := instance.linked(t.Context()); !errors.Is(err, linkstate.ErrNotLinked) {
		t.Fatalf("retry did not unlink: %v", err)
	}
}

func TestUnlinkPendingPushRequiresDiscardAgainstLastCompletedSync(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace, remote, instance := initializedApp(t, root)
	if err := os.WriteFile(filepath.Join(remote, "hooks", "pre-receive"), []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, "new.txt")
	if err := os.WriteFile(path, []byte("unpublished addition\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Add(t.Context(), AddOptions{Paths: []string{"new.txt"}, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip}); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(t.Context(), SyncOptions{Message: "Add asset", Conflict: ConflictAbort, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip}); err == nil || !strings.Contains(err.Error(), "push private branch") {
		t.Fatalf("Sync = %v", err)
	}
	_, state, err := instance.linked(t.Context())
	if err != nil || state.Materializing == nil {
		t.Fatalf("recovery state: %+v, %v", state.Materializing, err)
	}
	options := UnlinkOptions{Force: true, RemoveFiles: true, ApproveRemoveFiles: true}
	err = instance.Unlink(t.Context(), options)
	if !errors.Is(err, interaction.ErrDecisionRequired) || !strings.Contains(err.Error(), "new.txt") {
		t.Fatalf("Unlink = %v", err)
	}
	options.DiscardUnsyncedChanges = true
	if err := instance.Unlink(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("recovery addition remains: %v", err)
	}
}

func TestUnlinkPreservesSkippedUnmanagedFileDuringPushRecovery(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace := initializePublicRepository(t, root)
	remote := initializePrivateRemoteWithFile(t, root, ".env", "remote private\n")
	obstruction := filepath.Join(workspace, ".env")
	if err := os.WriteFile(obstruction, []byte("unmanaged local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "hooks", "pre-receive"), []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	instance, _ := testApp(t, workspace, root, remote)
	if err := instance.Link(t.Context(), LinkOptions{Repository: "getspas/private-files"}); err != nil {
		t.Fatal(err)
	}
	enrolled := filepath.Join(workspace, "new.txt")
	if err := os.WriteFile(enrolled, []byte("enrolled work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Add(t.Context(), AddOptions{Paths: []string{"new.txt"}, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip}); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(t.Context(), SyncOptions{Message: "Add asset", Conflict: ConflictSkip, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip}); err == nil || !strings.Contains(err.Error(), "push private branch") {
		t.Fatalf("Sync = %v", err)
	}
	if err := instance.Unlink(t.Context(), UnlinkOptions{Force: true, RemoveFiles: true, ApproveRemoveFiles: true, DiscardUnsyncedChanges: true}); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(obstruction); err != nil || string(content) != "unmanaged local\n" {
		t.Fatalf("skipped file = %q, %v", content, err)
	}
	if _, err := os.Stat(enrolled); !os.IsNotExist(err) {
		t.Fatalf("enrolled file remains: %v", err)
	}
}
