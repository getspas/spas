package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteCreatesAndReplacesFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nested", "state.json")
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
}
