package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getspas/spas/internal/presentation"
)

func TestDiffColorFollowsDestinationPolicy(t *testing.T) {
	publicRoot, _, instance := initializedApp(t, t.TempDir())
	runGit(t, publicRoot, "config", "core.autocrlf", "true")
	state := loadState(t, instance, publicRoot)
	for _, root := range []string{publicRoot, state.Private.LocalRepositoryPath} {
		runGit(t, root, "config", "color.diff", "always")
		if err := os.WriteFile(filepath.Join(root, "docs", "ARCHITECTURE.md"), []byte("changed asset\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, state.Private.LocalRepositoryPath, "add", "--", "docs/ARCHITECTURE.md")
	// Use different workspace bytes so both unstaged and staged diffs exist.
	if err := os.WriteFile(filepath.Join(publicRoot, "docs", "ARCHITECTURE.md"), []byte("workspace edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, staged := range []bool{false, true} {
		for _, mode := range []presentation.ColorMode{"auto", "always", "never"} {
			var out, diagnostics bytes.Buffer
			instance.Out, instance.Err, instance.Git.Stderr = &out, &diagnostics, &diagnostics
			instance.Presenter = presentation.New(mode, false, false, &out, &diagnostics)
			if err := instance.Diff(t.Context(), DiffOptions{Staged: staged}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "diff --git") || strings.Contains(out.String(), "\x1b") != (mode == "always") || diagnostics.Len() != 0 {
				t.Fatalf("staged %t, color %s: stdout %q, stderr %q", staged, mode, out.String(), diagnostics.String())
			}
			out.Reset()
			if err := instance.Diff(t.Context(), DiffOptions{Staged: staged, NameOnly: true}); err != nil {
				t.Fatal(err)
			}
			if out.String() != "docs/ARCHITECTURE.md\n" {
				t.Fatalf("name-only output: %q", out.String())
			}
		}
	}
}
