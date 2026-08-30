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

func TestParseRejectsNonPortablePaths(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"",
		"../secret",
		"/absolute",
		"C:/absolute",
		"C:relative",
		"//server/share/private",
		`\\server\share\private`,
		".git/config",
		"nested/.GiT/config",
		"CON",
		"folder/NUL.txt",
		"folder/COM¹.log",
		"CONIN$",
		"file.",
		"file ",
		"stream:name",
		`invalid*name`,
		`literal\backslash`,
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

func TestCanonical(t *testing.T) {
	t.Parallel()

	path, err := Parse("docs/Architecture.md")
	if err != nil {
		t.Fatal(err)
	}
	if got := Canonical(path, true); got != "docs/architecture.md" {
		t.Fatalf("Canonical() = %q", got)
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

func TestCanonicalPreservesInvalidUTF8ObservedPaths(t *testing.T) {
	t.Parallel()

	raw := string([]byte{'D', 'o', 'c', 's', '/', 0xff, 'A'})
	path, err := ParseObserved(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := Canonical(path, false); got != raw {
		t.Fatalf("Canonical(case-sensitive) changed raw bytes: %x", []byte(got))
	}
	want := string([]byte{'d', 'o', 'c', 's', '/', 0xff, 'a'})
	if got := Canonical(path, true); got != want {
		t.Fatalf("Canonical(case-insensitive) = %x, want %x", []byte(got), []byte(want))
	}
}

func TestParseRejectsComponentLongerThanCrossPlatformByteLimit(t *testing.T) {
	t.Parallel()

	_, err := Parse(strings.Repeat("é", 128)) // 256 UTF-8 bytes.
	if err == nil {
		t.Fatal("Parse() error = nil, want component byte-length error")
	}
}

func TestParseAllowsComponentAtPortableASCIILimit(t *testing.T) {
	t.Parallel()

	value := strings.Repeat("a", 255)
	path, err := Parse(value)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if path.String() != value {
		t.Fatalf("Parse() = %q, want %q", path, value)
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

func TestValidatePathLength(t *testing.T) {
	t.Parallel()

	root := "/short/root"
	if runtime.GOOS == "windows" {
		root = `C:\short\root`
	}
	shortPath := Path("a/b/c.txt")
	if err := ValidatePathLength(root, shortPath); err != nil {
		t.Fatalf("ValidatePathLength(short) = %v, want nil", err)
	}

	longPath := Path(strings.Repeat("a/", 130) + "file.txt")
	err := ValidatePathLength(root, longPath)
	if runtime.GOOS == "windows" {
		if err == nil {
			t.Fatal("ValidatePathLength(long) error = nil on Windows, want limit error")
		}
		if !strings.Contains(err.Error(), "reaches or exceeds the Windows limit") || !strings.Contains(err.Error(), "short") {
			t.Fatalf("ValidatePathLength(long) error = %v, want Windows limit error naming root", err)
		}
	} else {
		if err != nil {
			t.Fatalf("ValidatePathLength(long) error = %v on %s, want nil", err, runtime.GOOS)
		}
	}
}
