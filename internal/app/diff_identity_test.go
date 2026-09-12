package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/getspas/spas/internal/spaserr"
)

func TestDiffRejectsUnicodeNeighborCreatedAfterEnrollment(t *testing.T) {
	t.Parallel()
	for _, ignoreCase := range []bool{false, true} {
		t.Run(strconv.FormatBool(ignoreCase), func(t *testing.T) {
			t.Parallel()
			root, _, instance := initializedApp(t, t.TempDir())
			stored := "café.txt"
			canonical := filepath.Join(root, stored)
			if err := os.WriteFile(canonical, []byte("enrolled content\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := instance.Add(t.Context(), AddOptions{Paths: []string{stored}, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip}); err != nil {
				t.Fatal(err)
			}
			neighbor := filepath.Join(root, "cafe\u0301.txt")
			file, err := os.OpenFile(neighbor, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if errors.Is(err, os.ErrExist) {
				t.Skip("volume aliases Unicode normalization variants")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteString("unenrolled neighbor\n"); err != nil {
				file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			originalInfo, err := os.Lstat(canonical)
			if err != nil {
				t.Fatal(err)
			}
			neighborInfo, err := os.Lstat(neighbor)
			if err != nil {
				t.Fatal(err)
			}
			if os.SameFile(originalInfo, neighborInfo) {
				t.Fatal("fixture identities match")
			}
			runGit(t, root, "config", "core.ignorecase", strconv.FormatBool(ignoreCase))
			state := loadState(t, instance, root)
			if err := os.WriteFile(filepath.Join(state.Private.LocalRepositoryPath, stored), []byte("staged content\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runGit(t, state.Private.LocalRepositoryPath, "add", "--", stored)
			for _, staged := range []bool{false, true} {
				for _, format := range []string{"json", "names", "patch", "stat"} {
					var output bytes.Buffer
					instance.Out = &output
					instance.JSON = format == "json"
					opts := DiffOptions{Paths: []string{neighbor}, Staged: staged, NameOnly: format == "names", Stat: format == "stat"}
					err := instance.Diff(t.Context(), opts)
					if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsupportedPath || output.Len() != 0 {
						t.Errorf("Diff(neighbor, staged=%t, %s) = %v, output=%q", staged, format, err, output.String())
					}
				}
				var output bytes.Buffer
				instance.Out, instance.JSON = &output, true
				if err := instance.Diff(t.Context(), DiffOptions{Paths: []string{stored}, Staged: staged}); err != nil {
					t.Fatal(err)
				}
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
					t.Fatalf("exact selection = %s", output.String())
				}
			}
			if err := instance.Remove(t.Context(), RemoveOptions{Paths: []string{neighbor}}); err == nil {
				t.Error("Remove accepted the unenrolled Unicode neighbor")
			}
			if got := loadState(t, instance, root); len(got.PendingAdds) != 1 || got.PendingAdds[0] != stored {
				t.Errorf("rejected neighbor changed enrollment: %q", got.PendingAdds)
			}
		})
	}
}

func TestDiffAcceptsGenuineNormalizationAlias(t *testing.T) {
	t.Parallel()
	root, _, instance := initializedApp(t, t.TempDir())
	stored := "café.txt"
	canonical, observed := filepath.Join(root, stored), filepath.Join(root, "cafe\u0301.txt")
	if err := os.WriteFile(canonical, []byte("enrolled content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	actual, err := os.Stat(canonical)
	if err != nil {
		t.Fatal(err)
	}
	alias, err := os.Stat(observed)
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("volume distinguishes normalization variants")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(actual, alias) {
		t.Fatal("fixture normalization spellings have distinct identities")
	}
	if err := instance.Add(t.Context(), AddOptions{Paths: []string{stored}, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip}); err != nil {
		t.Fatal(err)
	}
	state := loadState(t, instance, root)
	if err := os.WriteFile(filepath.Join(state.Private.LocalRepositoryPath, stored), []byte("staged content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, state.Private.LocalRepositoryPath, "add", "--", stored)
	for _, ignoreCase := range []string{"false", "true"} {
		runGit(t, root, "config", "core.ignorecase", ignoreCase)
		for _, staged := range []bool{false, true} {
			var output bytes.Buffer
			instance.Out = &output
			if err := instance.Diff(t.Context(), DiffOptions{Paths: []string{observed}, NameOnly: true, Staged: staged}); err != nil {
				t.Fatal(err)
			}
			if output.String() != stored+"\n" {
				t.Fatalf("normalization alias = %q", output.String())
			}
		}
	}
}
