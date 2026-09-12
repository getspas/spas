package pathmodel

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Observer indexes directory names for one enrollment operation. File identity
// and symlink checks are performed for every selection. Validate must succeed
// before the caller uses the collected paths to change enrollment state.
type Observer struct {
	root        string
	directories map[string]map[string]string
}

func NewObserver(root string) *Observer {
	return &Observer{root: root, directories: make(map[string]map[string]string)}
}

// Path returns actual directory-entry case in NFC. Each component must identify
// one entry whose NFC spelling still addresses the same file.
func (o *Observer) Path(observed string) (Path, error) {
	root := o.root
	relative, err := filepath.Rel(root, observed)
	if err != nil {
		return "", err
	}
	if _, err := Parse(filepath.ToSlash(relative)); err != nil {
		return "", err
	}
	raw := Path(filepath.ToSlash(relative))
	if err := ValidateNoSymlinkComponents(root, raw); err != nil {
		return "", err
	}
	parent := root
	var components []string
	for component := range strings.SplitSeq(raw.String(), "/") {
		selected, err := os.Lstat(filepath.Join(parent, component))
		if err != nil {
			return "", err
		}
		names, err := o.directoryNames(parent)
		if err != nil {
			return "", err
		}
		key := Canonical(Path(component), true)
		name, exists := names[key]
		if !exists {
			return "", fmt.Errorf("%q has no matching directory entry for %q", observed, component)
		}
		if name == "" {
			return "", fmt.Errorf("%q has ambiguous directory entries for %q", observed, component)
		}
		canonical := norm.NFC.String(name)
		normalized, err := os.Lstat(filepath.Join(parent, canonical))
		if err != nil {
			return "", fmt.Errorf("%q cannot be addressed by its Unicode NFC spelling %q: %w", observed, canonical, err)
		}
		if !os.SameFile(selected, normalized) {
			return "", fmt.Errorf("%q and its Unicode NFC spelling %q select different entries", observed, canonical)
		}
		components = append(components, canonical)
		parent = filepath.Join(parent, name)
	}
	return Parse(strings.Join(components, "/"))
}

func (o *Observer) directoryNames(parent string) (map[string]string, error) {
	if names, exists := o.directories[parent]; exists {
		return names, nil
	}
	names, err := readDirectoryNames(parent)
	if err != nil {
		return nil, err
	}
	o.directories[parent] = names
	return names, nil
}

// Validate rejects directory-name changes during path collection.
func (o *Observer) Validate() error {
	for parent, names := range o.directories {
		current, err := readDirectoryNames(parent)
		if err != nil {
			return err
		}
		if !maps.Equal(names, current) {
			return fmt.Errorf("directory entries changed during enrollment in %q", parent)
		}
	}
	return nil
}

func readDirectoryNames(parent string) (map[string]string, error) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(entries))
	for _, entry := range entries {
		key := Canonical(Path(entry.Name()), true)
		if _, exists := names[key]; exists {
			names[key] = "" // Multiple entries have the same portable comparison key.
		} else {
			names[key] = entry.Name()
		}
	}
	return names, nil
}
