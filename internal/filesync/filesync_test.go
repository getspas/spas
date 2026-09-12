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

func TestCopyManagedInheritsCheckoutPermissions(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}

	sourceRoot := t.TempDir()
	destinationRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceRoot, "plain"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "tool"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	// Control entries record what the current umask leaves of the maximal
	// modes without mutating the process-global umask in a parallel test.
	controlRoot := t.TempDir()
	control := func(name string, mode os.FileMode) os.FileMode {
		t.Helper()
		file, err := os.OpenFile(filepath.Join(controlRoot, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			t.Fatal(err)
		}
		info, err := file.Stat()
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			t.Fatal(err)
		}
		return info.Mode().Perm()
	}
	wantPlain := control("plain", 0o666)
	wantExec := control("tool", 0o777)
	if err := os.Mkdir(filepath.Join(controlRoot, "dir"), 0o777); err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(filepath.Join(controlRoot, "dir"))
	if err != nil {
		t.Fatal(err)
	}
	wantDir := dirInfo.Mode().Perm()

	if err := CopyManaged(sourceRoot, "plain", destinationRoot, "plain"); err != nil {
		t.Fatalf("CopyManaged(plain) error = %v", err)
	}
	if err := CopyManaged(sourceRoot, "tool", destinationRoot, "nested/tool"); err != nil {
		t.Fatalf("CopyManaged(tool) error = %v", err)
	}

	for _, test := range []struct {
		path string
		want os.FileMode
	}{
		{"plain", wantPlain},
		{filepath.Join("nested", "tool"), wantExec},
		{"nested", wantDir},
	} {
		info, err := os.Stat(filepath.Join(destinationRoot, test.path))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != test.want {
			t.Errorf("%s mode = %o, want %o", test.path, got, test.want)
		}
	}
}

func TestCopyManagedOwnerOnlyKeepsCopiesPrivate(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}

	sourceRoot := t.TempDir()
	destinationRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceRoot, "plain"), []byte("private"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "tool"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := CopyManagedOwnerOnly(sourceRoot, "plain", destinationRoot, "plain"); err != nil {
		t.Fatalf("CopyManagedOwnerOnly(plain) error = %v", err)
	}
	if err := CopyManagedOwnerOnly(sourceRoot, "tool", destinationRoot, "nested/tool"); err != nil {
		t.Fatalf("CopyManagedOwnerOnly(tool) error = %v", err)
	}

	for _, test := range []struct {
		path string
		want os.FileMode
	}{
		{"plain", 0o600},
		{filepath.Join("nested", "tool"), 0o700},
	} {
		info, err := os.Stat(filepath.Join(destinationRoot, test.path))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != test.want {
			t.Errorf("%s mode = %o, want %o", test.path, got, test.want)
		}
	}
	info, err := os.Stat(filepath.Join(destinationRoot, "nested"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got&0o077 != 0 {
		t.Errorf("nested directory mode = %o, want no group/other access", got)
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
