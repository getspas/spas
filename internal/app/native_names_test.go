package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/getspas/spas/internal/gitexec"
	"github.com/getspas/spas/internal/spaserr"
)

func TestNativeFilenameLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	instance, workspace, root, remote := fixture(t)
	content := []byte{'b', 'i', 'n', 0, 0xff, '\n'}
	var names []string
	candidates := []string{
		"[private].txt", "#private.txt", "!private.txt",
	}
	if runtime.GOOS != "windows" {
		// Win32 reserves these spellings even where low-level creation or
		// directory enumeration can expose a literal entry for them.
		candidates = append(candidates,
			"notes:2026.md", "CON", "NUL.txt", "COM¹.log", "CONIN$", "CONOUT$",
			"trailing.", "trailing ", "wild*card.txt", "question?.txt",
			`angle<>.txt`, `quote".txt`, "pipe|.txt", `literal\backslash.txt`,
			":(glob)*")
	}
	for _, name := range candidates {
		if supportsLiteralFilename(t, name) {
			names = append(names, "assets/"+name)
		}
	}
	if len(names) == 0 {
		t.Skip("filesystem cannot represent the tested literal filenames")
	}
	for _, name := range names {
		full := filepath.Join(workspace, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, content, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := instance.Add(ctx, AddOptions{Paths: []string{"assets"}, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip}); err != nil {
		t.Fatal(err)
	}
	// These nearby names must neither be ignored nor staged by wildcard rules.
	for _, name := range []string{"wildXcard.txt", "questionX.txt", "p.txt", "trailing"} {
		full := filepath.Join(workspace, "assets", name)
		if err := os.WriteFile(full, []byte("unenrolled\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := instance.Git.Run(ctx, workspace, "check-ignore", "--no-index", "-q", "--", "assets/"+name)
		if code, ok := gitexec.ExitCode(err); !ok || code != 1 {
			t.Fatalf("nearby %q should not be ignored: %v", name, err)
		}
	}
	// A fresh App reads persisted enrollment rather than using in-memory state.
	instance, output := testApp(t, workspace, root, remote)
	instance.JSON = true
	if err := instance.Status(ctx, StatusOptions{}); err != nil {
		t.Fatal(err)
	}
	var status Status
	if err := json.Unmarshal(output.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	if !slices.Equal(status.PendingAdds, names) {
		t.Fatalf("pending additions = %q, want %q", status.PendingAdds, names)
	}
	output.Reset()
	if err := instance.Diff(ctx, DiffOptions{Paths: names, NameOnly: true}); err != nil {
		t.Fatal(err)
	}
	var diff struct {
		ChangedPaths []string `json:"changedPaths"`
	}
	if err := json.Unmarshal(output.Bytes(), &diff); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(diff.ChangedPaths, names) {
		t.Fatalf("changed paths = %q, want %q", diff.ChangedPaths, names)
	}
	if err := instance.Sync(ctx, syncOptions("Store personal assets")); err != nil {
		t.Fatal(err)
	}
	// Inspect the actual Git tree with NUL-delimited output to retain spelling.
	tree := gitOutput(t, root, "--git-dir="+remote, "ls-tree", "-r", "-z", "--name-only", "main")
	if got := strings.Split(strings.TrimSuffix(tree, "\x00"), "\x00"); !slices.Equal(got, names) {
		t.Fatalf("asset tree = %q, want exactly %q", got, names)
	}
	// A second link validates and materializes the incoming asset tree.
	otherRoot := t.TempDir()
	otherWorkspace := filepath.Join(otherRoot, "workspace")
	if err := os.MkdirAll(otherWorkspace, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, otherWorkspace, "init", "-q", "-b", "main")
	runGit(t, otherWorkspace, "config", "user.name", "SPAS Test")
	runGit(t, otherWorkspace, "config", "user.email", "spas@example.invalid")
	runGit(t, otherWorkspace, "commit", "-q", "--allow-empty", "-m", "Initial project")
	other, _ := testApp(t, otherWorkspace, otherRoot, remote)
	if err := other.Link(ctx, LinkOptions{Repository: "getspas/private-files", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := other.Sync(ctx, syncOptions("")); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if got, err := os.ReadFile(filepath.Join(otherWorkspace, filepath.FromSlash(name))); err != nil || !bytes.Equal(got, content) {
			t.Fatalf("incoming %q = %q, %v", name, got, err)
		}
		if err := os.Remove(filepath.Join(workspace, filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
	}
	if err := instance.Sync(ctx, syncOptions("")); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		full := filepath.Join(workspace, filepath.FromSlash(name))
		if got, err := os.ReadFile(full); err != nil || !bytes.Equal(got, content) {
			t.Fatalf("restored %q = %q, %v", name, got, err)
		}
		if info, err := os.Stat(full); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0) {
			t.Fatalf("executable state of %q was not restored: %v", name, err)
		}
	}
	if err := instance.Remove(ctx, RemoveOptions{Paths: names}); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(ctx, syncOptions("Remove personal assets")); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if _, err := os.Lstat(filepath.Join(workspace, filepath.FromSlash(name))); !os.IsNotExist(err) {
			t.Fatalf("removed %q still exists: %v", name, err)
		}
	}
	for _, name := range []string{"wildXcard.txt", "questionX.txt", "p.txt", "trailing"} {
		if got, err := os.ReadFile(filepath.Join(workspace, "assets", name)); err != nil || string(got) != "unenrolled\n" {
			t.Fatalf("unenrolled %q changed: %q, %v", name, got, err)
		}
	}
}

func TestNativeFilenameSafetyRejections(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	instance, workspace, _, _ := fixture(t)
	for _, name := range []string{
		"../outside", ".git/config", "nested/.gitignore", "nested/.gitattributes",
		"nested/.gitmodules", ".spas-tmp/asset", "line\nbreak", "carriage\rreturn",
		"control\x01", "format\u200d", string([]byte{'b', 'a', 'd', 0xff}),
	} {
		if strings.HasPrefix(name, "nested/") || strings.HasPrefix(name, ".spas-tmp/") {
			full := filepath.Join(workspace, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte("unsupported\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		err := instance.Add(ctx, AddOptions{Paths: []string{name}, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip})
		if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsupportedPath {
			t.Fatalf("Add(%q) = %v, want unsupported path", name, err)
		}
	}
	ancestor := "[private]"
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "asset"), []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, ancestor)); err == nil {
		err := instance.Add(ctx, AddOptions{Paths: []string{ancestor + "/asset"}, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip})
		if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsupportedPath {
			t.Fatalf("Add(symlink ancestor) = %v, want unsupported path", err)
		}
	} else {
		t.Logf("filesystem cannot create symlink: %v", err)
	}
	instance.JSON = true
	var output bytes.Buffer
	instance.Out = &output
	if err := instance.Status(ctx, StatusOptions{}); err != nil {
		t.Fatal(err)
	}
	var status Status
	if err := json.Unmarshal(output.Bytes(), &status); err != nil || len(status.PendingAdds) != 0 {
		t.Fatalf("rejected paths altered enrollment: %+v, %v", status, err)
	}
}

// Probe the actual filesystem: successful creation alone can still alias a
// device, trim trailing characters, or interpret a separator on some hosts.
func supportsLiteralFilename(t *testing.T, name string) bool {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, name), []byte("probe"), 0o600); err != nil {
		t.Logf("filesystem cannot create %q: %v", name, err)
		return false
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != name || !entries[0].Type().IsRegular() {
		t.Logf("filesystem did not preserve literal filename %q", name)
		return false
	}
	return true
}

func TestNativeFilenamePushRecovery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	instance, workspace, root, remote := fixture(t)
	name := "draft*?.txt"
	if !supportsLiteralFilename(t, name) {
		t.Skip("filesystem cannot represent wildcard filename")
	}
	enroll(t, instance, workspace, map[string]string{name: "initial\n", "draftXX.txt": "unchanged\n"})
	hook := filepath.Join(remote, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, name), []byte("edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(ctx, syncOptions("Update draft")); err == nil || !strings.Contains(err.Error(), "push private branch") {
		t.Fatalf("Sync() error = %v, want rejected push", err)
	}
	if got := gitOutput(t, root, "--git-dir="+remote, "show", "main:"+name); got != "initial\n" {
		t.Fatalf("failed push changed remote: %q", got)
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	instance, output := testApp(t, workspace, root, remote)
	instance.JSON = true
	if err := instance.Status(ctx, StatusOptions{}); err != nil {
		t.Fatal(err)
	}
	var status Status
	if err := json.Unmarshal(output.Bytes(), &status); err != nil || !status.PendingRecovery {
		t.Fatalf("status = %+v, %v; want pending recovery", status, err)
	}
	if err := instance.Sync(ctx, syncOptions("")); err != nil {
		t.Fatal(err)
	}
	if got := gitOutput(t, root, "--git-dir="+remote, "show", "main:"+name); got != "edited\n" {
		t.Fatalf("recovered remote content = %q", got)
	}
	if got := gitOutput(t, root, "--git-dir="+remote, "show", "main:draftXX.txt"); got != "unchanged\n" {
		t.Fatalf("nearby asset changed during recovery: %q", got)
	}
	if count := strings.TrimSpace(gitOutput(t, root, "--git-dir="+remote, "rev-list", "--count", "main")); count != "2" {
		t.Fatalf("recovery duplicated commit: %s commits, want 2", count)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, name)); err != nil || string(got) != "edited\n" {
		t.Fatalf("recovered workspace = %q, %v", got, err)
	}
}
