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

// Path observes directory entries without merging distinct spellings.
func (o *Observer) Path(observed string) (Path, error) { return o.path(observed, false) }

// Destination observes existing parent spellings while retaining the literal
// suffix of a path that has not been created yet.
func (o *Observer) Destination(path Path) (Path, error) { return o.path(path.OSPath(o.root), true) }

func (o *Observer) path(observed string, allowMissing bool) (Path, error) {
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
	parts := strings.Split(raw.String(), "/")
	for index, component := range parts {
		selected, err := os.Lstat(filepath.Join(parent, component))
		if allowMissing && os.IsNotExist(err) {
			return Parse(strings.Join(append(components, parts[index:]...), "/"))
		}
		if err != nil {
			return "", err
		}
		names, err := o.directoryNames(parent)
		if err != nil {
			return "", err
		}
		name, exists := names[component]
		if !exists {
			var matches, normalizedMatches []string
			for candidate := range names {
				if Folded(Path(candidate)) != Folded(Path(component)) {
					continue
				}
				info, err := os.Lstat(filepath.Join(parent, candidate))
				if err != nil {
					return "", err
				}
				if !os.SameFile(selected, info) {
					continue
				}
				matches = append(matches, candidate)
				if norm.NFC.String(candidate) == norm.NFC.String(component) {
					normalizedMatches = append(normalizedMatches, candidate)
				}
			}
			if len(normalizedMatches) > 0 {
				matches = normalizedMatches
			}
			if len(matches) != 1 {
				return "", fmt.Errorf("%q has %d matching directory entries for %q; select an exact entry spelling", observed, len(matches), component)
			}
			name = matches[0]
		}
		components = append(components, name)
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
		names[entry.Name()] = entry.Name()
	}
	return names, nil
}
