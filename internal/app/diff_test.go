package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffShowsPendingAddition(t *testing.T) {
	t.Parallel()
	publicRoot, _, instance := initializedApp(t, t.TempDir())
	runGit(t, publicRoot, "config", "core.autocrlf", "false")
	for name, content := range map[string]string{"new.txt": "new secret\n", "empty.txt": "", "binary.bin": "\x00\x01"} {
		if err := os.WriteFile(filepath.Join(publicRoot, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := instance.Add(context.Background(), AddOptions{
		Paths: []string{"new.txt", "empty.txt", "binary.bin"}, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip,
	}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		path string
		opts DiffOptions
		json bool
		want string
	}{
		{name: "patch", want: "+new secret"},
		{name: "stat", opts: DiffOptions{Stat: true}, want: "1 insertion(+)"},
		{name: "name only", opts: DiffOptions{NameOnly: true}, want: "new.txt"},
		{name: "json", json: true, want: `"changedPaths":["new.txt"]`},
		{name: "empty patch", path: "empty.txt", want: "new file mode"},
		{name: "empty stat", path: "empty.txt", opts: DiffOptions{Stat: true}, want: "empty.txt"},
		{name: "binary patch", path: "binary.bin", want: "Binary files"},
		{name: "binary stat", path: "binary.bin", opts: DiffOptions{Stat: true}, want: "Bin"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out, stderr bytes.Buffer
			instance.Out, instance.Err, instance.Git.Stderr = &out, &stderr, &stderr
			instance.JSON = test.json
			path := test.path
			if path == "" {
				path = "new.txt"
			}
			test.opts.Paths = []string{path}
			if err := instance.Diff(context.Background(), test.opts); err != nil {
				t.Fatalf("Diff() error = %v", err)
			}
			if !strings.Contains(out.String(), test.want) {
				t.Errorf("Diff() output = %q, want %q", out.String(), test.want)
			}
			if stderr.Len() != 0 {
				t.Errorf("Diff() stderr = %q", stderr.String())
			}
		})
	}
}

func TestDiffRejectsMissingManagedFile(t *testing.T) {
	t.Parallel()
	publicRoot, _, instance := initializedApp(t, t.TempDir())
	state := loadState(t, instance, publicRoot)
	if err := os.Remove(filepath.Join(state.Private.LocalRepositoryPath, "docs", "ARCHITECTURE.md")); err != nil {
		t.Fatal(err)
	}
	for _, opts := range []DiffOptions{{}, {Stat: true}, {NameOnly: true}} {
		if err := instance.Diff(context.Background(), opts); err == nil {
			t.Errorf("Diff(%+v) succeeded with a missing managed private file", opts)
		}
	}
}

type failedDiffWriter struct {
	err error
}

func (w failedDiffWriter) Write([]byte) (int, error) { return 0, w.err }

func TestDiffPropagatesOutputFailure(t *testing.T) {
	t.Parallel()
	publicRoot, _, instance := initializedApp(t, t.TempDir())
	if err := os.WriteFile(filepath.Join(publicRoot, "docs", "ARCHITECTURE.md"), []byte("modified\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeErr := errors.New("diff output unavailable")
	instance.Out = failedDiffWriter{err: writeErr}
	for _, opts := range []DiffOptions{{}, {Stat: true}} {
		if err := instance.Diff(context.Background(), opts); !errors.Is(err, writeErr) {
			t.Errorf("Diff(%+v) error = %v, want output failure", opts, err)
		}
	}
}

func TestDiffPropagatesOperandDisappearance(t *testing.T) {
	for _, test := range []struct {
		name   string
		remove bool
		stat   bool
	}{
		{name: "modified patch"},
		{name: "modified stat", stat: true},
		{name: "removed patch", remove: true},
		{name: "removed stat", remove: true, stat: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			publicRoot, _, instance := initializedApp(t, t.TempDir())
			state := loadState(t, instance, publicRoot)
			path := filepath.Join(publicRoot, "docs", "ARCHITECTURE.md")
			if test.remove {
				if err := instance.Remove(context.Background(), RemoveOptions{Paths: []string{"docs/ARCHITECTURE.md"}}); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(state.Private.LocalRepositoryPath, "docs", "ARCHITECTURE.md")
			} else if err := os.WriteFile(path, []byte("modified\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			realGit, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			enableGitProxy(t, "remove-before-diff")
			t.Setenv("SPAS_APP_REAL_GIT", realGit)
			t.Setenv("SPAS_APP_EDIT_PATH", path)
			instance.Git.Path = os.Args[0]
			if err := instance.Diff(context.Background(), DiffOptions{Stat: test.stat}); err == nil {
				t.Fatal("Diff() swallowed Git's missing-operand error")
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("test did not remove the diff operand: %v", err)
			}
		})
	}
}
