//go:build windows

package filesync

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestManagedOperationsRejectWindowsJunctionTraversal(t *testing.T) {
	t.Parallel()

	sourceRoot := t.TempDir()
	destinationRoot := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourceRoot, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "nested", "private.env"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(destinationRoot, "nested")
	command := exec.Command(
		"pwsh",
		"-NoLogo",
		"-NoProfile",
		"-NonInteractive",
		"-Command",
		`$ErrorActionPreference = "Stop"; New-Item -ItemType Junction -Path $args[0] -Target $args[1] | Out-Null`,
		junction,
		outside,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("could not create Windows junction: %v\n%s", err, output)
	}

	if err := CopyManaged(sourceRoot, "nested/private.env", destinationRoot, "nested/private.env"); err == nil {
		t.Fatal("CopyManaged() traversed a Windows junction")
	}
	outsideFile := filepath.Join(outside, "private.env")
	if _, err := os.Stat(outsideFile); !os.IsNotExist(err) {
		t.Fatalf("CopyManaged() wrote outside its root: %v", err)
	}
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveManaged(destinationRoot, "nested/private.env"); err == nil {
		t.Fatal("RemoveManaged() traversed a Windows junction")
	}
	if _, err := os.Stat(outsideFile); err != nil {
		t.Fatalf("RemoveManaged() deleted a file outside its root: %v", err)
	}
}
