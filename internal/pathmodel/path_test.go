package pathmodel

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		".env",
		"docs/ARCHITECTURE.md",
		".secret/private.json",
	} {
		got, err := Parse(value)
		if err != nil {
			t.Errorf("Parse(%q) error = %v", value, err)
		}
		if string(got) != value {
			t.Errorf("Parse(%q) = %q", value, got)
		}
	}
}

func TestParseRejectsUnsafeOrUnrepresentablePaths(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"",
		"../secret",
		"/absolute",
		"//server/share/private",
		".git/config",
		"nested/.GiT/config",
		"line\nbreak",
		"carriage\rreturn",
		"control\u0001character",
		"format\u200Echaracter",
		string([]byte{'b', 'a', 'd', 0xff}),
	} {
		if _, err := Parse(value); err == nil {
			t.Errorf("Parse(%q) error = nil, want non-nil", value)
		}
	}
}

func TestParseUsesHostFilenameSyntax(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"CON", "folder/NUL.txt", "folder/COM¹.log", "CONIN$", "CONOUT$",
		"file.", "file ", "stream:name", `invalid*name`, `question?name`,
		`angle<>name`, `quote"name`, `pipe|name`, `literal\backslash`,
		"C:/relative", "C:relative", `\\server\share\private`,
	} {
		got, err := Parse(value)
		if runtime.GOOS == "windows" {
			if err == nil {
				t.Errorf("Parse(%q) accepted unsupported Windows syntax", value)
			}
		} else if err != nil || got.String() != value {
			t.Errorf("Parse(%q) = %q, %v; want literal path", value, got, err)
		}
	}
}

func TestFolded(t *testing.T) {
	t.Parallel()

	path, err := Parse("docs/Architecture.md")
	if err != nil {
		t.Fatal(err)
	}
	if got := Folded(path); got != "docs/architecture.md" {
		t.Fatalf("Folded() = %q", got)
	}
}

func TestResolveDoesNotFollowManagedPathSymlinks(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "target"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(root, "link")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	path, absolute, err := Resolve(root, root, "link")
	if err != nil {
		t.Fatal(err)
	}
	if path != "link" || filepath.Base(absolute) != "link" {
		t.Fatalf("Resolve(link) = %q, %q; symlink was followed", path, absolute)
	}
	if err := ValidateNoSymlinkComponents(root, path); err == nil {
		t.Fatal("ValidateNoSymlinkComponents(link) error = nil")
	}
}

func TestFoldedPreservesInvalidUTF8ObservedPaths(t *testing.T) {
	t.Parallel()

	raw := string([]byte{'D', 'o', 'c', 's', '/', 0xff, 'A'})
	path, err := ParseObserved(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := path.String(); got != raw {
		t.Fatalf("Path.String() changed raw bytes: %x", []byte(got))
	}
	want := string([]byte{'d', 'o', 'c', 's', '/', 0xff, 'a'})
	if got := Folded(path); got != want {
		t.Fatalf("Folded() = %x, want %x", []byte(got), []byte(want))
	}
}

func TestParsePreservesLongComponents(t *testing.T) {
	t.Parallel()
	for _, value := range []string{strings.Repeat("é", 128), strings.Repeat("a", 512)} {
		path, err := Parse(value)
		if err != nil || path.String() != value {
			t.Fatalf("Parse() = %q, %v; want exact identity", path, err)
		}
	}
}

func TestResolveAllowsTotalPathLengthExceedingWindowsLimit(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	// Build relative path components that push the total absolute path length >= 260.
	// Note each component is <= 255 bytes, but total length exceeds 260.
	comp := strings.Repeat("a", 100)
	rel := filepath.Join(comp, comp, comp)
	path, abs, err := Resolve(root, root, rel)
	if err != nil {
		t.Fatalf("Resolve() error = %v, want nil", err)
	}
	if len(abs) < 260 {
		t.Fatalf("len(abs) = %d, want >= 260", len(abs))
	}
	expected := comp + "/" + comp + "/" + comp
	if path.String() != expected {
		t.Fatalf("Resolve() = %q, want %q", path, expected)
	}
}
