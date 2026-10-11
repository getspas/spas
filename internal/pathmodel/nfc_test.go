package pathmodel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParsePreservesUnicodeIdentity(t *testing.T) {
	t.Parallel()
	for _, spelling := range []string{"secrets/re\u0301sume\u0301.txt", "secrets/résumé.txt"} {
		got, err := Parse(spelling)
		if err != nil || got.String() != spelling {
			t.Fatalf("Parse(%q) = %q, %v", spelling, got, err)
		}
	}
}

func TestObserverRechecksSymlinksAfterIndexingNames(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	selected := filepath.Join(root, "secret.env")
	if err := os.WriteFile(selected, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	observer := NewObserver(root)
	if _, err := observer.Path(selected); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(selected); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.env", selected); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if _, err := observer.Path(selected); err == nil {
		t.Fatal("observer accepted a replacement symlink")
	}
}

func TestObserverDetectsRenamedDirectoryEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	selected := filepath.Join(root, "café.env")
	if err := os.WriteFile(selected, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	observer := NewObserver(root)
	if _, err := observer.Path(selected); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(selected, filepath.Join(root, "CAFÉ.env")); err != nil {
		t.Fatal(err)
	}
	if err := observer.Validate(); err == nil {
		t.Fatal("observer accepted changed directory spelling")
	}
}
