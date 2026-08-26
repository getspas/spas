//go:build windows

package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestIsRetryable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "windows ERROR_SHARING_VIOLATION",
			err:  windows.ERROR_SHARING_VIOLATION,
			want: true,
		},
		{
			name: "windows ERROR_ACCESS_DENIED",
			err:  windows.ERROR_ACCESS_DENIED,
			want: true,
		},
		{
			name: "syscall ERROR_SHARING_VIOLATION",
			err:  syscall.Errno(32),
			want: true,
		},
		{
			name: "syscall ERROR_ACCESS_DENIED",
			err:  syscall.Errno(5),
			want: true,
		},
		{
			name: "wrapped windows sharing violation",
			err:  fmt.Errorf("wrap: %w", windows.ERROR_SHARING_VIOLATION),
			want: true,
		},
		{
			name: "wrapped syscall access denied",
			err:  fmt.Errorf("wrap: %w", syscall.Errno(5)),
			want: true,
		},
		{
			name: "windows ERROR_FILE_NOT_FOUND",
			err:  windows.ERROR_FILE_NOT_FOUND,
			want: false,
		},
		{
			name: "windows ERROR_PATH_NOT_FOUND",
			err:  windows.ERROR_PATH_NOT_FOUND,
			want: false,
		},
		{
			name: "generic error",
			err:  errors.New("something went wrong"),
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isRetryable(tc.err); got != tc.want {
				t.Fatalf("isRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestReplaceSucceedsFirstAttempt(t *testing.T) {
	origMove := moveFileEx
	defer func() { moveFileEx = origMove }()

	var calls int32
	moveFileEx = func(from, to *uint16, flags uint32) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}

	if err := replace("src", "dst"); err != nil {
		t.Fatalf("replace() error = %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("moveFileEx calls = %d, want 1", got)
	}
}

func TestReplaceRetriesOnSharingViolationThenSucceeds(t *testing.T) {
	origMove := moveFileEx
	defer func() { moveFileEx = origMove }()

	var calls int32
	moveFileEx = func(from, to *uint16, flags uint32) error {
		call := atomic.AddInt32(&calls, 1)
		if call < 3 {
			return windows.ERROR_SHARING_VIOLATION
		}
		return nil
	}

	if err := replace("src", "dst"); err != nil {
		t.Fatalf("replace() error = %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("moveFileEx calls = %d, want 3", got)
	}
}

func TestReplaceRetriesOnAccessDeniedThenSucceeds(t *testing.T) {
	origMove := moveFileEx
	defer func() { moveFileEx = origMove }()

	var calls int32
	moveFileEx = func(from, to *uint16, flags uint32) error {
		call := atomic.AddInt32(&calls, 1)
		if call == 1 {
			return windows.ERROR_ACCESS_DENIED
		}
		return nil
	}

	if err := replace("src", "dst"); err != nil {
		t.Fatalf("replace() error = %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("moveFileEx calls = %d, want 2", got)
	}
}

func TestReplaceFailsImmediatelyOnNonRetryableError(t *testing.T) {
	origMove := moveFileEx
	defer func() { moveFileEx = origMove }()

	var calls int32
	moveFileEx = func(from, to *uint16, flags uint32) error {
		atomic.AddInt32(&calls, 1)
		return windows.ERROR_FILE_NOT_FOUND
	}

	err := replace("src", "dst")
	if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		t.Fatalf("replace() error = %v, want ERROR_FILE_NOT_FOUND", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("moveFileEx calls = %d, want 1 (should not retry)", got)
	}
}

func TestReplaceExhaustsRetriesOnPersistentSharingViolation(t *testing.T) {
	origMove := moveFileEx
	defer func() { moveFileEx = origMove }()

	var calls int32
	moveFileEx = func(from, to *uint16, flags uint32) error {
		atomic.AddInt32(&calls, 1)
		return windows.ERROR_SHARING_VIOLATION
	}

	err := replace("src", "dst")
	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("replace() error = %v, want ERROR_SHARING_VIOLATION", err)
	}
	wantCalls := int32(maxReplaceRetries + 1)
	if got := atomic.LoadInt32(&calls); got != wantCalls {
		t.Fatalf("moveFileEx calls = %d, want %d", got, wantCalls)
	}
}

func TestWriteIntegrationWindows(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if err := Write(path, []byte("version 1"), 0o600); err != nil {
		t.Fatalf("initial Write() error = %v", err)
	}
	if err := Write(path, []byte("version 2"), 0o600); err != nil {
		t.Fatalf("replacement Write() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "version 2" {
		t.Fatalf("content = %q, want %q", string(data), "version 2")
	}
}
