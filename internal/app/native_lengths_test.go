package app

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/getspas/spas/internal/linkstate"
)

// Git for Windows needs its own long-path opt-in in addition to native
// filesystem support. This config is scoped to the test process and its Git
// children; no user or system configuration is changed.
func configureNativeLongPaths(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		return
	}
	config := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(config, []byte("[core]\nlongpaths = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", config)
}

func TestNativeLongPathsLifecycle(t *testing.T) {
	configureNativeLongPaths(t)
	for _, location := range []string{"asset", "workspace", "checkout"} {
		t.Run(location, func(t *testing.T) {
			root := t.TempDir()
			workspaceParent := root
			if location == "workspace" {
				workspaceParent = filepath.Join(root, strings.Repeat("w", 100), strings.Repeat("x", 100), strings.Repeat("y", 100))
				if err := os.MkdirAll(workspaceParent, 0o700); err != nil {
					t.Skipf("native filesystem cannot create long workspace root: %v", err)
				}
			}
			workspace := initializePublicRepository(t, workspaceParent)
			remote := filepath.Join(root, "assets.git")
			runGit(t, root, "init", "--bare", "-q", remote)
			instance, _ := testApp(t, workspace, root, remote)
			dataDir := instance.Store.DataDir
			if location == "checkout" {
				dataDir = filepath.Join(root, strings.Repeat("d", 100), strings.Repeat("e", 100), strings.Repeat("f", 100))
				if err := os.MkdirAll(dataDir, 0o700); err != nil {
					t.Skipf("native filesystem cannot create long checkout storage: %v", err)
				}
				instance.Store.DataDir = dataDir
			}
			if err := instance.Link(t.Context(), LinkOptions{Repository: "getspas/private-files", Branch: "main"}); err != nil {
				t.Fatal(err)
			}
			name := "personal/asset.bin"
			if location == "asset" {
				name = strings.Repeat("a", 100) + "/" + strings.Repeat("b", 100) + "/" + strings.Repeat("c", 100) + "/asset.bin"
			}
			target := filepath.Join(workspace, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Skipf("native filesystem cannot create asset parents: %v", err)
			}
			content := []byte("personal\x00\xff\n")
			if err := os.WriteFile(target, content, 0o600); err != nil {
				t.Skipf("native filesystem cannot create asset: %v", err)
			}
			// Directory enrollment must follow the same native policy as a single file.
			if err := instance.Add(t.Context(), AddOptions{Paths: []string{filepath.Dir(name)}, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip}); err != nil {
				t.Fatal(err)
			}
			reload := func() App {
				app, _ := testApp(t, workspace, root, remote)
				app.Store.DataDir = dataDir
				return app
			}
			instance = reload()
			if got := loadState(t, instance, workspace).PendingAdds; !slices.Equal(got, []string{name}) {
				t.Fatalf("reloaded enrollment = %q, want %q", got, name)
			}
			if err := instance.Sync(t.Context(), syncOptions("Store asset")); err != nil {
				t.Fatal(err)
			}
			if got := gitOutput(t, root, "--git-dir="+remote, "show", "main:"+name); got != string(content) {
				t.Fatalf("stored asset = %q, want %q", got, content)
			}
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
			instance = reload()
			if err := instance.Sync(t.Context(), syncOptions("")); err != nil {
				t.Fatalf("restore missing asset: %v", err)
			}
			if got, err := os.ReadFile(target); err != nil || !bytes.Equal(got, content) {
				t.Fatalf("restored asset = %q, %v", got, err)
			}
			teammatePush(t, root, remote, map[string]string{name: "remote\x00\xff\n"}, nil)
			if err := instance.Sync(t.Context(), syncOptions("")); err != nil {
				t.Fatalf("receive incoming asset: %v", err)
			}
			if got, err := os.ReadFile(target); err != nil || string(got) != "remote\x00\xff\n" {
				t.Fatalf("incoming asset = %q, %v", got, err)
			}
			if err := instance.Diff(t.Context(), DiffOptions{Paths: []string{name}}); err != nil {
				t.Fatal(err)
			}
			if err := instance.Remove(t.Context(), RemoveOptions{Paths: []string{name}}); err != nil {
				t.Fatal(err)
			}
			if err := instance.Sync(t.Context(), syncOptions("Remove asset")); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("removed asset remains: %v", err)
			}
		})
	}
}

func TestIncomingNativeLengthFailurePreservesLink(t *testing.T) {
	t.Parallel()
	instance, workspace, root, remote := fixture(t)
	enroll(t, instance, workspace, map[string]string{"asset.txt": "original\n"})
	before := loadState(t, instance, workspace)
	name := strings.Repeat("n", 512) + ".txt"
	if err := os.WriteFile(filepath.Join(workspace, name), nil, 0o600); err == nil {
		t.Skip("native filesystem supports the tested component length")
	} else if _, ok := errors.AsType[*os.PathError](err); !ok {
		t.Fatal(err)
	}
	// Git trees can describe paths the destination cannot materialize. Build the
	// incoming tree without asking a second working tree to create that name.
	clone := filepath.Join(root, "incoming")
	runGit(t, root, "clone", "-q", "-b", "main", remote, clone)
	runGit(t, clone, "config", "user.name", "SPAS Test")
	runGit(t, clone, "config", "user.email", "spas@example.invalid")
	blob := strings.TrimSpace(gitOutput(t, clone, "rev-parse", "HEAD:asset.txt"))
	runGit(t, clone, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+name)
	runGit(t, clone, "commit", "-q", "-m", "Add incoming asset")
	runGit(t, clone, "push", "-q", "origin", "HEAD:main")
	err := instance.Sync(t.Context(), syncOptions(""))
	if err == nil || !strings.Contains(err.Error(), name) {
		t.Fatalf("Sync() = %v, want native failure naming incoming path", err)
	}
	if native, ok := errors.AsType[*os.PathError](err); !ok || native.Op == "" {
		t.Fatalf("Sync() = %v, want contextual filesystem operation", err)
	}
	after := loadState(t, instance, workspace)
	if after.Private.ExpectedHead != before.Private.ExpectedHead || after.Materializing != nil || !slices.Equal(after.ManagedPaths, before.ManagedPaths) {
		t.Fatalf("failed incoming tree changed link: %+v", after)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "asset.txt")); err != nil || string(got) != "original\n" {
		t.Fatalf("workspace changed: %q, %v", got, err)
	}
	runGit(t, clone, "update-index", "--force-remove", "--", name)
	runGit(t, clone, "commit", "-q", "-m", "Remove unsupported asset")
	runGit(t, clone, "push", "-q", "origin", "HEAD:main")
	instance, _ = testApp(t, workspace, root, remote)
	if err := instance.Sync(t.Context(), syncOptions("")); err != nil {
		t.Fatalf("retry after correcting incoming tree: %v", err)
	}
}

func TestLongPathMaterializationFailureResumesAfterReload(t *testing.T) {
	configureNativeLongPaths(t)
	instance, workspace, root, remote := fixture(t)
	name := strings.Repeat("a", 100) + "/" + strings.Repeat("b", 100) + "/" + strings.Repeat("c", 100) + "/asset.txt"
	enroll(t, instance, workspace, map[string]string{name: "original\n"})
	teammatePush(t, root, remote, map[string]string{name: "incoming\n"}, nil)
	// A real filesystem obstruction at the atomic staging boundary forces a
	// failure after the result has been published, leaving recovery to resume it.
	obstruction := filepath.Join(workspace, ".spas-tmp")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	enableGitProxy(t, "obstruct-staging-after-push")
	t.Setenv("SPAS_APP_REAL_GIT", realGit)
	t.Setenv("SPAS_APP_EDIT_PATH", obstruction)
	instance.Git.Path = os.Args[0]
	err = instance.Sync(t.Context(), syncOptions(""))
	if err == nil || !strings.Contains(err.Error(), ".spas-tmp") {
		t.Fatalf("Sync() = %v, want staging failure", err)
	}
	pending := loadState(t, instance, workspace)
	if pending.Materializing == nil || pending.Materializing.Phase != linkstate.MaterializationPushed || !slices.Contains(pending.Materializing.FinalPaths, name) {
		t.Fatalf("published result lost recovery state: %+v", pending.Materializing)
	}
	if err := os.Remove(obstruction); err != nil {
		t.Fatal(err)
	}
	instance, _ = testApp(t, workspace, root, remote)
	if err := instance.Sync(t.Context(), syncOptions("")); err != nil {
		t.Fatalf("resume after removing obstruction: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(name))); err != nil || string(got) != "incoming\n" {
		t.Fatalf("resumed contents = %q, %v", got, err)
	}
	after := loadState(t, instance, workspace)
	if after.Materializing != nil || after.Private.ExpectedHead != pending.Materializing.ResultPrivateHead {
		t.Fatalf("recovery did not finalize: %+v", after.Materializing)
	}
}

func TestLongPathMergeFailureRestoresRecordedHeadForRetry(t *testing.T) {
	configureNativeLongPaths(t)
	instance, workspace, root, remote := fixture(t)
	name := strings.Repeat("a", 100) + "/" + strings.Repeat("b", 100) + "/" + strings.Repeat("c", 100) + "/asset.txt"
	enroll(t, instance, workspace, map[string]string{name: "original\n"})
	before := loadState(t, instance, workspace)
	teammatePush(t, root, remote, map[string]string{"incoming.txt": "remote\n"}, nil)
	target := filepath.Join(workspace, filepath.FromSlash(name))
	if err := os.WriteFile(target, []byte("edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	enableGitProxy(t, "fail-before-merge")
	t.Setenv("SPAS_APP_REAL_GIT", realGit)
	t.Setenv("SPAS_APP_EDIT_PATH", name)
	instance.Git.Path = os.Args[0]
	err = instance.Sync(t.Context(), syncOptions("Update asset"))
	if err == nil || !strings.Contains(err.Error(), "injected checkout failure") || !strings.Contains(err.Error(), name) {
		t.Fatalf("Sync() = %v, want Git checkout failure", err)
	}
	after := loadState(t, instance, workspace)
	if after.Private.ExpectedHead != before.Private.ExpectedHead || after.ActiveMerge != nil || after.Materializing != nil {
		t.Fatalf("failed merge changed recorded state: %+v", after)
	}
	if head := strings.TrimSpace(gitOutput(t, after.Private.LocalRepositoryPath, "rev-parse", "HEAD")); head != before.Private.ExpectedHead {
		t.Fatalf("failed merge stranded checkout at %s, want %s", head, before.Private.ExpectedHead)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "edited\n" {
		t.Fatalf("approved workspace edits changed: %q, %v", got, err)
	}
	instance, _ = testApp(t, workspace, root, remote)
	if err := instance.Sync(t.Context(), syncOptions("Update asset")); err != nil {
		t.Fatalf("retry after Git checkout failure: %v", err)
	}
	if got := gitOutput(t, root, "--git-dir="+remote, "show", "main:"+name); got != "edited\n" {
		t.Fatalf("retried asset = %q", got)
	}
}
