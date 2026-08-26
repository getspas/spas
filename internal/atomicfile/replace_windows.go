//go:build windows

package atomicfile

import (
	"errors"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

const maxReplaceRetries = 3

var replaceRetryDelays = [...]time.Duration{
	10 * time.Millisecond,
	25 * time.Millisecond,
	50 * time.Millisecond,
}

var moveFileEx = windows.MoveFileEx

func isRetryable(err error) bool {
	if errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return true
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == 32 || errno == 5
	}
	var winErrno windows.Errno
	if errors.As(err, &winErrno) {
		return winErrno == windows.ERROR_SHARING_VIOLATION || winErrno == windows.ERROR_ACCESS_DENIED
	}
	return false
}

func replace(source, destination string) error {
	sourcePtr, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPtr, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}

	flags := uint32(windows.MOVEFILE_REPLACE_EXISTING | windows.MOVEFILE_WRITE_THROUGH)
	var lastErr error
	for attempt := 0; attempt <= maxReplaceRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(replaceRetryDelays[attempt-1])
		}
		lastErr = moveFileEx(sourcePtr, destinationPtr, flags)
		if lastErr == nil {
			return nil
		}
		if !isRetryable(lastErr) {
			return lastErr
		}
	}
	return lastErr
}
