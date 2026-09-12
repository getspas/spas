package gitexec

import (
	"fmt"
	"strings"
)

// ParsePathOutput reads one raw pathname printed by a Git path query. Only
// the terminating LF belongs to the protocol; whitespace in the name remains.
func ParsePathOutput(output []byte) (string, error) {
	path, terminated := strings.CutSuffix(string(output), "\n")
	if !terminated || path == "" || strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("Git returned a malformed pathname")
	}
	return path, nil
}
