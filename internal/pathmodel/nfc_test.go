package pathmodel

import (
	"os"
	"path/filepath"
	"testing"
)

// Managed paths are normalized to NFC at the single parsing funnel, matching
// the precomposed form Git uses on macOS: exclude patterns are not normalized
// by Git, so a decomposed spelling would never match its own exclusion rule.
func TestParseNormalizesToNFC(t *testing.T) {
	t.Parallel()

	decomposed := "secrets/re\u0301sume\u0301.txt" // NFD: e + combining acute
	composed := "secrets/r\u00e9sum\u00e9.txt"     // NFC
	got, err := Parse(decomposed)
	if err != nil {
		t.Fatalf("Parse(NFD) error = %v", err)
	}
	want, err := Parse(composed)
	if err != nil {
		t.Fatalf("Parse(NFC) error = %v", err)
	}
	if got != want {
		t.Fatalf("Parse(NFD) = %q, want the NFC spelling %q", got, want)
	}
	if got.String() != string(want) {
		t.Fatalf("stored spelling = %q, want %q", got.String(), want)
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
