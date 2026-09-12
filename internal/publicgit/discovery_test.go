package publicgit

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/getspas/spas/internal/gitexec"
)

func TestDiscoverDistinguishesAbsenceFromFailure(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"outside", "corrupt config", "invalid marker", "invalid gitfile", "missing directory"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			hint := root
			switch kind {
			case "corrupt config":
				runGit(t, root, "init", "-q")
				if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[broken\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "invalid marker":
				if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
					t.Fatal(err)
				}
				hint = filepath.Join(root, "nested")
				if err := os.Mkdir(hint, 0o700); err != nil {
					t.Fatal(err)
				}
			case "invalid gitfile":
				if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: missing-target\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing directory":
				hint = filepath.Join(root, "missing")
			}
			_, err := Discover(t.Context(), gitexec.Runner{}, hint)
			if err == nil || errors.Is(err, ErrNotRepository) != (kind == "outside") {
				t.Fatalf("Discover(%s) = %v", kind, err)
			}
			if kind != "missing directory" {
				if _, ok := gitexec.ExitCode(err); !ok {
					t.Fatalf("discovery discarded Git's error: %v", err)
				}
			}
		})
	}
}

func TestDiscoverPreservesCanceledCause(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Discover(ctx, gitexec.Runner{}, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Discover cancellation = %v", err)
	}
}

func TestDiscoverRejectsInvalidRootOutput(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPAS_PUBLICGIT_PROXY", "invalid-root-output")
	t.Setenv("SPAS_PUBLICGIT_REAL_GIT", realGit)
	if _, err := Discover(t.Context(), gitexec.Runner{Path: os.Args[0]}, t.TempDir()); err == nil || errors.Is(err, ErrNotRepository) {
		t.Fatalf("Discover invalid output = %v", err)
	}
}

func TestDiscoverRejectsInaccessibleMetadata(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	marker := filepath.Join(root, ".git")
	info, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(marker, info.Mode().Perm()); err != nil {
			t.Error(err)
		}
	})
	if err := os.Chmod(marker, 0); err != nil {
		t.Fatal(err)
	}
	if file, err := os.Open(filepath.Join(marker, "config")); err == nil {
		file.Close()
		t.Skip("filesystem or user does not enforce permission denial")
	} else if !errors.Is(err, os.ErrPermission) {
		t.Fatal(err)
	}
	if _, err := Discover(t.Context(), gitexec.Runner{}, root); err == nil || errors.Is(err, ErrNotRepository) {
		t.Fatalf("Discover inaccessible metadata = %v", err)
	}
}

func TestRepositoryAbsenceDiagnostic(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, stderr, stdout string
		code                 int
		want                 bool
	}{
		{"outside", "fatal: not a git repository (or any of the parent directories): .git\n", "", 128, true},
		{"mount", "fatal: not a git repository (or any parent up to mount point /tmp)\nStopping at filesystem boundary (GIT_DISCOVERY_ACROSS_FILESYSTEM not set).\n", "", 128, true},
		{"config", "fatal: bad config line 1 in file .git/config\n", "", 128, false},
		{"explicit gitdir", "fatal: not a git repository: (NULL)\n", "", 128, false},
		{"wrong status", "fatal: not a git repository (or any of the parent directories): .git\n", "", 1, false},
		{"partial output", "fatal: not a git repository (or any of the parent directories): .git\n", "/workspace\n", 128, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := &gitexec.ExitError{ExitCode: test.code, Stderr: test.stderr}
			if got := repositoryAbsentDiagnostic(gitexec.Result{Stdout: []byte(test.stdout)}, err); got != test.want {
				t.Fatalf("absence = %t, want %t", got, test.want)
			}
		})
	}
}
