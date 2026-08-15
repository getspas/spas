package filesync

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/getspas/spas/internal/pathmodel"
)

const (
	ManagedTempDirectory = ".spas-tmp"
	ManagedTempProbe     = ManagedTempDirectory + "/probe"
	managedTempPrefix    = ".spas-copy-"
	managedTempSuffix    = ".tmp"
)

func Equal(left, right string) (bool, error) {
	leftDigest, err := digest(left)
	if err != nil {
		return false, err
	}
	rightDigest, err := digest(right)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(leftDigest, rightDigest) {
		return false, nil
	}
	leftExecutable, err := Executable(left)
	if err != nil {
		return false, err
	}
	rightExecutable, err := Executable(right)
	if err != nil {
		return false, err
	}
	return leftExecutable == rightExecutable, nil
}

// Executable reports the Git-relevant executable bit of a regular file.
func Executable(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("%s is not a regular file", path)
	}
	return info.Mode().Perm()&0o111 != 0, nil
}

// CopyManaged copies one repository-relative regular file between two roots.
// os.Root constrains all source and destination operations to those roots, and
// the explicit component checks reject symbolic-link indirection even when a
// link would remain inside the root.
func CopyManaged(sourceRoot string, source pathmodel.Path, destinationRoot string, destination pathmodel.Path) error {
	return copyManaged(sourceRoot, source, destinationRoot, destination, nil)
}

// ExpectedSnapshot binds a managed workspace mutation to the bytes, existence,
// and Git-relevant executable mode approved during synchronization planning.
type ExpectedSnapshot struct {
	Digest     [32]byte
	Existed    bool
	Executable bool
}

// CopyManagedIfUnchanged prepares the replacement before checking the
// destination inside its os.Root boundary. A changed destination is never
// renamed over.
func CopyManagedIfUnchanged(
	sourceRoot string,
	source pathmodel.Path,
	destinationRoot string,
	destination pathmodel.Path,
	expected ExpectedSnapshot,
) error {
	return copyManaged(sourceRoot, source, destinationRoot, destination, &expected)
}

func copyManaged(
	sourceRoot string,
	source pathmodel.Path,
	destinationRoot string,
	destination pathmodel.Path,
	expected *ExpectedSnapshot,
) error {
	inputRoot, err := os.OpenRoot(sourceRoot)
	if err != nil {
		return fmt.Errorf("open source root: %w", err)
	}
	defer inputRoot.Close()
	if err := validateRootPath(inputRoot, source, true); err != nil {
		return fmt.Errorf("inspect source %q: %w", source, err)
	}
	input, err := inputRoot.Open(filepath.FromSlash(source.String()))
	if err != nil {
		return fmt.Errorf("open source %q: %w", source, err)
	}
	defer input.Close()
	sourceInfo, err := input.Stat()
	if err != nil {
		return fmt.Errorf("inspect source %q: %w", source, err)
	}

	outputRoot, err := os.OpenRoot(destinationRoot)
	if err != nil {
		return fmt.Errorf("open destination root: %w", err)
	}
	defer outputRoot.Close()
	parent := filepath.Dir(filepath.FromSlash(destination.String()))
	if parent != "." {
		if err := validateExistingParents(outputRoot, destination); err != nil {
			return err
		}
		if err := outputRoot.MkdirAll(parent, 0o700); err != nil {
			return fmt.Errorf("create destination parent for %q: %w", destination, err)
		}
	}
	if err := validateDestination(outputRoot, destination); err != nil {
		return err
	}
	if err := prepareManagedTempDirectory(outputRoot); err != nil {
		return err
	}
	defer outputRoot.Remove(ManagedTempDirectory)

	tempBase, err := temporaryName()
	if err != nil {
		return err
	}
	tempName := filepath.Join(ManagedTempDirectory, tempBase)
	temp, err := outputRoot.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create temporary destination for %q: %w", destination, err)
	}
	defer outputRoot.Remove(tempName)
	if err := temp.Chmod(copyPermissions(sourceInfo.Mode())); err != nil {
		_ = temp.Close()
		return fmt.Errorf("apply mode to %q: %w", destination, err)
	}
	if _, err := io.Copy(temp, input); err != nil {
		_ = temp.Close()
		return fmt.Errorf("copy %q: %w", destination, err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("flush %q: %w", destination, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary destination for %q: %w", destination, err)
	}
	if expected != nil {
		if err := verifyExpectedSnapshot(outputRoot, destination, *expected); err != nil {
			return err
		}
	}
	if err := outputRoot.Rename(tempName, filepath.FromSlash(destination.String())); err != nil {
		return fmt.Errorf("replace %q: %w", destination, err)
	}
	return nil
}

// CleanupManagedTemps removes only recognized orphaned SPAS staging files.
// Any other entry makes the reserved directory fail closed instead of deleting
// user data.
func CleanupManagedTemps(rootPath string) error {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return fmt.Errorf("open managed temporary-file root: %w", err)
	}
	defer root.Close()
	info, err := root.Lstat(ManagedTempDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect managed temporary directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("reserved workspace path %q is not a regular directory", ManagedTempDirectory)
	}
	if err := cleanManagedTempDirectory(root); err != nil {
		return err
	}
	if err := root.Remove(ManagedTempDirectory); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove managed temporary directory: %w", err)
	}
	return nil
}

func prepareManagedTempDirectory(root *os.Root) error {
	info, err := root.Lstat(ManagedTempDirectory)
	if errors.Is(err, os.ErrNotExist) {
		if err := root.Mkdir(ManagedTempDirectory, 0o700); err != nil {
			return fmt.Errorf("create managed temporary directory: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect managed temporary directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("reserved workspace path %q is not a regular directory", ManagedTempDirectory)
	}
	return cleanManagedTempDirectory(root)
}

func cleanManagedTempDirectory(root *os.Root) error {
	directory, err := root.Open(ManagedTempDirectory)
	if err != nil {
		return fmt.Errorf("open managed temporary directory: %w", err)
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil {
		return fmt.Errorf("read managed temporary directory: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close managed temporary directory: %w", closeErr)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() ||
			!strings.HasPrefix(name, managedTempPrefix) ||
			!strings.HasSuffix(name, managedTempSuffix) {
			return fmt.Errorf("reserved workspace directory %q contains unsupported entry %q", ManagedTempDirectory, name)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect orphaned managed temporary file %q: %w", name, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("orphaned managed temporary path %q is not a regular file", name)
		}
		if err := root.Remove(filepath.Join(ManagedTempDirectory, name)); err != nil {
			return fmt.Errorf("remove orphaned managed temporary file %q: %w", name, err)
		}
	}
	return nil
}

func validateExistingParents(root *os.Root, path pathmodel.Path) error {
	components := strings.Split(path.String(), "/")
	for index := 0; index < len(components)-1; index++ {
		current := filepath.FromSlash(strings.Join(components[:index+1], "/"))
		info, err := root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect destination %q: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination %q traverses a symbolic link", path)
		}
		if !info.IsDir() {
			return fmt.Errorf("destination %q has a non-directory ancestor", path)
		}
	}
	return nil
}

func RemoveManaged(rootPath string, path pathmodel.Path) error {
	return removeManaged(rootPath, path, nil)
}

// RemoveManagedIfUnchanged checks the target inside its os.Root boundary
// immediately before removal.
func RemoveManagedIfUnchanged(rootPath string, path pathmodel.Path, expected ExpectedSnapshot) error {
	return removeManaged(rootPath, path, &expected)
}

func removeManaged(rootPath string, path pathmodel.Path, expected *ExpectedSnapshot) error {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return fmt.Errorf("open removal root: %w", err)
	}
	defer root.Close()
	if expected != nil {
		if err := verifyExpectedSnapshot(root, path, *expected); err != nil {
			return err
		}
		if !expected.Existed {
			return nil
		}
	}
	if err := validateRootPath(root, path, true); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect removal target %q: %w", path, err)
	}
	if err := root.Remove(filepath.FromSlash(path.String())); err != nil {
		return fmt.Errorf("remove %q: %w", path, err)
	}
	return nil
}

func verifyExpectedSnapshot(root *os.Root, path pathmodel.Path, expected ExpectedSnapshot) error {
	value := filepath.FromSlash(path.String())
	info, err := root.Lstat(value)
	if errors.Is(err, os.ErrNotExist) {
		if expected.Existed {
			return fmt.Errorf("workspace path %q changed after synchronization planning", path)
		}
		if err := validateExistingParents(root, path); err != nil {
			return err
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect workspace path %q: %w", path, err)
	}
	if !expected.Existed || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("workspace path %q changed after synchronization planning", path)
	}
	if err := validateRootPath(root, path, true); err != nil {
		return fmt.Errorf("inspect workspace path %q: %w", path, err)
	}
	file, err := root.Open(value)
	if err != nil {
		return fmt.Errorf("open workspace path %q: %w", path, err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("read workspace path %q: %w", path, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close workspace path %q: %w", path, closeErr)
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	executable := info.Mode().Perm()&0o111 != 0
	if digest != expected.Digest || executable != expected.Executable {
		return fmt.Errorf("workspace path %q changed after synchronization planning", path)
	}
	return nil
}

func Snapshot(path string) ([32]byte, bool, error) {
	value, err := digestArray(path)
	if errors.Is(err, os.ErrNotExist) {
		return [32]byte{}, false, nil
	}
	return value, err == nil, err
}

func VerifySnapshot(path string, expected [32]byte, existed bool) error {
	actual, currentExists, err := Snapshot(path)
	if err != nil {
		return err
	}
	if currentExists != existed || (existed && actual != expected) {
		return fmt.Errorf("file changed after synchronization planning: %s", path)
	}
	return nil
}

func validateDestination(root *os.Root, path pathmodel.Path) error {
	components := strings.Split(path.String(), "/")
	for index := range components {
		current := filepath.FromSlash(strings.Join(components[:index+1], "/"))
		info, err := root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if index == len(components)-1 {
				return nil
			}
			return fmt.Errorf("destination parent for %q does not exist", path)
		}
		if err != nil {
			return fmt.Errorf("inspect destination %q: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination %q traverses a symbolic link", path)
		}
		if index < len(components)-1 && !info.IsDir() {
			return fmt.Errorf("destination %q has a non-directory ancestor", path)
		}
		if index == len(components)-1 && !info.Mode().IsRegular() {
			return fmt.Errorf("destination %q is not a regular file", path)
		}
	}
	return nil
}

func validateRootPath(root *os.Root, path pathmodel.Path, requireRegular bool) error {
	components := strings.Split(path.String(), "/")
	for index := range components {
		current := filepath.FromSlash(strings.Join(components[:index+1], "/"))
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%q traverses a symbolic link", path)
		}
		if index < len(components)-1 && !info.IsDir() {
			return fmt.Errorf("%q has a non-directory ancestor", path)
		}
		if index == len(components)-1 && requireRegular && !info.Mode().IsRegular() {
			return fmt.Errorf("%q is not a regular file", path)
		}
	}
	return nil
}

// copyPermissions keeps managed copies owner-private while preserving the
// source's executable bit, so scripts survive the round trip through the
// private clone.
func copyPermissions(sourceMode os.FileMode) os.FileMode {
	mode := os.FileMode(0o600)
	// Git records a single executable/non-executable distinction. Preserve it
	// even when the source's only executable bit is group or other.
	if sourceMode.Perm()&0o111 != 0 {
		mode |= 0o100
	}
	return mode
}

func temporaryName() (string, error) {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate temporary filename: %w", err)
	}
	return managedTempPrefix + hex.EncodeToString(random[:]) + managedTempSuffix, nil
}

func digest(path string) ([]byte, error) {
	value, err := digestArray(path)
	if err != nil {
		return nil, err
	}
	return value[:], nil
}

func digestArray(path string) ([32]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return [32]byte{}, err
	}
	if !info.Mode().IsRegular() {
		return [32]byte{}, fmt.Errorf("%s is not a regular file", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return [32]byte{}, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return [32]byte{}, err
	}
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result, nil
}
