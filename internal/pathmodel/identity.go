package pathmodel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// ExclusionPaths includes Git's precomposed spelling only where the native
// destination aliases it. Both literal rules are needed when project Git
// precomposes an imported decomposed path during directory enumeration.
func ExclusionPaths(root string, paths []Path) ([]Path, error) {
	result := append([]Path{}, paths...)
	observer := NewObserver(root)
	for _, path := range paths {
		observed, err := observer.Destination(path)
		if err != nil {
			return nil, err
		}
		result = append(result, observed)
	}
	keys, err := DestinationKeys(root, result)
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		result = append(result, Path(key))
	}
	for _, path := range append([]Path{}, result...) {
		for _, spelling := range []string{norm.NFC.String(path.String()), norm.NFD.String(path.String())} {
			alias := Path(spelling)
			if alias == path {
				continue
			}
			keys, err := ComparisonKeys(root, []Path{path}, []Path{alias})
			if err != nil {
				return nil, err
			}
			if keys[path] == keys[alias] {
				result = append(result, alias)
			}
		}
	}

	slices.Sort(result)
	return slices.Compact(result), nil
}

// DestinationKeys separates exact Git identities from native destination
// equivalence. It probes sibling spellings beneath their existing destination
// parent, including ancestors, without writing any asset path.
func DestinationKeys(root string, paths []Path) (map[Path]string, error) {
	return ComparisonKeys(root, paths, nil)
}

// ComparisonKeys never creates a name belonging only to the project index.
// Such a name may be valid Git data but unrepresentable on this destination.
func ComparisonKeys(root string, managed, observed []Path) (map[Path]string, error) {
	paths := append(append([]Path{}, managed...), observed...)
	owned := make(map[Path]bool, len(managed))
	for _, path := range managed {
		owned[path] = true
	}

	for {
		info, err := os.Lstat(root)
		if err == nil && info.IsDir() {
			break
		}
		if err == nil {
			return nil, fmt.Errorf("destination %q is not a directory", root)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect destination %q: %w", root, err)
		}
		parent := filepath.Dir(root)
		if parent == root {
			return nil, fmt.Errorf("no directory for destination %q", root)
		}
		root = parent
	}
	keys := make(map[Path]string, len(paths))
	type selection struct {
		path       Path
		components []string
		managed    bool
	}
	items := make([]selection, 0, len(paths))
	for _, path := range paths {
		if _, err := ParseObserved(path.String()); err != nil {
			return nil, err
		}
		items = append(items, selection{path, strings.Split(path.String(), "/"), owned[path]})
	}
	var visit func(string, string, []selection) error
	visit = func(parent, prefix string, items []selection) error {
		managedHere := false
		for _, item := range items {
			managedHere = managedHere || item.managed
		}
		if !managedHere {
			for _, item := range items {
				keys[item.path] = prefix + strings.Join(item.components, "/")
			}
			return nil
		}
		groups := make(map[string][]selection)
		for _, item := range items {
			groups[item.components[0]] = append(groups[item.components[0]], item)
		}
		names := make([]string, 0, len(groups))
		for name := range groups {
			names = append(names, name)
		}
		slices.Sort(names)
		representatives := make(map[string]string, len(names))
		if len(names) > 1 {
			probe, err := os.MkdirTemp(parent, ".spas-identity-*")
			if err != nil {
				return fmt.Errorf("probe path identities in %q: %w", parent, err)
			}
			probeErr := func() error {
				infos := make(map[string]os.FileInfo)
				required := make(map[string]bool)
				for name, items := range groups {
					for _, item := range items {
						if item.managed {
							required[name] = true
						}
					}
				}
				for _, name := range names {
					if !required[name] {
						continue
					}
					full := filepath.Join(probe, name)
					if err := os.Mkdir(full, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
						return fmt.Errorf("probe destination spelling %q in %q: %w", name, parent, err)
					}
					info, err := os.Lstat(full)
					if err != nil {
						return err
					}
					representative := name
					for prior, previous := range infos {
						if os.SameFile(info, previous) {
							representative = prior
							break
						}
					}
					representatives[name] = representative
					if representative == name {
						infos[name] = info
					}
				}
				for _, name := range names {
					if required[name] {
						continue
					}
					representatives[name] = name
					info, err := os.Lstat(filepath.Join(probe, name))
					if unsupportedObservedName(err) {
						continue
					}
					if err != nil {
						return fmt.Errorf("compare project spelling %q in %q: %w", name, parent, err)
					}
					for prior, previous := range infos {
						if os.SameFile(info, previous) {
							representatives[name] = prior
							break
						}
					}
				}
				return nil
			}()

			if err := errors.Join(probeErr, os.RemoveAll(probe)); err != nil {
				return err
			}
		} else {
			representatives[names[0]] = names[0]
		}
		children := make(map[string][]selection)
		parents := make(map[string]string)
		for _, name := range names {
			representative := representatives[name]
			key := prefix + representative
			for _, item := range groups[name] {
				if len(item.components) == 1 {
					keys[item.path] = key
				} else {
					item.components = item.components[1:]
					children[key] = append(children[key], item)
				}
			}
			if _, found := parents[key]; !found {
				parents[key] = parent
			}
			needsParent := false
			for _, child := range children[key] {
				needsParent = needsParent || child.managed
			}
			if !needsParent {
				continue
			}
			full := filepath.Join(parent, name)
			info, err := os.Lstat(full)
			if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("inspect destination ancestor %q: %w", full, err)
			}
			if err == nil {
				if info.Mode()&os.ModeSymlink != 0 {
					for _, child := range children[key] {
						if child.managed {
							return fmt.Errorf("%q traverses symbolic link %q", child.path, full)
						}
					}
				} else if info.IsDir() {
					parents[key] = full
				}
			}
		}
		childKeys := make([]string, 0, len(children))
		for key := range children {
			childKeys = append(childKeys, key)
		}
		slices.Sort(childKeys)
		for _, key := range childKeys {
			if err := visit(parents[key], key+"/", children[key]); err != nil {
				return err
			}
		}
		return nil
	}
	if len(items) > 0 {
		if err := visit(root, "", items); err != nil {
			return nil, err
		}
	}
	return keys, nil
}
