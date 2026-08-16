//go:build !windows

package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

func replace(source, destination string) error {
	if err := os.Rename(source, destination); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return fmt.Errorf("sync directory for %s: %w", destination, err)
	}
	defer dir.Close()

	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync directory for %s: %w", destination, err)
	}
	return nil
}
