//go:build !windows

package pathmodel

import (
	"errors"
	"os"
	"syscall"
)

func unsupportedObservedName(err error) bool {
	return os.IsNotExist(err) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENAMETOOLONG)
}
