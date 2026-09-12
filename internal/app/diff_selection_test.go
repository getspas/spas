package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getspas/spas/internal/spaserr"
)

func TestDiffSelectsAuthoritativePaths(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"pending", "modified", "removed", "staged", "staged deletion"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			root, _, instance := initializedApp(t, t.TempDir())
			state := loadState(t, instance, root)
			runGit(t, root, "config", "core.ignorecase", "true")
			stored, alias := "docs/ARCHITECTURE.md", "DOCS/architecture.md"
			staged := strings.HasPrefix(phase, "staged")
			if phase == "pending" {
				stored, alias = "new.txt", "NEW.TXT"
			}
			file := filepath.Join(root, filepath.FromSlash(stored))
			if err := os.WriteFile(file, []byte("changed secret\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if phase == "pending" {
				if err := instance.Add(t.Context(), AddOptions{Paths: []string{stored}, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip}); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "removed" {
				if err := instance.Remove(t.Context(), RemoveOptions{Paths: []string{stored}}); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "removed" || phase == "staged deletion" {
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			}
			if staged {
				privateFile := filepath.Join(state.Private.LocalRepositoryPath, filepath.FromSlash(stored))
				if phase == "staged deletion" {
					if err := os.Remove(privateFile); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(privateFile, []byte("changed secret\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				runGit(t, state.Private.LocalRepositoryPath, "add", "--", stored)
			}
			for _, format := range []string{"json", "names", "patch", "stat"} {
				t.Run(format, func(t *testing.T) {
					var output bytes.Buffer
					instance.Out = &output
					instance.JSON = format == "json"
					opts := DiffOptions{Paths: []string{alias}, NameOnly: format == "names", Stat: format == "stat", Staged: staged}
					if err := instance.Diff(t.Context(), opts); err != nil {
						t.Fatal(err)
					}
					switch format {
					case "json":
						var result struct {
							Changed []string `json:"changedPaths"`
							Staged  []string `json:"stagedPaths"`
						}
						if err := json.Unmarshal(output.Bytes(), &result); err != nil {
							t.Fatal(err)
						}
						paths := result.Changed
						if staged {
							paths = result.Staged
						}
						if len(paths) != 1 || paths[0] != stored {
							t.Fatalf("Diff paths = %q, want %q", paths, stored)
						}
					case "names":
						if output.String() != stored+"\n" {
							t.Fatalf("Diff names = %q", output.String())
						}
					case "patch":
						want := "+changed secret"
						if phase == "removed" || phase == "staged deletion" {
							want = "-initial"
						}
						if !strings.Contains(output.String(), want) {
							t.Fatalf("Diff patch = %q, want %q", output.String(), want)
						}
					case "stat":
						if !strings.Contains(output.String(), "1 file changed") {
							t.Fatalf("Diff stat = %q", output.String())
						}
					}
					output.Reset()
					opts.Paths = []string{"not-enrolled.txt"}
					if err := instance.Diff(t.Context(), opts); err != nil {
						t.Fatal(err)
					}
					if format != "json" && output.Len() != 0 {
						t.Fatalf("unmatched filter produced output: %q", output.String())
					}
				})
			}
		})
	}
}

func TestDiffRejectsDistinctCaseEquivalentFile(t *testing.T) {
	t.Parallel()
	root, _, instance := initializedApp(t, t.TempDir())
	alias := filepath.Join(root, "docs", "architecture.md")
	file, err := os.OpenFile(alias, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		t.Skip("volume aliases case variants")
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "config", "core.ignorecase", "true")
	state := loadState(t, instance, root)
	privateFile := filepath.Join(state.Private.LocalRepositoryPath, "docs", "ARCHITECTURE.md")
	if err := os.WriteFile(privateFile, []byte("staged secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, state.Private.LocalRepositoryPath, "add", "--", "docs/ARCHITECTURE.md")
	for _, staged := range []bool{false, true} {
		err := instance.Diff(t.Context(), DiffOptions{Paths: []string{alias}, Staged: staged})
		if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsupportedPath {
			t.Fatalf("Diff(distinct file, staged=%t) = %v", staged, err)
		}
	}
}

func TestDiffHonorsCaseSensitiveFilesystem(t *testing.T) {
	t.Parallel()
	root, _, instance := initializedApp(t, t.TempDir())
	if _, err := os.Stat(filepath.Join(root, "docs", "architecture.md")); err == nil {
		t.Skip("volume aliases case variants")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	runGit(t, root, "config", "core.ignorecase", "false")
	if err := os.WriteFile(filepath.Join(root, "docs", "ARCHITECTURE.md"), []byte("modified\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	instance.Out = &output
	if err := instance.Diff(t.Context(), DiffOptions{Paths: []string{"docs/architecture.md"}, NameOnly: true}); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("case-sensitive filter selected another name: %q", output.String())
	}
	if err := instance.Diff(t.Context(), DiffOptions{Paths: []string{"docs/ARCHITECTURE.md"}, NameOnly: true}); err != nil {
		t.Fatal(err)
	}
	if output.String() != "docs/ARCHITECTURE.md\n" {
		t.Fatalf("exact filter = %q", output.String())
	}
}

func TestStagedDiffSelectsRenamePaths(t *testing.T) {
	t.Parallel()
	root, _, instance := initializedApp(t, t.TempDir())
	state := loadState(t, instance, root)
	privateRoot := state.Private.LocalRepositoryPath
	if err := os.Rename(filepath.Join(privateRoot, "docs", "ARCHITECTURE.md"), filepath.Join(privateRoot, "docs", "RENAMED.md")); err != nil {
		t.Fatal(err)
	}
	runGit(t, privateRoot, "config", "diff.renames", "true")
	runGit(t, privateRoot, "add", "-A")
	runGit(t, root, "config", "core.ignorecase", "true")
	for _, test := range []struct{ selected, stored, patch string }{
		{"docs/ARCHITECTURE.md", "docs/ARCHITECTURE.md", "-initial"},
		{"DOCS/architecture.md", "docs/ARCHITECTURE.md", "-initial"},
		{"docs/RENAMED.md", "docs/RENAMED.md", "+initial"},
		{"DOCS/renamed.md", "docs/RENAMED.md", "+initial"},
	} {
		for _, format := range []string{"json", "names", "patch", "stat"} {
			t.Run(test.selected+"/"+format, func(t *testing.T) {
				var output bytes.Buffer
				instance.Out = &output
				instance.JSON = format == "json"
				opts := DiffOptions{Paths: []string{test.selected}, Staged: true, NameOnly: format == "names", Stat: format == "stat"}
				if err := instance.Diff(t.Context(), opts); err != nil {
					t.Fatal(err)
				}
				want := test.patch
				if format == "json" {
					var result struct {
						Paths []string `json:"stagedPaths"`
					}
					if err := json.Unmarshal(output.Bytes(), &result); err != nil || len(result.Paths) != 1 || result.Paths[0] != test.stored {
						t.Fatalf("rename paths = %s, decode error %v", output.String(), err)
					}
					return
				}
				if format == "names" {
					want = test.stored + "\n"
				} else if format == "stat" {
					want = "1 file changed"
				}
				if !strings.Contains(output.String(), want) {
					t.Fatalf("staged rename output = %q, want %q", output.String(), want)
				}
			})
		}
	}
}

func TestStagedDiffDisambiguatesCaseOnlyRename(t *testing.T) {
	t.Parallel()
	root, _, instance := initializedApp(t, t.TempDir())
	state := loadState(t, instance, root)
	privateRoot := state.Private.LocalRepositoryPath
	runGit(t, root, "config", "core.ignorecase", "true")
	runGit(t, privateRoot, "config", "core.ignorecase", "false")
	oldPath, newPath := "docs/ARCHITECTURE.md", "docs/architecture.md"
	if err := os.Rename(filepath.Join(privateRoot, filepath.FromSlash(oldPath)), filepath.Join(privateRoot, filepath.FromSlash(newPath))); err != nil {
		t.Fatal(err)
	}
	runGit(t, privateRoot, "add", "-A")
	instance.JSON = true
	for _, selected := range []string{oldPath, newPath} {
		var output bytes.Buffer
		instance.Out = &output
		if err := instance.Diff(t.Context(), DiffOptions{Paths: []string{selected}, Staged: true}); err != nil {
			t.Fatal(err)
		}
		var result struct {
			Paths []string `json:"stagedPaths"`
		}
		if err := json.Unmarshal(output.Bytes(), &result); err != nil || len(result.Paths) != 1 || result.Paths[0] != selected {
			t.Fatalf("exact rename selection %q = %s, %v", selected, output.String(), err)
		}
	}
	if err := instance.Diff(t.Context(), DiffOptions{Paths: []string{"DOCS/Architecture.md"}, Staged: true}); err == nil {
		t.Fatal("ambiguous rename alias was accepted")
	}
}
