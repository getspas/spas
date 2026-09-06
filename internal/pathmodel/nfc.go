package pathmodel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// ValidateNFCSpelling checks that normalizing an observed path preserves each
// selected directory entry. Filesystems may alias Unicode spellings; distinct
// entries, including hard links, require distinct exclusion rules.
func ValidateNFCSpelling(root, observed string) error {
	relative, err := filepath.Rel(root, observed)
	if err != nil {
		return err
	}
	raw := Path(filepath.ToSlash(relative))
	if err := ValidateNoSymlinkComponents(root, raw); err != nil {
		return err
	}
	parent := root
	for _, component := range strings.Split(raw.String(), "/") {
		canonical := norm.NFC.String(component)
		if component != canonical {
			selected, err := os.Lstat(filepath.Join(parent, component))
			if err != nil {
				return err
			}
			normalized, err := os.Lstat(filepath.Join(parent, canonical))
			if err != nil {
				return fmt.Errorf("%q cannot be addressed by its Unicode NFC spelling %q: %w", observed, canonical, err)
			}
			if !os.SameFile(selected, normalized) {
				return fmt.Errorf("%q and its Unicode NFC spelling %q select different entries", observed, canonical)
			}
			entries, err := os.ReadDir(parent)
			if err != nil {
				return err
			}
			matches := 0
			key := Canonical(Path(canonical), true)
			for _, entry := range entries {
				// Apply the portable case policy to names returned by the
				// filesystem, which may use a different case from the request.
				if Canonical(Path(entry.Name()), true) == key {
					matches++
				}
			}
			if matches != 1 {
				return fmt.Errorf("%q has ambiguous directory entries for Unicode NFC spelling %q", observed, canonical)
			}
		}
		parent = filepath.Join(parent, component)
	}
	return nil
}
