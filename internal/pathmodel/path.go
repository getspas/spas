package pathmodel

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
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
		return "", fmt.Errorf("path contains a control character that SPAS cannot represent in exclusion rules")
	}
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("SPAS state and exclusion rules require valid UTF-8 paths")
	}
	if runtime.GOOS == "windows" && strings.Contains(value, `\`) {
		return "", fmt.Errorf("path must use Git slash separators")
	}

	if strings.HasPrefix(value, "/") || filepath.IsAbs(value) || filepath.VolumeName(value) != "" {
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
// reject a name unsupported by SPAS or the current host, because a public
// repository may track such files (device names, unusual Unicode). The original
// spelling is preserved exactly so pathspecs
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
	// Windows filesystem APIs replace malformed UTF-8 when converting to UTF-16.
	// Reject it before observing a different path through those APIs.
	if !utf8.ValidString(value) {
		return "", "", fmt.Errorf("SPAS state and exclusion rules require valid UTF-8 paths")
	}
	publicRoot, err := filepath.Abs(publicRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve public workspace: %w", err)
	}
	workspace, err := os.Stat(publicRoot)
	if err != nil {
		return "", "", fmt.Errorf("inspect public workspace: %w", err)
	}
	if filepath.IsAbs(value) {
		base = ""
	}
	absolute, err := filepath.Abs(filepath.Join(base, value))
	if err != nil {
		return "", "", fmt.Errorf("resolve path %q: %w", value, err)
	}
	var prefixes []string
	for prefix := absolute; ; prefix = filepath.Dir(prefix) {
		prefixes = append(prefixes, prefix)
		if filepath.Dir(prefix) == prefix {
			break
		}
	}
	// Match the workspace from the filesystem root inward, before inspecting
	// managed components. Rebase only that prefix so Unicode spelling, missing
	// entries, and managed symlinks remain visible to subsequent validation.
	for _, prefix := range slices.Backward(prefixes) {
		info, err := os.Stat(prefix)
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			return "", "", fmt.Errorf("inspect path prefix: %w", err)
		}
		if !os.SameFile(workspace, info) {
			continue
		}
		relative, err := filepath.Rel(prefix, absolute)
		if err != nil {
			return "", "", fmt.Errorf("make path relative to public workspace: %w", err)
		}
		path, err := Parse(filepath.ToSlash(relative))
		if err != nil {
			return "", "", err
		}
		return path, filepath.Join(publicRoot, relative), nil
	}
	return "", "", fmt.Errorf("path must stay inside the public workspace")
}

func validateComponent(value string) error {
	if value == "" || value == "." || value == ".." {
		return fmt.Errorf("empty or traversal component")
	}
	// Win32 aliases trailing dots/spaces and device names, and a colon can
	// address an alternate data stream. Reject those forms on Windows before
	// SPAS writes a different file than the literal Git path.
	if runtime.GOOS == "windows" {
		if strings.HasSuffix(value, " ") || strings.HasSuffix(value, ".") {
			return fmt.Errorf("component has a trailing space or period unsupported by Windows")
		}
		if strings.ContainsAny(value, ":<>\"|?*") {
			return fmt.Errorf("component contains a character unsupported by Windows")
		}
		if windowsReserved.MatchString(value) {
			return fmt.Errorf("component uses a Windows reserved name")
		}
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f || unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("component contains a control or format character unsupported by SPAS")
		}
	}
	return nil
}

// Folded groups candidate aliases for read-only lookup. Membership in a folded
// group does not establish filesystem equivalence or asset identity.
func Folded(path Path) string {
	value := string(path)
	if utf8.ValidString(value) {
		return cases.Fold().String(norm.NFC.String(value))
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
