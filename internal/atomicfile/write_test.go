package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteCreatesAndReplacesFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "state.json")
	if err := Write(path, []byte("first\n"), 0o600); err != nil {
		t.Fatalf("first Write() error = %v", err)
	}
	if err := Write(path, []byte("second\n"), 0o600); err != nil {
		t.Fatalf("replacement Write() error = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second\n" {
		t.Fatalf("file content = %q, want second write", got)
	}

	// Verify no temporary files were left behind in the directory
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		t.Fatalf("directory entries = %v, want only state.json", entries)
	}
}

func TestWriteEmptyContent(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "empty.txt")
	if err := Write(path, []byte{}, 0o644); err != nil {
		t.Fatalf("Write() empty error = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("file length = %d, want 0", len(got))
	}
}

func TestWriteDirectoryCreationFailure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Create a file where a directory should be
	blockingFile := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blockingFile, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	invalidPath := filepath.Join(blockingFile, "nested", "file.txt")
	if err := Write(invalidPath, []byte("content"), 0o600); err == nil {
		t.Fatal("Write() succeeded unexpectedly when directory path is invalid")
	}
}
