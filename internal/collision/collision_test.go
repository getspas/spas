package collision

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getspas/spas/internal/pathmodel"
)

func TestDetectNative(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	public := paths(t, ".env", "config", ".secret/public.json")
	private := paths(t, ".env", "config/private.json", ".secret/private.json")
	got, err := DetectNative(root, public, private)
	if err != nil || len(got) != 2 || got[0].Kind != TrackedPath || got[1].Kind != FileDirectory {
		t.Fatalf("DetectNative() = %#v, %v", got, err)
	}
}

func TestNativeDestinationEquivalence(t *testing.T) {
	t.Parallel()
	for _, names := range [][]string{{"Plan", "plan"}, {"café", "cafe\u0301"}, {"straße", "strasse"}, {"İ", "i"}} {
		t.Run(names[0], func(t *testing.T) {
			root := t.TempDir()
			first, err := os.OpenFile(filepath.Join(root, names[0]), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			first.Close()
			second, err := os.OpenFile(filepath.Join(root, names[1]), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			aliases := errors.Is(err, os.ErrExist)
			if err != nil && !aliases {
				t.Fatal(err)
			}
			if second != nil {
				second.Close()
			}
			pair := paths(t, names...)
			got, err := DetectNative(root, pair[:1], pair[1:])
			if err != nil {
				t.Fatal(err)
			}
			if (len(got) == 1) != aliases {
				t.Fatalf("native collisions = %#v, aliases=%t", got, aliases)
			}
			err = ValidateDestination(root, pair, nil)
			if (err != nil) != aliases {
				t.Fatalf("simultaneous tree = %v, aliases=%t", err, aliases)
			}
			if err := ValidateDestination(root, pair[:1], pair[1:]); err != nil {
				t.Fatalf("file rename: %v", err)
			}
			descendants := paths(t, names[1]+"/child")
			err = ValidateDestination(root, pair[:1], descendants)
			if (err != nil) != aliases {
				t.Fatalf("file/directory transition = %v, aliases=%t", err, aliases)
			}
			if aliases && !strings.Contains(err.Error(), names[0]) {
				t.Fatalf("collision does not identify paths: %v", err)
			}
		})
	}
}

func TestNativeSharedDirectoryIsNotConflict(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	current := paths(t, "Folder/one.txt")
	incoming := paths(t, "folder/two.txt")
	if err := ValidateDestination(root, current, incoming); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDestination(root, append(current, incoming...), nil); err != nil {
		t.Fatal(err)
	}
}

func paths(t *testing.T, values ...string) []pathmodel.Path {
	t.Helper()
	result := make([]pathmodel.Path, 0, len(values))
	for _, value := range values {
		path, err := pathmodel.Parse(value)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, path)
	}
	return result
}
