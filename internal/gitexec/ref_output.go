package gitexec

import (
	"fmt"
	"strings"
)

// ParseRefOutput reads one LF-terminated ref and removes its exact namespace.
// An empty namespace preserves a literal branch name or complete ref.
func ParseRefOutput(output []byte, namespace string) (string, error) {
	ref, terminated := strings.CutSuffix(string(output), "\n")
	name, matches := strings.CutPrefix(ref, namespace)
	if !terminated || !matches || name == "" || strings.ContainsAny(name, "\x00\r\n") {
		return "", fmt.Errorf("Git returned an invalid reference for namespace %q", namespace)
	}
	return name, nil
}
