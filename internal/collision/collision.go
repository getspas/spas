package collision

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/getspas/spas/internal/pathmodel"
)

type Kind string

const (
	TrackedPath   Kind = "tracked_path"
	FileDirectory Kind = "file_directory"
	NativeAlias   Kind = "native_filesystem_alias"
)

type Collision struct {
	Kind    Kind           `json:"kind"`
	Public  pathmodel.Path `json:"publicPath"`
	Private pathmodel.Path `json:"privatePath"`
}

func (c Collision) Error() string {
	switch c.Kind {
	case TrackedPath:
		return fmt.Sprintf("tracked-path conflict: public and private repositories both track %q", c.Public)
	case FileDirectory:
		return fmt.Sprintf("file/directory conflict: public %q conflicts with private %q", c.Public, c.Private)
	case NativeAlias:
		return fmt.Sprintf("native filesystem alias conflict: public %q conflicts with private %q", c.Public, c.Private)
	default:
		return fmt.Sprintf("path conflict: public %q conflicts with private %q", c.Public, c.Private)
	}
}

func DetectNative(root string, public, private []pathmodel.Path) ([]Collision, error) {
	keys, err := pathmodel.ComparisonKeys(root, private, public)
	if err != nil {
		return nil, err
	}
	return detect(public, private, func(path pathmodel.Path) string { return keys[path] }), nil
}

// DetectObserved checks existing native aliases without writing a probe, for
// read-only diagnostics. Missing entries retain their exact Git spelling.
func DetectObserved(root string, public, private []pathmodel.Path) ([]Collision, error) {
	keys := make(map[pathmodel.Path]string)
	observer := pathmodel.NewObserver(root)
	for _, path := range append(append([]pathmodel.Path{}, public...), private...) {
		if _, parseErr := pathmodel.Parse(path.String()); parseErr != nil {
			keys[path] = path.String()
			continue
		}
		observed, err := observer.Destination(path)
		if errors.Is(err, os.ErrNotExist) {
			keys[path] = path.String()
			continue
		}
		if err != nil {
			return nil, err
		}
		keys[path] = observed.String()
	}
	return detect(public, private, func(path pathmodel.Path) string { return keys[path] }), nil
}

func detect(public, private []pathmodel.Path, key func(pathmodel.Path) string) []Collision {
	type entry struct {
		path      pathmodel.Path
		canonical string
	}
	publicEntries := make([]entry, 0, len(public))
	publicExact := make(map[string][]pathmodel.Path, len(public))
	for _, path := range public {
		canonical := key(path)
		publicEntries = append(publicEntries, entry{path: path, canonical: canonical})
		publicExact[canonical] = append(publicExact[canonical], path)
	}
	sort.Slice(publicEntries, func(i, j int) bool {
		if publicEntries[i].canonical == publicEntries[j].canonical {
			return publicEntries[i].path < publicEntries[j].path
		}
		return publicEntries[i].canonical < publicEntries[j].canonical
	})

	result := []Collision{}
	for _, privatePath := range private {
		privateCanonical := key(privatePath)
		for _, publicPath := range publicExact[privateCanonical] {
			switch {
			case publicPath == privatePath:
				result = append(result, Collision{Kind: TrackedPath, Public: publicPath, Private: privatePath})
			default:
				result = append(result, Collision{Kind: NativeAlias, Public: publicPath, Private: privatePath})
			}
		}
		for separator := strings.IndexByte(privateCanonical, '/'); separator >= 0; {
			for _, publicPath := range publicExact[privateCanonical[:separator]] {
				result = append(result, Collision{Kind: FileDirectory, Public: publicPath, Private: privatePath})
			}
			next := strings.IndexByte(privateCanonical[separator+1:], '/')
			if next < 0 {
				break
			}
			separator += next + 1
		}
		descendantPrefix := privateCanonical + "/"
		start := sort.Search(len(publicEntries), func(index int) bool {
			return publicEntries[index].canonical >= descendantPrefix
		})
		for index := start; index < len(publicEntries); index++ {
			entry := publicEntries[index]
			if !strings.HasPrefix(entry.canonical, descendantPrefix) {
				break
			}
			result = append(result, Collision{Kind: FileDirectory, Public: entry.path, Private: privatePath})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Public == result[j].Public {
			return result[i].Private < result[j].Private
		}
		return result[i].Public < result[j].Public
	})
	return result
}

func crossRevisionFileDirectoryConflict(files, descendants map[string]pathmodel.Path) (pathmodel.Path, pathmodel.Path, bool) {
	canonicalPaths := make([]string, 0, len(descendants))
	for canonical := range descendants {
		canonicalPaths = append(canonicalPaths, canonical)
	}
	sort.Strings(canonicalPaths)
	for _, canonical := range canonicalPaths {
		descendant := descendants[canonical]
		for separator := strings.IndexByte(canonical, '/'); separator >= 0; {
			if ancestor, found := files[canonical[:separator]]; found {
				return ancestor, descendant, true
			}
			next := strings.IndexByte(canonical[separator+1:], '/')
			if next < 0 {
				break
			}
			separator += next + 1
		}
	}
	return "", "", false
}

// ValidateDestination checks each simultaneous tree separately, then checks
// cross-revision file/directory transitions without rejecting native aliases
// used only as a rename between revisions.
func ValidateDestination(root string, current, incoming []pathmodel.Path) error {
	keys, err := pathmodel.DestinationKeys(root, append(append([]pathmodel.Path{}, current...), incoming...))
	if err != nil {
		return err
	}
	keyed := func(paths []pathmodel.Path) (map[string]pathmodel.Path, error) {
		original := make(map[string]pathmodel.Path, len(paths))
		for _, path := range paths {
			key := keys[path]
			if prior, exists := original[key]; exists && prior != path {
				return nil, fmt.Errorf("destination %q aliases paths %q and %q", root, prior, path)
			}
			original[key] = path
		}
		return original, nil
	}
	firstNames, err := keyed(current)
	if err != nil {
		return err
	}
	secondNames, err := keyed(incoming)
	if err != nil {
		return err
	}
	for _, names := range []map[string]pathmodel.Path{firstNames, secondNames} {
		if a, b, found := crossRevisionFileDirectoryConflict(names, names); found {
			return fmt.Errorf("destination %q has a file/directory conflict: %q and %q", root, a, b)
		}
	}
	if a, b, found := crossRevisionFileDirectoryConflict(firstNames, secondNames); found {
		return fmt.Errorf("destination %q changes between a file and directory: %q and %q", root, a, b)
	}
	if a, b, found := crossRevisionFileDirectoryConflict(secondNames, firstNames); found {
		return fmt.Errorf("destination %q changes between a file and directory: %q and %q", root, a, b)
	}
	return nil
}
