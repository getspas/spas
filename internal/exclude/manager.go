package exclude

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/getspas/spas/internal/atomicfile"
	"github.com/getspas/spas/internal/filesync"
	"github.com/getspas/spas/internal/lock"
	"github.com/getspas/spas/internal/pathmodel"
	"github.com/getspas/spas/internal/spaserr"
)

type Plan struct {
	Path              string
	Before            []byte
	BeforeExists      bool
	After             []byte
	AfterExists       bool
	ExistingUserRules bool
	Changed           bool
	linkID            string
	managed           []pathmodel.Path
	originalBlock     []byte
	originalBlockSeen bool
}

func Build(path, linkID string, managed []pathmodel.Path) (Plan, error) {
	before, beforeExists, _, err := readRegularOrMissing(path)
	if err != nil {
		return Plan{}, wrapValidationError(err)
	}

	plan, err := buildPlan(path, before, beforeExists, linkID, managed)
	if err != nil {
		return Plan{}, wrapValidationError(err)
	}
	return plan, nil
}

func buildPlan(path string, before []byte, beforeExists bool, linkID string, managed []pathmodel.Path) (Plan, error) {
	start := []byte("# BEGIN SPAS link=" + linkID)
	end := []byte("# END SPAS link=" + linkID)
	startIndex, _, blockEnd, err := locateBlock(before, start, end)
	if err != nil {
		return Plan{}, err
	}

	newline := []byte("\n")
	if bytes.Contains(before, []byte("\r\n")) {
		newline = []byte("\r\n")
	}
	block := buildBlock(start, end, newline, managed)

	var after []byte
	existingUserRules := false
	if startIndex >= 0 {
		after = append(after, before[:startIndex]...)
		if len(managed) > 0 {
			after = append(after, block...)
		}
		after = append(after, before[blockEnd:]...)
		existingUserRules = hasRules(append(append([]byte{}, before[:startIndex]...), before[blockEnd:]...))
	} else {
		existingUserRules = hasRules(before)
		after = append(after, before...)
		if len(managed) > 0 {
			if len(after) > 0 && !bytes.HasSuffix(after, []byte("\n")) {
				after = append(after, newline...)
			}
			if len(after) > 0 && !bytes.HasSuffix(after, append(newline, newline...)) {
				after = append(after, newline...)
			}
			after = append(after, block...)
		}
	}

	afterExists := beforeExists || len(after) > 0
	plan := Plan{
		Path:              path,
		Before:            before,
		BeforeExists:      beforeExists,
		After:             after,
		AfterExists:       afterExists,
		ExistingUserRules: existingUserRules,
		Changed:           beforeExists != afterExists || !bytes.Equal(before, after),
		linkID:            linkID,
		managed:           append([]pathmodel.Path{}, managed...),
	}
	if startIndex >= 0 {
		plan.originalBlock = append([]byte{}, before[startIndex:blockEnd]...)
		plan.originalBlockSeen = true
	}
	return plan, nil
}

func Apply(plan Plan) error {
	return wrapValidationError(withLock(plan.Path, func() error {
		current, currentExists, currentMode, err := readRegularOrMissing(plan.Path)
		if err != nil {
			return err
		}
		latest, err := buildPlan(plan.Path, current, currentExists, plan.linkID, plan.managed)
		if err != nil {
			return err
		}
		return replaceDocument(plan.Path, currentExists, currentMode, latest.After, latest.AfterExists)
	}))
}

func Restore(plan Plan) error {
	return wrapValidationError(withLock(plan.Path, func() error {
		current, currentExists, currentMode, err := readRegularOrMissing(plan.Path)
		if err != nil {
			return err
		}
		desired, desiredExists, err := restoreManagedBlock(current, plan)
		if err != nil {
			return err
		}
		return replaceDocument(plan.Path, currentExists, currentMode, desired, desiredExists)
	}))
}

func replaceDocument(path string, currentExists bool, currentMode os.FileMode, desired []byte, desiredExists bool) error {
	if !desiredExists {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove public repository local exclude file: %w", err)
		}
		return nil
	}

	// A pre-existing exclude file keeps its permissions; only a file SPAS
	// creates itself defaults to owner-only.
	mode := os.FileMode(0o600)
	if currentExists {
		mode = currentMode.Perm()
	}
	if err := atomicfile.Write(path, desired, mode); err != nil {
		return fmt.Errorf("replace public repository local exclude file: %w", err)
	}
	return nil
}

func withLock(path string, operation func() error) (returnErr error) {
	lockPath := path + ".spas.lock"
	fileLock, err := lock.AcquirePath(lockPath, "public repository local exclude lock")
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := fileLock.Release(); releaseErr != nil {
			if returnErr == nil {
				returnErr = releaseErr
			} else {
				returnErr = errors.Join(returnErr, releaseErr)
			}
		}
	}()
	return operation()
}

func wrapValidationError(err error) error {
	if err == nil {
		return nil
	}
	if _, typed := spaserr.KindOf(err); typed {
		return err
	}
	return spaserr.Wrap(spaserr.KindExclusionValidation, err)
}

func restoreManagedBlock(document []byte, plan Plan) ([]byte, bool, error) {
	start := []byte("# BEGIN SPAS link=" + plan.linkID)
	end := []byte("# END SPAS link=" + plan.linkID)
	startIndex, _, blockEnd, err := locateBlock(document, start, end)
	if err != nil {
		return nil, false, err
	}
	result := append([]byte{}, document...)
	if startIndex >= 0 {
		result = append(append(append([]byte{}, document[:startIndex]...), plan.originalBlock...), document[blockEnd:]...)
	} else if plan.originalBlockSeen {
		newline := []byte("\n")
		if bytes.Contains(document, []byte("\r\n")) {
			newline = []byte("\r\n")
		}
		if len(result) > 0 && !bytes.HasSuffix(result, []byte("\n")) {
			result = append(result, newline...)
		}
		if len(result) > 0 && !bytes.HasSuffix(result, append(newline, newline...)) {
			result = append(result, newline...)
		}
		result = append(result, plan.originalBlock...)
	}
	return result, plan.BeforeExists || len(result) > 0, nil
}

func readRegularOrMissing(path string) ([]byte, bool, os.FileMode, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, 0, nil
	}
	if err != nil {
		return nil, false, 0, fmt.Errorf("inspect public repository local exclude file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, false, 0, fmt.Errorf("public repository local exclude path is not a regular file")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, false, 0, fmt.Errorf("read public repository local exclude file: %w", err)
	}
	return content, true, info.Mode(), nil
}

func locateBlock(document, start, end []byte) (int, int, int, error) {
	var starts []int
	var ends []int
	for lineStart := 0; lineStart <= len(document); {
		lineEnd := bytes.IndexByte(document[lineStart:], '\n')
		next := len(document) + 1
		if lineEnd < 0 {
			lineEnd = len(document)
		} else {
			lineEnd += lineStart
			next = lineEnd + 1
		}
		contentEnd := lineEnd
		if contentEnd > lineStart && document[contentEnd-1] == '\r' {
			contentEnd--
		}
		line := document[lineStart:contentEnd]
		switch {
		case bytes.Equal(line, start):
			starts = append(starts, lineStart)
		case bytes.Equal(line, end):
			ends = append(ends, lineStart)
		}
		if next > len(document) {
			break
		}
		lineStart = next
	}

	if len(starts) == 0 {
		if len(ends) != 0 {
			return -1, -1, -1, fmt.Errorf("malformed SPAS block in public repository local exclude file")
		}
		return -1, -1, -1, nil
	}
	if len(starts) != 1 {
		return -1, -1, -1, fmt.Errorf("duplicate SPAS blocks in public repository local exclude file")
	}
	if len(ends) == 0 {
		return -1, -1, -1, fmt.Errorf("unterminated SPAS block in public repository local exclude file")
	}
	if len(ends) != 1 {
		return -1, -1, -1, fmt.Errorf("duplicate SPAS block endings in public repository local exclude file")
	}
	startIndex := starts[0]
	endIndex := ends[0]
	if endIndex <= startIndex {
		return -1, -1, -1, fmt.Errorf("malformed SPAS block in public repository local exclude file")
	}
	blockEnd := endIndex + len(end)
	if blockEnd < len(document) && document[blockEnd] == '\r' {
		blockEnd++
	}
	if blockEnd < len(document) && document[blockEnd] == '\n' {
		blockEnd++
	}
	return startIndex, endIndex, blockEnd, nil
}

func buildBlock(start, end, newline []byte, managed []pathmodel.Path) []byte {
	sorted := append([]pathmodel.Path{}, managed...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var result bytes.Buffer
	result.Write(start)
	result.Write(newline)
	result.WriteByte('/')
	result.WriteString(filesync.ManagedTempDirectory)
	result.WriteByte('/')
	result.Write(newline)
	for _, path := range sorted {
		result.WriteByte('/')
		result.WriteString(escape(path.String()))
		result.Write(newline)
	}
	result.Write(end)
	result.Write(newline)
	return result.Bytes()
}

func escape(path string) string {
	var result strings.Builder
	for _, r := range path {
		switch r {
		case '\\', '*', '?', '[', ']', ' ', '#', '!':
			result.WriteByte('\\')
		}
		result.WriteRune(r)
	}
	return result.String()
}

func hasRules(document []byte) bool {
	for _, line := range strings.Split(strings.ReplaceAll(string(document), "\r\n", "\n"), "\n") {
		value := strings.TrimSpace(line)
		if value != "" && !strings.HasPrefix(value, "#") {
			return true
		}
	}
	return false
}
