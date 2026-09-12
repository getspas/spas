//go:build windows

package atomicfile

import (
	"errors"
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
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED)
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
