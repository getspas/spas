package pathmodel

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

type Path string

var windowsReserved = regexp.MustCompile(`(?i)^(CON|PRN|AUX|NUL|CONIN\$|CONOUT\$|COM[1-9¹²³]|LPT[1-9¹²³])(?:\..*)?$`)

func Parse(value string) (Path, error) {
	if value == "" {
		return "", fmt.Errorf("path is empty")
	}
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("path contains an unsupported control character")
	}
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("path must be valid UTF-8")
	}
	// Managed paths are stored, excluded, and staged in Unicode NFC, matching
	// the precomposed form Git itself uses on macOS. Exclude patterns are not
	// normalized by Git, so a decomposed spelling would never match.
	value = norm.NFC.String(value)
	if strings.Contains(value, `\`) {
		return "", fmt.Errorf("path must use Git slash separators")
	}

	if strings.HasPrefix(value, "/") || filepath.IsAbs(value) {
		return "", fmt.Errorf("path must be relative to the public workspace")
	}

	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path must stay inside the public workspace")
	}

	components := strings.Split(clean, "/")
	for _, component := range components {
		if err := validateComponent(component); err != nil {
			return "", fmt.Errorf("invalid path %q: %w", value, err)
		}
		if strings.EqualFold(component, ".git") {
			return "", fmt.Errorf("path must not contain Git metadata")
		}
	}

	return Path(strings.Join(components, "/")), nil
}

// ParseObserved validates a path observed in the PUBLIC repository's index.
// It rejects only what is unsafe to compare or to pass to Git — it must never
// reject a merely non-portable name, because a public repository is allowed to
// track files SPAS itself would not manage (colons, Windows-reserved names,
// unusual Unicode). The original spelling is preserved exactly so pathspecs
// still match the index.
func ParseObserved(value string) (Path, error) {
	if value == "" {
		return "", fmt.Errorf("path is empty")
	}
	if strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("path contains a NUL byte")
	}
	if strings.HasPrefix(value, "/") || filepath.IsAbs(value) {
		return "", fmt.Errorf("path must be relative to the public workspace")
	}
	for _, component := range strings.Split(value, "/") {
		if component == ".." {
			return "", fmt.Errorf("path must stay inside the public workspace")
		}
		if strings.EqualFold(component, ".git") {
			return "", fmt.Errorf("path must not contain Git metadata")
		}
	}
	return Path(value), nil
}

func Resolve(publicRoot, base, value string) (Path, string, error) {
	resolvedRoot, err := filepath.EvalSymlinks(publicRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve public workspace: %w", err)
	}
	publicRoot = resolvedRoot
	resolvedBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", "", fmt.Errorf("resolve path base: %w", err)
	}
	base = resolvedBase
	if filepath.IsAbs(value) {
		base = ""
	}
	absolute, err := filepath.Abs(filepath.Join(base, value))
	if err != nil {
		return "", "", fmt.Errorf("resolve path %q: %w", value, err)
	}

	relative, err := filepath.Rel(publicRoot, absolute)
	if err != nil {
		return "", "", fmt.Errorf("make path relative to public workspace: %w", err)
	}
	path, err := Parse(filepath.ToSlash(relative))
	if err != nil {
		return "", "", err
	}
	return path, absolute, nil
}

func validateComponent(value string) error {
	if value == "" || value == "." || value == ".." {
		return fmt.Errorf("empty or traversal component")
	}
	// Private paths must be materializable on every supported filesystem.
	// Common Unix filesystems limit one name component to 255 encoded bytes,
	// while Windows limits one component to 255 Unicode characters. A 255-byte
	// UTF-8 ceiling is the conservative common denominator: it also bounds the
	// Unicode character count because every character occupies at least one byte.
	if len(value) > 255 {
		return fmt.Errorf("component exceeds the cross-platform 255-byte filename limit")
	}
	if strings.HasSuffix(value, " ") || strings.HasSuffix(value, ".") {
		return fmt.Errorf("component has a trailing space or period")
	}
	if strings.Contains(value, ":") {
		return fmt.Errorf("component contains a colon")
	}
	if strings.ContainsAny(value, "<>\"|?*") {
		return fmt.Errorf("component contains a Windows-reserved character")
	}
	if windowsReserved.MatchString(value) {
		return fmt.Errorf("component uses a Windows reserved name")
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f || unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("component contains an unsupported control character")
		}
	}
	return nil
}

func Canonical(path Path, ignoreCase bool) string {
	value := string(path)
	if utf8.ValidString(value) {
		value = norm.NFC.String(value)
		if ignoreCase {
			return cases.Fold().String(value)
		}
		return value
	}

	// Public Git indexes can contain arbitrary non-NUL byte names on Unix.
	// ParseObserved preserves those names so collision scanning never makes an
	// otherwise valid public repository unusable. Unicode normalization and
	// folding cannot be applied to invalid UTF-8 without changing the byte
	// sequence, so preserve non-ASCII bytes and fold only ASCII letters.
	if !ignoreCase {
		return value
	}
	bytes := []byte(value)
	for index, character := range bytes {
		if character >= 'A' && character <= 'Z' {
			bytes[index] = character + ('a' - 'A')
		}
	}
	return string(bytes)
}

func (p Path) String() string {
	return string(p)
}

func (p Path) OSPath(root string) string {
	return filepath.Join(root, filepath.FromSlash(string(p)))
}

func InspectRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("only regular files are supported")
	}
	return nil
}

func ValidateNoSymlinkComponents(root string, path Path) error {
	current := root
	components := strings.Split(path.String(), "/")
	for index, component := range components {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%q traverses a symbolic link", path)
		}
		if index < len(components)-1 && !info.IsDir() {
			return fmt.Errorf("%q has a non-directory ancestor", path)
		}
	}
	return nil
}
