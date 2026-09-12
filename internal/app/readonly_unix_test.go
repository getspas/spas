//go:build !windows

package app

import (
	"errors"
	"os"
	"testing"
)

func denyWorkspaceCreation(t *testing.T, root string) {
	t.Helper()
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	originalMode := info.Mode().Perm()
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(root, originalMode); err != nil {
			t.Errorf("restore read-only workspace permissions: %v", err)
		}
	})

	file, err := os.CreateTemp(root, ".spas-case-Probe-test-*")
	if err == nil {
		name := file.Name()
		_ = file.Close()
		_ = os.Remove(name)
		t.Skip("filesystem or test account bypasses Unix directory permission denial")
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("os.CreateTemp(%q) error = %v, want permission denial", root, err)
	}
}
