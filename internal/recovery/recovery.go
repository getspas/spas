// Package recovery saves point-in-time copies of public workspace files that
// an approved ownership override is about to discard, so an accepted override
// never becomes unrecoverable data loss.
package recovery

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/getspas/spas/internal/filesync"
	"github.com/getspas/spas/internal/pathmodel"
)

// Store writes recovery copies beneath one operation-scoped directory.
type Store struct {
	// Root is the operation directory, e.g.
	// <data>/recovery/<linkID>/<operationID>.
	Root string
}

// NewStore creates an operation-scoped recovery store under dataDir.
func NewStore(dataDir, linkID string) (Store, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Store{}, fmt.Errorf("create recovery store: %w", err)
	}
	root := filepath.Join(dataDir, "recovery", linkID, "op-"+hex.EncodeToString(random[:]))
	return Store{Root: root}, nil
}

// Save copies the current bytes and mode of the workspace file at path into
// the store. It reports saved=false without error when the source does not
// exist or is not a regular file (there is nothing to recover).
func (s Store) Save(publicRoot string, path pathmodel.Path) (bool, error) {
	source := path.OSPath(publicRoot)
	info, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect recovery source %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		return false, fmt.Errorf("create recovery store: %w", err)
	}
	if err := filesync.CopyManaged(publicRoot, path, s.Root, path); err != nil {
		return false, fmt.Errorf("save recovery copy of %q: %w", path, err)
	}
	return true, nil
}

// Used reports whether any recovery copy was written.
func (s Store) Used() bool {
	if s.Root == "" {
		return false
	}
	_, err := os.Stat(s.Root)
	return err == nil
}
