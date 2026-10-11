package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/getspas/spas/internal/linkstate"
	"github.com/getspas/spas/internal/pathmodel"
	"github.com/getspas/spas/internal/spaserr"
)

func TestNativeDistinctIdentityLifecycle(t *testing.T) {
	t.Parallel()
	for _, names := range [][]string{{"straße.txt", "strasse.txt"}, {"Plan.txt", "plan.txt"}, {"café.txt", "cafe\u0301.txt"}, {"İ.txt", "i.txt"}} {
		t.Run(names[0], func(t *testing.T) {
			instance, workspace, root, remote := fixture(t)
			for index, name := range names {
				file, err := os.OpenFile(filepath.Join(workspace, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
				if errors.Is(err, os.ErrExist) {
					t.Skip("destination aliases the tested spellings")
				}
				if err != nil {
					t.Fatal(err)
				}
				_, writeErr := file.Write([]byte{byte('a' + index), 0, 0xff})
				if err := errors.Join(writeErr, file.Close()); err != nil {
					t.Fatal(err)
				}
			}
			if err := instance.Add(t.Context(), AddOptions{Paths: names, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip}); err != nil {
				t.Fatal(err)
			}
			for index, name := range names {
				names[index] = nativeEntrySpelling(t, workspace, name)
			}
			instance, _ = testApp(t, workspace, root, remote)
			want := append([]string{}, names...)
			slices.Sort(want)
			if got := loadState(t, instance, workspace).PendingAdds; !slices.Equal(got, want) {
				t.Fatalf("pending paths = %q, want %q", got, want)
			}
			if err := instance.Sync(t.Context(), syncOptions("Store distinct assets")); err != nil {
				t.Fatal(err)
			}
			for index, name := range names {
				if got, err := os.ReadFile(filepath.Join(workspace, name)); err != nil || !bytes.Equal(got, []byte{byte('a' + index), 0, 0xff}) {
					t.Fatalf("asset %q = %q, %v", name, got, err)
				}
			}
			if err := os.WriteFile(filepath.Join(workspace, names[0]), []byte("edited\x00\xff"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(workspace, names[0]), 0o700); err != nil {
				t.Fatal(err)
			}
			firstInfo, err := os.Stat(filepath.Join(workspace, names[0]))
			if err != nil {
				t.Fatal(err)
			}
			secondInfo, err := os.Stat(filepath.Join(workspace, names[1]))
			if err != nil {
				t.Fatal(err)
			}
			independentModes := firstInfo.Mode().Perm()&0o111 != secondInfo.Mode().Perm()&0o111
			var selectedDiff bytes.Buffer
			instance.Out = &selectedDiff
			if err := instance.Diff(t.Context(), DiffOptions{Paths: []string{names[0]}, NameOnly: true}); err != nil {
				t.Fatal(err)
			}
			if selectedDiff.String() != names[0]+"\n" {
				t.Fatalf("selected diff = %q", selectedDiff.String())
			}
			hook := filepath.Join(remote, "hooks", "pre-receive")
			if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := instance.Sync(t.Context(), syncOptions("Edit one asset")); err == nil || !strings.Contains(err.Error(), "push private branch") {
				t.Fatalf("Sync() = %v, want rejected push", err)
			}
			if err := os.Remove(hook); err != nil {
				t.Fatal(err)
			}
			instance, _ = testApp(t, workspace, root, remote)
			if err := instance.Sync(t.Context(), syncOptions("")); err != nil {
				t.Fatal(err)
			}
			if got := gitOutput(t, root, "--git-dir="+remote, "show", "main:"+names[1]); got != string([]byte{'b', 0, 0xff}) {
				t.Fatalf("neighbor changed: %q", got)
			}
			if independentModes {
				for index, mode := range []string{"100755", "100644"} {
					entry := gitOutput(t, root, "-c", "core.precomposeUnicode=false", "--git-dir="+remote, "ls-tree", "main", "--", names[index])
					if !strings.HasPrefix(entry, mode+" ") {
						t.Fatalf("independent mode of %q = %q", names[index], entry)
					}
				}
			}
			otherRoot := t.TempDir()
			otherWorkspace := filepath.Join(otherRoot, "workspace")
			if err := os.Mkdir(otherWorkspace, 0o700); err != nil {
				t.Fatal(err)
			}
			runGit(t, otherWorkspace, "init", "-q", "-b", "main")
			runGit(t, otherWorkspace, "config", "user.name", "SPAS Test")
			runGit(t, otherWorkspace, "config", "user.email", "spas@example.invalid")
			runGit(t, otherWorkspace, "commit", "-q", "--allow-empty", "-m", "Initial project")
			other, _ := testApp(t, otherWorkspace, otherRoot, remote)
			if err := other.Link(t.Context(), LinkOptions{Repository: "getspas/private-files", Branch: "main"}); err != nil {
				t.Fatal(err)
			}
			if err := other.Sync(t.Context(), syncOptions("")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(otherWorkspace, names[1]), []byte("remote\x00\xff"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := other.Sync(t.Context(), syncOptions("Edit neighbor remotely")); err != nil {
				t.Fatal(err)
			}
			instance, _ = testApp(t, workspace, root, remote)
			if err := instance.Sync(t.Context(), syncOptions("")); err != nil {
				t.Fatal(err)
			}
			if got, err := os.ReadFile(filepath.Join(workspace, names[0])); err != nil || !bytes.Equal(got, []byte("edited\x00\xff")) {
				t.Fatalf("remote neighbor edit changed first asset: %q, %v", got, err)
			}
			if err := instance.Remove(t.Context(), RemoveOptions{Paths: []string{names[0]}}); err != nil {
				t.Fatal(err)
			}
			if err := instance.Sync(t.Context(), syncOptions("Remove one asset")); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Join(workspace, names[0])); !os.IsNotExist(err) {
				t.Fatalf("removed asset remains: %v", err)
			}
			if got, err := os.ReadFile(filepath.Join(workspace, names[1])); err != nil || !bytes.Equal(got, []byte("remote\x00\xff")) {
				t.Fatalf("neighbor = %q, %v", got, err)
			}
		})
	}
}

func TestIncomingNativeAliasCollisionPreservesWorkspace(t *testing.T) {
	t.Parallel()
	instance, workspace, root, remote := fixture(t)
	first, second := "Plan.txt", "plan.txt"
	if err := os.WriteFile(filepath.Join(workspace, first), []byte("project content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, second)); errors.Is(err, os.ErrNotExist) {
		t.Skip("destination distinguishes the tested names")
	} else if err != nil {
		t.Fatal(err)
	}
	runGit(t, workspace, "add", "--", first)
	runGit(t, workspace, "commit", "-q", "-m", "Track project file")
	seed := filepath.Join(root, "seed")
	if err := os.Mkdir(seed, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "init", "-q", "-b", "main")
	runGit(t, seed, "config", "user.name", "SPAS Test")
	runGit(t, seed, "config", "user.email", "spas@example.invalid")
	blob := strings.TrimSpace(gitOutput(t, seed, "hash-object", "-w", "--stdin"))
	runGit(t, seed, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+second)
	runGit(t, seed, "commit", "-q", "-m", "Store asset")
	runGit(t, seed, "push", "-q", remote, "main")
	runGit(t, workspace, "config", "core.ignoreCase", "false")
	err := instance.Sync(t.Context(), syncOptions(""))
	if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindPathConflict || !strings.Contains(err.Error(), first) || !strings.Contains(err.Error(), second) {
		t.Fatalf("Sync() = %v, want named ownership collision", err)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, first)); err != nil || string(got) != "project content\n" {
		t.Fatalf("project file changed: %q, %v", got, err)
	}
	if len(loadState(t, instance, workspace).ManagedPaths) != 0 {
		t.Fatal("colliding asset was adopted")
	}
}

func TestDiffRejectsDistinctHardLinkSpelling(t *testing.T) {
	t.Parallel()
	instance, workspace, _, _ := fixture(t)
	stored, neighbor := "Plan.txt", "plan.txt"
	if err := os.WriteFile(filepath.Join(workspace, stored), []byte("enrolled\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(workspace, stored), filepath.Join(workspace, neighbor)); errors.Is(err, os.ErrExist) {
		t.Skip("destination aliases the tested names")
	} else if err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if err := instance.Add(t.Context(), AddOptions{Paths: []string{stored}, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip}); err != nil {
		t.Fatal(err)
	}
	runGit(t, workspace, "config", "core.ignoreCase", "true")
	var output bytes.Buffer
	instance.Out = &output
	err := instance.Diff(t.Context(), DiffOptions{Paths: []string{neighbor}, NameOnly: true})
	if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsupportedPath || output.Len() != 0 {
		t.Fatalf("Diff(distinct hard link) = %v, output=%q", err, output.String())
	}
}

func TestNativeIdentityInterruptedMaterialization(t *testing.T) {
	t.Parallel()
	instance, workspace, root, remote := fixture(t)
	names := []string{"İ.txt", "i.txt"}
	for _, name := range names {
		file, err := os.OpenFile(filepath.Join(workspace, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, os.ErrExist) {
			t.Skip("destination aliases the tested names")
		}
		if err != nil {
			t.Fatal(err)
		}
		file.Close()
	}
	enroll(t, instance, workspace, map[string]string{names[0]: "first\n", names[1]: "second\n"})
	hook := filepath.Join(remote, "hooks", "pre-receive")
	target := "'" + strings.ReplaceAll(filepath.ToSlash(filepath.Join(workspace, names[0])), "'", "'\\''") + "'"
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf 'late edit\\n' > "+target+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, names[1]), []byte("second edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(t.Context(), syncOptions("Edit second asset")); err == nil {
		t.Fatal("Sync succeeded after a workspace snapshot changed")
	}
	state := loadState(t, instance, workspace)
	if state.Materializing == nil || state.Materializing.Phase != linkstate.MaterializationPushed {
		t.Fatalf("pending materialization = %#v", state.Materializing)
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, names[0]), []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	instance, _ = testApp(t, workspace, root, remote)
	if err := instance.Sync(t.Context(), syncOptions("")); err != nil {
		t.Fatal(err)
	}
	for index, content := range []string{"first\n", "second edited\n"} {
		if got, err := os.ReadFile(filepath.Join(workspace, names[index])); err != nil || string(got) != content {
			t.Fatalf("recovered %q = %q, %v", names[index], got, err)
		}
	}
	if count := strings.TrimSpace(gitOutput(t, root, "--git-dir="+remote, "rev-list", "--count", "main")); count != "2" {
		t.Fatalf("recovery duplicated commit: %s", count)
	}
	if loadState(t, instance, workspace).Materializing != nil {
		t.Fatal("recovery did not clear its journal")
	}
}

func TestEnrollmentChecksCheckoutDestinationIndependently(t *testing.T) {
	t.Parallel()
	_, workspace, _, remote := fixture(t)
	names := []string{"Plan.txt", "plan.txt"}
	for _, name := range names {
		file, err := os.OpenFile(filepath.Join(workspace, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, os.ErrExist) {
			t.Skip("workspace aliases the tested names")
		}
		if err != nil {
			t.Fatal(err)
		}
		file.Close()
	}
	otherRoot, err := os.MkdirTemp(aliasDestinationRoot(), "spas-alternate-destination-*")
	if err != nil {
		t.Skipf("alternate filesystem root unavailable: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(otherRoot); err != nil {
			t.Error(err)
		}
	})
	if err := os.WriteFile(filepath.Join(otherRoot, "case-check"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(otherRoot, "CASE-CHECK")); os.IsNotExist(err) {
		t.Skip("alternate destination also distinguishes case")
	} else if err != nil {
		t.Fatal(err)
	}
	instance, _ := testApp(t, workspace, otherRoot, remote)
	if err := instance.Link(t.Context(), LinkOptions{Repository: "getspas/private-files", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	err = instance.Add(t.Context(), AddOptions{Paths: names, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip})
	if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsupportedPath || !strings.Contains(err.Error(), "aliases paths") {
		t.Fatalf("Add() = %v, want checkout collision", err)
	}
	if len(loadState(t, instance, workspace).PendingAdds) != 0 {
		t.Fatal("rejected enrollment changed state")
	}
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(workspace, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIncomingUnicodeIdentityAndExclusions(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"cafe\u0301.txt", "café.txt"} {
		t.Run(name, func(t *testing.T) { incomingUnicodeIdentity(t, name) })
	}
}

func incomingUnicodeIdentity(t *testing.T, name string) {
	instance, workspace, root, remote := fixture(t)
	names := []string{name, "Folder/one.txt", "folder/two.txt"}
	seed := filepath.Join(root, "seed")
	if err := os.Mkdir(seed, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "init", "-q", "-b", "main")
	runGit(t, seed, "config", "user.name", "SPAS Test")
	runGit(t, seed, "config", "user.email", "spas@example.invalid")
	for _, name := range names {
		blob := strings.TrimSpace(gitOutput(t, seed, "hash-object", "-w", "--stdin"))
		runGit(t, seed, "-c", "core.precomposeUnicode=false", "update-index", "--add", "--cacheinfo", "100644,"+blob+","+name)
	}
	runGit(t, seed, "commit", "-q", "-m", "Store literal paths")
	runGit(t, seed, "push", "-q", remote, "main")
	// A project setting can disagree with native case behavior; exclusions must
	// cover the names Git actually enumerates without changing project config.
	runGit(t, workspace, "config", "core.ignoreCase", "false")
	runGit(t, workspace, "config", "core.precomposeUnicode", "true")
	if err := instance.Sync(t.Context(), syncOptions("")); err != nil {
		t.Fatal(err)
	}
	instance, _ = testApp(t, workspace, root, remote)
	state := loadState(t, instance, workspace)
	want := append([]string{}, names...)
	slices.Sort(want)
	if !slices.Equal(state.ManagedPaths, want) {
		t.Fatalf("managed paths = %q, want %q", state.ManagedPaths, want)
	}
	if got := gitOutput(t, workspace, "ls-files", "--others", "--exclude-standard", "-z"); got != "" {
		t.Fatalf("assets visible to project Git: %q", got)
	}
	excludePath := filepath.Join(workspace, ".git", "info", "exclude")
	rules, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rules, []byte("/café.txt\n")) {
		if err := os.WriteFile(excludePath, bytes.ReplaceAll(rules, []byte("/café.txt\n"), nil), 0o600); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		instance.Out, instance.JSON = &output, true
		if err := instance.Status(t.Context(), StatusOptions{}); err != nil {
			t.Fatal(err)
		}
		var status Status
		if err := json.Unmarshal(output.Bytes(), &status); err != nil || !slices.Equal(status.ExclusionFailures, []string{names[0]}) {
			t.Fatalf("Status missed exposed Unicode asset: %s, %v", output.String(), err)
		}
		if err := instance.Sync(t.Context(), syncOptions("")); err != nil {
			t.Fatal(err)
		}
		instance.JSON = false
	}
	if err := os.WriteFile(filepath.Join(workspace, names[0]), []byte("edited\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(t.Context(), syncOptions("Edit Unicode asset")); err != nil {
		t.Fatal(err)
	}
	if got := gitOutput(t, root, "-c", "core.precomposeUnicode=false", "--git-dir="+remote, "show", "main:"+names[0]); got != "edited\n" {
		t.Fatalf("remote Unicode contents = %q", got)
	}
	if err := instance.Remove(t.Context(), RemoveOptions{Paths: []string{names[0]}}); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(t.Context(), syncOptions("Remove Unicode asset")); err != nil {
		t.Fatal(err)
	}
	for _, name := range names[1:] {
		if _, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(name))); err != nil {
			t.Fatalf("neighbor %q removed: %v", name, err)
		}
	}
}

func TestSyncIgnoresUnrepresentableProjectNames(t *testing.T) {
	t.Parallel()
	instance, workspace, _, _ := fixture(t)
	blob := strings.TrimSpace(gitOutput(t, workspace, "hash-object", "-w", "--stdin"))
	runGit(t, workspace, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+strings.Repeat("x", 256))
	runGit(t, workspace, "commit", "-q", "-m", "Track absent foreign name")
	if err := os.WriteFile(filepath.Join(workspace, "asset.txt"), []byte("asset\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Add(t.Context(), AddOptions{Paths: []string{"asset.txt"}, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip}); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(t.Context(), syncOptions("Store asset")); err != nil {
		t.Fatalf("unrelated project name blocks sync: %v", err)
	}
}
func TestMergedTreeChecksWorkspaceBeforePublication(t *testing.T) {
	t.Parallel()
	_, workspace, root, remote := fixture(t)
	distinctRoot := os.Getenv("SPAS_TEST_DISTINCT_ROOT")
	if distinctRoot == "" {
		t.Skip("SPAS_TEST_DISTINCT_ROOT must identify an available distinguishing destination")
	}
	csRoot, err := os.MkdirTemp(distinctRoot, "spas-mixed-merge-")
	if err != nil {
		t.Skip(err)
	}
	defer os.RemoveAll(csRoot)
	if err := os.WriteFile(filepath.Join(workspace, "case-check"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "CASE-CHECK")); os.IsNotExist(err) {
		t.Skip("workspace distinguishes case")
	}
	os.Remove(filepath.Join(workspace, "case-check"))
	if err := os.WriteFile(filepath.Join(csRoot, "case-check"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(csRoot, "CASE-CHECK")); err == nil {
		t.Skip("checkout destination aliases case")
	}
	os.Remove(filepath.Join(csRoot, "case-check"))
	instance, _ := testApp(t, workspace, csRoot, remote)
	if err := instance.Link(t.Context(), LinkOptions{Repository: "getspas/private-files", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	enroll(t, instance, workspace, map[string]string{"base.txt": "base\n"})
	seed := filepath.Join(root, "seed-mixed-destination")
	runGit(t, root, "clone", "-q", remote, seed)
	runGit(t, seed, "config", "user.name", "SPAS Test")
	runGit(t, seed, "config", "user.email", "spas@example.invalid")
	if err := os.WriteFile(filepath.Join(seed, "plan.txt"), []byte("remote\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", "plan.txt")
	runGit(t, seed, "commit", "-q", "-m", "Add remote name")
	runGit(t, seed, "push", "-q", "origin", "main")
	if err := os.WriteFile(filepath.Join(workspace, "Plan.txt"), []byte("local\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Add(t.Context(), AddOptions{Paths: []string{"Plan.txt"}, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip}); err != nil {
		t.Fatal(err)
	}
	options := syncOptions("Add local name")
	options.Conflict = ConflictOverride
	err = instance.Sync(t.Context(), options)
	tree := gitOutput(t, root, "--git-dir="+remote, "ls-tree", "--name-only", "main")
	if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsupportedPath {
		t.Fatalf("Sync() = %v, want workspace collision", err)
	}
	if got, readErr := os.ReadFile(filepath.Join(workspace, "Plan.txt")); readErr != nil || string(got) != "local\n" {
		t.Fatalf("local asset changed: %q, %v", got, readErr)
	}
	state := loadState(t, instance, workspace)
	if got := strings.TrimSpace(gitOutput(t, state.Private.LocalRepositoryPath, "rev-parse", "HEAD")); got != state.Private.ExpectedHead {
		t.Fatalf("checkout head %s no longer matches %s", got, state.Private.ExpectedHead)
	}
	if strings.Contains(tree, "Plan.txt") && strings.Contains(tree, "plan.txt") {
		t.Fatal("published workspace-aliasing tree")
	}
}

func aliasDestinationRoot() string {
	if root := os.Getenv("SPAS_TEST_ALIAS_ROOT"); root != "" {
		return root
	}
	return os.TempDir()
}

func TestMergePreflightRestoresUnbornCheckout(t *testing.T) {
	t.Parallel()
	instance, workspace, root, remote := fixture(t)
	if err := instance.Sync(t.Context(), syncOptions("")); err == nil {
		t.Fatal("empty synchronization unexpectedly succeeded")
	}
	if err := os.WriteFile(filepath.Join(workspace, "local.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Add(t.Context(), AddOptions{Paths: []string{"local.txt"}, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip}); err != nil {
		t.Fatal(err)
	}
	seed := filepath.Join(root, "seed-unrelated")
	if err := os.Mkdir(seed, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "init", "-q", "-b", "main")
	runGit(t, seed, "config", "user.name", "SPAS Test")
	runGit(t, seed, "config", "user.email", "spas@example.invalid")
	if err := os.WriteFile(filepath.Join(seed, "remote.txt"), []byte("remote\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", "remote.txt")
	runGit(t, seed, "commit", "-q", "-m", "Create remote assets")
	runGit(t, seed, "push", "-q", remote, "main")
	if err := instance.Sync(t.Context(), syncOptions("Store local asset")); err == nil || !strings.Contains(err.Error(), "preview private merge") {
		t.Fatalf("Sync() = %v, want unrelated-history preflight failure", err)
	}
	var output bytes.Buffer
	instance.Out, instance.JSON = &output, true
	if err := instance.Status(t.Context(), StatusOptions{}); err != nil {
		t.Fatal(err)
	}
	var status Status
	if err := json.Unmarshal(output.Bytes(), &status); err != nil || status.ActualPrivateHead != "" || status.PrivateHeadMismatch || status.PrivateClean == nil || !*status.PrivateClean || !slices.Equal(status.PendingAdds, []string{"local.txt"}) {
		t.Fatalf("restored status = %+v, %v", status, err)
	}
	if err := instance.Remove(t.Context(), RemoveOptions{Paths: []string{"local.txt"}}); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(t.Context(), syncOptions("")); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "local.txt")); err != nil || string(got) != "local\n" {
		t.Fatalf("cancelled local asset = %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "remote.txt")); err != nil || string(got) != "remote\n" {
		t.Fatalf("remote asset = %q, %v", got, err)
	}
}

func TestUnbornMergeRollbackRemovesAdoptedFiles(t *testing.T) {
	instance, workspace, root, remote := fixture(t)
	if err := instance.Sync(t.Context(), syncOptions("")); err == nil {
		t.Fatal("empty synchronization unexpectedly succeeded")
	}
	seed := filepath.Join(root, "seed-late-ownership")
	runGit(t, root, "clone", "-q", remote, seed)
	runGit(t, seed, "config", "user.name", "SPAS Test")
	runGit(t, seed, "config", "user.email", "spas@example.invalid")
	if err := os.WriteFile(filepath.Join(seed, "remote.txt"), []byte("remote\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", "remote.txt")
	runGit(t, seed, "commit", "-q", "-m", "Create remote asset")
	runGit(t, seed, "push", "-q", "origin", "main")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	enableGitProxy(t, "track-public-after-merge")
	t.Setenv("SPAS_APP_REAL_GIT", realGit)
	t.Setenv("SPAS_APP_WORKSPACE", workspace)
	t.Setenv("SPAS_APP_EDIT_PATH", filepath.Join(workspace, "remote.txt"))
	instance.Git.Path = os.Args[0]
	err = instance.Sync(t.Context(), syncOptions(""))
	if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindPathConflict {
		t.Fatalf("Sync() = %v, want late ownership rejection", err)
	}
	state := loadState(t, instance, workspace)
	if index := gitOutput(t, state.Private.LocalRepositoryPath, "ls-files"); index != "" {
		t.Fatalf("rollback left checkout entries: %q", index)
	}
	if _, err := os.Stat(filepath.Join(state.Private.LocalRepositoryPath, "remote.txt")); !os.IsNotExist(err) {
		t.Fatalf("rollback left adopted file: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "remote.txt")); err != nil || string(got) != "project\n" {
		t.Fatalf("project file changed: %q, %v", got, err)
	}
	instance.Git.Path = ""
	var output bytes.Buffer
	instance.Out, instance.JSON = &output, true
	if err := instance.Status(t.Context(), StatusOptions{}); err != nil {
		t.Fatal(err)
	}
	var status Status
	if err := json.Unmarshal(output.Bytes(), &status); err != nil || status.ActualPrivateHead != "" || status.PrivateHeadMismatch || status.PrivateClean == nil || !*status.PrivateClean {
		t.Fatalf("restored checkout = %+v, %v", status, err)
	}
}

func TestNativeCheckoutRejectsUntrackedHardLink(t *testing.T) {
	t.Parallel()
	for _, neighbor := range []string{"copy.txt", "Café.txt"} {
		t.Run(neighbor, func(t *testing.T) {
			instance, workspace, _, _ := fixture(t)
			enroll(t, instance, workspace, map[string]string{"café.txt": "enrolled\n"})
			state := loadState(t, instance, workspace)
			if err := os.Link(filepath.Join(state.Private.LocalRepositoryPath, "café.txt"), filepath.Join(state.Private.LocalRepositoryPath, neighbor)); err != nil {
				t.Skipf("distinct hard-link entry unavailable: %v", err)
			}
			err := instance.Sync(t.Context(), syncOptions(""))
			if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsafeGitState || !strings.Contains(err.Error(), "not clean") {
				t.Fatalf("Sync() accepted untracked checkout entry: %v", err)
			}
			if got, err := os.ReadFile(filepath.Join(workspace, "café.txt")); err != nil || string(got) != "enrolled\n" {
				t.Fatalf("workspace content = %q, %v", got, err)
			}
		})
	}
}

func TestNativeComposedFilenameComponentBudget(t *testing.T) {
	t.Parallel()
	instance, workspace, root, remote := fixture(t)
	name := strings.Repeat("é", 100) + ".txt"
	state := loadState(t, instance, workspace)
	for _, destination := range []string{workspace, state.Private.LocalRepositoryPath} {
		if err := pathmodel.ValidatePathLength(destination, pathmodel.Path(name)); err != nil {
			t.Skipf("retained root length limit applies: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, name), []byte("composed filename\n"), 0o600); err != nil {
		t.Skipf("native filesystem cannot represent filename: %v", err)
	}
	if err := instance.Add(t.Context(), AddOptions{Paths: []string{name}, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip}); err != nil {
		t.Fatal(err)
	}
	instance, _ = testApp(t, workspace, root, remote)
	if err := instance.Sync(t.Context(), syncOptions("Store composed filename")); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, name)); err != nil || string(got) != "composed filename\n" {
		t.Fatalf("filename contents = %q, %v", got, err)
	}
	if got := gitOutput(t, workspace, "ls-files", "--others", "--exclude-standard", "-z"); got != "" {
		t.Fatalf("asset exposed to project Git: %q", got)
	}
}

// Read native directory entries independently of enrollment/state parsing.
func nativeEntrySpelling(t *testing.T, root, relative string) string {
	t.Helper()
	parent := root
	var result []string
	for _, component := range strings.Split(relative, "/") {
		info, err := os.Lstat(filepath.Join(parent, component))
		if err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(parent)
		if err != nil {
			t.Fatal(err)
		}
		name := ""
		for _, entry := range entries {
			if entry.Name() == component {
				name = component
				break
			}
		}
		if name == "" {
			for _, entry := range entries {
				candidate, err := entry.Info()
				if err != nil {
					t.Fatal(err)
				}
				if os.SameFile(info, candidate) {
					if name != "" {
						t.Fatal("native observation is ambiguous")
					}
					name = entry.Name()
				}
			}
		}
		if name == "" {
			t.Fatal("native entry is missing")
		}
		result = append(result, name)
		parent = filepath.Join(parent, name)
	}
	return strings.Join(result, "/")
}
