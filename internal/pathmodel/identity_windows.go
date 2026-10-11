package pathmodel

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func unsupportedObservedName(err error) bool {
	return os.IsNotExist(err) || errors.Is(err, windows.ERROR_INVALID_NAME) || errors.Is(err, windows.ERROR_FILENAME_EXCED_RANGE)
}
