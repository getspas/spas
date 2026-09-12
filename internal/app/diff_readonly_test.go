package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type diffReadOnlyCase struct {
	name    string
	stored  string
	alias   string
	staged  bool
	patch   string
	prepare func(*testing.T, string, *App)
}

func TestDiffSelectionDoesNotWriteReadOnlyWorkspace(t *testing.T) {
	for _, test := range []diffReadOnlyCase{
		{
			name:   "ordinary modified",
			stored: "docs/ARCHITECTURE.md",
			alias:  "DOCS/architecture.md",
			patch:  "+changed",
			prepare: func(t *testing.T, publicRoot string, _ *App) {
				if err := os.WriteFile(filepath.Join(publicRoot, "docs", "ARCHITECTURE.md"), []byte("changed\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:   "ordinary pending addition",
			stored: "pending.txt",
			alias:  "PENDING.TXT",
			patch:  "+pending",
			prepare: func(t *testing.T, publicRoot string, instance *App) {
				if err := os.WriteFile(filepath.Join(publicRoot, "pending.txt"), []byte("pending\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := instance.Add(t.Context(), AddOptions{
					Paths: []string{"pending.txt"}, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip,
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:   "ordinary pending removal",
			stored: "docs/ARCHITECTURE.md",
			alias:  "DOCS/architecture.md",
			patch:  "-initial",
			prepare: func(t *testing.T, publicRoot string, instance *App) {
				if err := instance.Remove(t.Context(), RemoveOptions{Paths: []string{"docs/ARCHITECTURE.md"}}); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(publicRoot, "docs", "ARCHITECTURE.md")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:   "staged modified",
			stored: "docs/ARCHITECTURE.md",
			alias:  "DOCS/architecture.md",
			staged: true,
			patch:  "+staged",
			prepare: func(t *testing.T, publicRoot string, instance *App) {
				state := loadState(t, *instance, publicRoot)
				privateFile := filepath.Join(state.Private.LocalRepositoryPath, "docs", "ARCHITECTURE.md")
				if err := os.WriteFile(privateFile, []byte("staged\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				runGit(t, state.Private.LocalRepositoryPath, "add", "--", "docs/ARCHITECTURE.md")
			},
		},
		{
			name:   "staged deletion",
			stored: "docs/ARCHITECTURE.md",
			alias:  "DOCS/architecture.md",
			staged: true,
			patch:  "-initial",
			prepare: func(t *testing.T, publicRoot string, instance *App) {
				state := loadState(t, *instance, publicRoot)
				runGit(t, state.Private.LocalRepositoryPath, "rm", "-q", "--", "docs/ARCHITECTURE.md")
				if err := os.Remove(filepath.Join(publicRoot, "docs", "ARCHITECTURE.md")); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			publicRoot, _, instance := initializedApp(t, root)
			runGit(t, publicRoot, "config", "core.ignoreCase", "true")
			test.prepare(t, publicRoot, &instance)
			denyWorkspaceCreation(t, publicRoot)

			for _, selected := range []string{test.stored, test.alias} {
				for _, format := range []struct {
					name string
					json bool
					opts DiffOptions
				}{
					{name: "json", json: true},
					{name: "name-only", opts: DiffOptions{NameOnly: true}},
					{name: "patch"},
					{name: "stat", opts: DiffOptions{Stat: true}},
				} {
					t.Run(selected+"/"+format.name, func(t *testing.T) {
						var output, stderr strings.Builder
						instance.Out = &output
						instance.Err = &stderr
						instance.Git.Stdout = nil
						instance.Git.Stderr = &stderr
						instance.JSON = format.json
						options := format.opts
						options.Paths = []string{selected}
						options.Staged = test.staged
						if err := instance.Diff(t.Context(), options); err != nil {
							t.Fatalf("Diff(%+v) error = %v; stderr=%q", options, err, stderr.String())
						}
						assertReadOnlyDiffOutput(t, output.String(), format.name, test.staged, test.stored, test.patch)
					})
				}
			}
		})
	}
}

func assertReadOnlyDiffOutput(t *testing.T, output, format string, staged bool, stored, patch string) {
	t.Helper()
	slashOutput := strings.ReplaceAll(output, "\\", "/")
	switch format {
	case "json":
		var value struct {
			ChangedPaths []string `json:"changedPaths"`
			StagedPaths  []string `json:"stagedPaths"`
		}
		if err := json.Unmarshal([]byte(output), &value); err != nil {
			t.Fatalf("decode Diff JSON %q: %v", output, err)
		}
		paths := value.ChangedPaths
		if staged {
			paths = value.StagedPaths
		}
		if len(paths) != 1 || paths[0] != stored {
			t.Fatalf("Diff JSON paths = %q, want [%q]", paths, stored)
		}
	case "name-only":
		if output != stored+"\n" {
			t.Fatalf("Diff name-only output = %q, want %q", output, stored+"\n")
		}
	case "patch":
		base := filepath.Base(filepath.FromSlash(stored))
		if !strings.Contains(slashOutput, base) || !strings.Contains(output, patch) {
			t.Fatalf("Diff patch output = %q, want filename %q and content %q", output, base, patch)
		}
	case "stat":
		base := filepath.Base(filepath.FromSlash(stored))
		if !strings.Contains(slashOutput, base) || !strings.Contains(output, "1 file changed") {
			t.Fatalf("Diff stat output = %q, want filename %q and one changed file", output, base)
		}
	default:
		t.Fatalf("unknown Diff format %q", format)
	}
}
