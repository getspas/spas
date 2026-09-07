package publicgit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/getspas/spas/internal/gitexec"
)

func TestDiscoverPreservesWorkspaceWhitespace(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"project ", "project\u00a0", "project\t", "project\n", "project\r", "project with spaces"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if runtime.GOOS == "windows" && strings.ContainsAny(name, "\t\r\n") {
				t.Skip("Windows filenames exclude these control characters")
			}
			parent := t.TempDir()
			neighbor := filepath.Join(parent, "project")
			if err := os.Mkdir(neighbor, 0o700); err != nil {
				t.Fatal(err)
			}
			runGit(t, neighbor, "init", "-q")
			selected := filepath.Join(parent, name)
			if err := os.Mkdir(selected, 0o700); err != nil {
				if errors.Is(err, os.ErrExist) && runtime.GOOS == "windows" && name == "project " {
					t.Skip("Windows aliases trailing ASCII spaces")
				}
				t.Fatal(err)
			}
			selectedInfo, err := os.Stat(selected)
			if err != nil {
				t.Fatal(err)
			}
			neighborInfo, err := os.Stat(neighbor)
			if err != nil {
				t.Fatal(err)
			}
			if os.SameFile(selectedInfo, neighborInfo) {
				t.Skip("volume aliases the selected name to its trimmed neighbor")
			}
			runGit(t, selected, "init", "-q")
			repository, err := Discover(context.Background(), gitexec.Runner{}, selected)
			if err != nil {
				t.Fatal(err)
			}
			wantRoot, err := filepath.EvalSymlinks(selected)
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Clean(repository.Root) != filepath.Clean(wantRoot) {
				t.Fatalf("Root = %q, want selected workspace %q", repository.Root, wantRoot)
			}
			wantGitDir := filepath.Join(wantRoot, ".git")
			if filepath.Clean(repository.GitDir) != wantGitDir || filepath.Clean(repository.CommonDir) != wantGitDir {
				t.Fatalf("GitDir/CommonDir = %q/%q, want %q", repository.GitDir, repository.CommonDir, wantGitDir)
			}
		})
	}
}

func TestDiscoverPreservesSeparateMetadataWhitespace(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	metadata := filepath.Join(parent, "metadata\u00a0")
	runGit(t, parent, "init", "-q", "--separate-git-dir", metadata, root)
	repository, err := Discover(context.Background(), gitexec.Runner{}, root)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(repository.CommonDir) != want || filepath.Clean(repository.GitDir) != want {
		t.Fatalf("GitDir/CommonDir = %q/%q, want %q", repository.GitDir, repository.CommonDir, want)
	}
}

func TestBranchIgnoresTagNameAmbiguity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "initial")
	runGit(t, root, "-c", "tag.gpgsign=false", "tag", "main")
	repository, err := Discover(context.Background(), gitexec.Runner{}, root)
	if err != nil {
		t.Fatal(err)
	}
	branch, err := repository.Branch(context.Background())
	if err != nil || branch != "main" {
		t.Fatalf("Branch() = %q, %v, want main", branch, err)
	}
}
