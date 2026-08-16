package filesync

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCopyManagedRejectsDestinationSymlinkAncestor(t *testing.T) {
	t.Parallel()

	sourceRoot := t.TempDir()
	destinationRoot := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourceRoot, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "nested", "source"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(destinationRoot, "nested")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}

	err := CopyManaged(sourceRoot, "nested/source", destinationRoot, "nested/destination")
	if err == nil {
		t.Fatal("CopyManaged() error = nil, want symbolic-link rejection")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "destination")); !os.IsNotExist(statErr) {
		t.Fatalf("outside destination was created: %v", statErr)
	}
}

func TestCopyManagedDoesNotCreateDirectoriesThroughDestinationSymlink(t *testing.T) {
	t.Parallel()

	sourceRoot := t.TempDir()
	destinationRoot := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceRoot, "source"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(destinationRoot, "linked")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}

	err := CopyManaged(sourceRoot, "source", destinationRoot, "linked/new/destination")
	if err == nil {
		t.Fatal("CopyManaged() error = nil, want symbolic-link rejection")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "new")); !os.IsNotExist(statErr) {
		t.Fatalf("directory was created through the symbolic link: %v", statErr)
	}
}

func TestRemoveManagedRejectsSymlinkAncestor(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "private.env")
	if err := os.WriteFile(outsideFile, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "nested")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}

	if err := RemoveManaged(root, "nested/private.env"); err == nil {
		t.Fatal("RemoveManaged() error = nil, want symbolic-link rejection")
	}
	if _, err := os.Stat(outsideFile); err != nil {
		t.Fatalf("outside file was removed: %v", err)
	}
}

func TestCopyManagedRejectsSourceAndDestinationSymlinks(t *testing.T) {
	t.Parallel()

	sourceRoot := t.TempDir()
	destinationRoot := t.TempDir()
	sourceTarget := filepath.Join(sourceRoot, "source-target")
	if err := os.WriteFile(sourceTarget, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sourceTarget, filepath.Join(sourceRoot, "source-link")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destinationRoot, "destination-target"), []byte("public"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join(destinationRoot, "destination-target"),
		filepath.Join(destinationRoot, "destination-link"),
	); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}

	if err := CopyManaged(sourceRoot, "source-link", destinationRoot, "new-file"); err == nil {
		t.Fatal("CopyManaged(source symlink) error = nil")
	}
	if err := CopyManaged(sourceRoot, "source-target", destinationRoot, "destination-link"); err == nil {
		t.Fatal("CopyManaged(destination symlink) error = nil")
	}
	content, err := os.ReadFile(filepath.Join(destinationRoot, "destination-target"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "public" {
		t.Fatalf("destination symlink target changed to %q", content)
	}
}

func TestCheckedManagedMutationsPreserveChangedDestination(t *testing.T) {
	t.Parallel()

	sourceRoot := t.TempDir()
	destinationRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceRoot, "file"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(destinationRoot, "file")
	if err := os.WriteFile(destination, []byte("approved"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, existed, err := Snapshot(destination)
	if err != nil || !existed {
		t.Fatalf("Snapshot() = %x, %t, %v", digest, existed, err)
	}
	expected := ExpectedSnapshot{Digest: digest, Existed: true}
	if err := os.WriteFile(destination, []byte("late edit"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := CopyManagedIfUnchanged(sourceRoot, "file", destinationRoot, "file", expected); err == nil {
		t.Fatal("CopyManagedIfUnchanged() error = nil, want changed-destination rejection")
	}
	if err := RemoveManagedIfUnchanged(destinationRoot, "file", expected); err == nil {
		t.Fatal("RemoveManagedIfUnchanged() error = nil, want changed-destination rejection")
	}
	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "late edit" {
		t.Fatalf("destination content = %q, want late edit preserved", content)
	}
}

func TestCheckedManagedMutationsHonorAbsentDestination(t *testing.T) {
	t.Parallel()

	sourceRoot := t.TempDir()
	destinationRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceRoot, "file"), []byte("private"), 0o700); err != nil {
		t.Fatal(err)
	}
	expected := ExpectedSnapshot{Existed: false}
	if err := CopyManagedIfUnchanged(sourceRoot, "file", destinationRoot, "file", expected); err != nil {
		t.Fatalf("CopyManagedIfUnchanged() error = %v", err)
	}
	if runtime.GOOS != "windows" {
		executable, err := Executable(filepath.Join(destinationRoot, "file"))
		if err != nil {
			t.Fatal(err)
		}
		if !executable {
			t.Fatal("copied executable lost its executable mode")
		}
	}
	if err := os.Remove(filepath.Join(destinationRoot, "file")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveManagedIfUnchanged(destinationRoot, "file", expected); err != nil {
		t.Fatalf("RemoveManagedIfUnchanged(absent) error = %v", err)
	}
}

func TestCopyManagedCleansRecognizedOrphanedTemporaryFile(t *testing.T) {
	t.Parallel()

	sourceRoot := t.TempDir()
	destinationRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceRoot, "file"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	tempRoot := filepath.Join(destinationRoot, ManagedTempDirectory)
	if err := os.Mkdir(tempRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempRoot, ".spas-copy-orphan.tmp"), []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := CopyManaged(sourceRoot, "file", destinationRoot, "file"); err != nil {
		t.Fatalf("CopyManaged() error = %v", err)
	}
	if _, err := os.Stat(tempRoot); !os.IsNotExist(err) {
		t.Fatalf("managed temporary directory remains after copy: %v", err)
	}
}

func TestCleanupManagedTempsRejectsUnexpectedEntries(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	tempRoot := filepath.Join(root, ManagedTempDirectory)
	if err := os.Mkdir(tempRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	userPath := filepath.Join(tempRoot, "user.txt")
	if err := os.WriteFile(userPath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CleanupManagedTemps(root); err == nil {
		t.Fatal("CleanupManagedTemps() error = nil, want unexpected-entry rejection")
	}
	if content, err := os.ReadFile(userPath); err != nil || string(content) != "keep" {
		t.Fatalf("unexpected entry = %q, %v; want preserved", content, err)
	}
}

func TestSnapshotDetectsContentCreationAndDeletion(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "private.env")
	missingDigest, existed, err := Snapshot(path)
	if err != nil || existed {
		t.Fatalf("Snapshot(missing) = %x, %t, %v", missingDigest, existed, err)
	}
	if err := VerifySnapshot(path, missingDigest, false); err != nil {
		t.Fatalf("VerifySnapshot(missing) error = %v", err)
	}
	if err := os.WriteFile(path, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifySnapshot(path, missingDigest, false); err == nil {
		t.Fatal("VerifySnapshot(created) error = nil")
	}
	digest, existed, err := Snapshot(path)
	if err != nil || !existed {
		t.Fatalf("Snapshot(existing) = %x, %t, %v", digest, existed, err)
	}
	if err := os.WriteFile(path, []byte("A=2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifySnapshot(path, digest, true); err == nil {
		t.Fatal("VerifySnapshot(modified) error = nil")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := VerifySnapshot(path, digest, true); err == nil {
		t.Fatal("VerifySnapshot(deleted) error = nil")
	}
}

func TestSnapshotAndEqualRejectSymbolicLinks(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "target")
	link := filepath.Join(root, "link")
	if err := os.WriteFile(target, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if _, _, err := Snapshot(link); err == nil {
		t.Fatal("Snapshot(symlink) error = nil, want rejection")
	}
	if _, err := Equal(link, target); err == nil {
		t.Fatal("Equal(symlink, regular) error = nil, want rejection")
	}
}
