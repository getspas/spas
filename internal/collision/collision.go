package collision

import (
	"fmt"
	"sort"
	"strings"

	"github.com/getspas/spas/internal/pathmodel"
)

type Kind string

const (
	TrackedPath     Kind = "tracked_path"
	FileDirectory   Kind = "file_directory"
	CaseInsensitive Kind = "case_insensitive_filesystem"
	PortablePath    Kind = "cross_platform_filesystem"
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
	case CaseInsensitive:
		return fmt.Sprintf("case-insensitive filesystem conflict: public %q conflicts with private %q", c.Public, c.Private)
	case PortablePath:
		return fmt.Sprintf("cross-platform filesystem conflict: public %q conflicts with private %q", c.Public, c.Private)
	default:
		return fmt.Sprintf("path conflict: public %q conflicts with private %q", c.Public, c.Private)
	}
}

func Detect(public, private []pathmodel.Path, ignoreCase bool) []Collision {
	type entry struct {
		path      pathmodel.Path
		canonical string
	}
	publicEntries := make([]entry, 0, len(public))
	publicExact := make(map[string][]pathmodel.Path, len(public))
	for _, path := range public {
		canonical := pathmodel.Canonical(path, ignoreCase)
		publicEntries = append(publicEntries, entry{path: path, canonical: canonical})
		publicExact[canonical] = append(publicExact[canonical], path)
	}
	sort.Slice(publicEntries, func(i, j int) bool {
		if publicEntries[i].canonical == publicEntries[j].canonical {
			return publicEntries[i].path < publicEntries[j].path
		}
		return publicEntries[i].canonical < publicEntries[j].canonical
	})

	var result []Collision
	for _, privatePath := range private {
		privateCanonical := pathmodel.Canonical(privatePath, ignoreCase)
		for _, publicPath := range publicExact[privateCanonical] {
			switch {
			case publicPath == privatePath:
				result = append(result, Collision{Kind: TrackedPath, Public: publicPath, Private: privatePath})
			case ignoreCase:
				result = append(result, Collision{Kind: CaseInsensitive, Public: publicPath, Private: privatePath})
			default:
				result = append(result, Collision{Kind: PortablePath, Public: publicPath, Private: privatePath})
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

// PrivateRevisionCompatibility validates two individually portable private
// trees as successive revisions. Case-only file renames are valid: Git may
// replace "Foo" with "foo" between revisions even though their canonical
// spellings are equal. A file/directory transition is deliberately rejected
// by the current implementation because materializing it safely requires ordered removal of the
// old hierarchy and more extensive crash-recovery state.
func PrivateRevisionCompatibility(current, incoming []pathmodel.Path, ignoreCase bool) error {
	if err := PrivatePortabilityConflicts(current, ignoreCase); err != nil {
		return err
	}
	if err := PrivatePortabilityConflicts(incoming, ignoreCase); err != nil {
		return err
	}

	currentByCanonical := make(map[string]pathmodel.Path, len(current))
	incomingByCanonical := make(map[string]pathmodel.Path, len(incoming))
	for _, path := range current {
		currentByCanonical[pathmodel.Canonical(path, ignoreCase)] = path
	}
	for _, path := range incoming {
		incomingByCanonical[pathmodel.Canonical(path, ignoreCase)] = path
	}

	if ancestor, descendant, found := crossRevisionFileDirectoryConflict(currentByCanonical, incomingByCanonical); found {
		return fmt.Errorf("private repository changes a path between a file and a directory across revisions: %q conflicts with %q", ancestor, descendant)
	}
	if ancestor, descendant, found := crossRevisionFileDirectoryConflict(incomingByCanonical, currentByCanonical); found {
		return fmt.Errorf("private repository changes a path between a file and a directory across revisions: %q conflicts with %q", ancestor, descendant)
	}
	return nil
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

func PrivatePortabilityConflicts(paths []pathmodel.Path, ignoreCase bool) error {
	// Git trees contain files, not standalone directories. A private tree is
	// portable only when no two file paths collapse to the same canonical
	// spelling and no canonical file path is an ancestor of another file. The
	// latter catches trees such as `Foo` and `foo/child`, which are legal on a
	// case-sensitive filesystem but cannot be checked out on a case-insensitive
	// one because `Foo` would need to be both a file and a directory.
	seen := make(map[string]pathmodel.Path, len(paths))
	for _, path := range paths {
		canonical := pathmodel.Canonical(path, ignoreCase)
		if previous, found := seen[canonical]; found && previous != path {
			return fmt.Errorf("private repository contains a non-portable case or Unicode path conflict: %q and %q", previous, path)
		}
		seen[canonical] = path
	}
	canonicalPaths := make([]string, 0, len(seen))
	for canonical := range seen {
		canonicalPaths = append(canonicalPaths, canonical)
	}
	sort.Strings(canonicalPaths)
	for _, canonical := range canonicalPaths {
		path := seen[canonical]
		for separator := strings.IndexByte(canonical, '/'); separator >= 0; {
			if ancestor, found := seen[canonical[:separator]]; found {
				return fmt.Errorf("private repository contains a non-portable case-folded file/directory conflict: %q and %q", ancestor, path)
			}
			next := strings.IndexByte(canonical[separator+1:], '/')
			if next < 0 {
				break
			}
			separator += next + 1
		}
	}
	return nil
}
