package lock

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/getspas/spas/internal/spaserr"
)

type Lock struct {
	path string
	file *os.File
}

type metadata struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"startedAt"`
}

func Acquire(dir, name string) (*Lock, error) {
	return AcquirePath(filepath.Join(dir, name+".lock"), "link lock")
}

func AcquirePath(path, resourceDescription string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	acquired, err := tryLock(file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !acquired {
		_ = file.Close()
		if resourceDescription == "" {
			resourceDescription = "resource"
		}
		return nil, spaserr.Wrap(
			spaserr.KindLockHeld,
			fmt.Errorf("another SPAS operation holds the %s at %s", resourceDescription, path),
		)
	}
	cleanup := func(primary error) error {
		return errors.Join(primary, unlock(file), file.Close())
	}
	if err := file.Truncate(0); err != nil {
		return nil, cleanup(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		return nil, cleanup(err)
	}
	value := metadata{PID: os.Getpid(), StartedAt: time.Now().UTC()}
	if err := json.NewEncoder(file).Encode(value); err != nil {
		return nil, cleanup(err)
	}
	if err := file.Sync(); err != nil {
		return nil, cleanup(err)
	}
	return &Lock{path: path, file: file}, nil
}

func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	return errors.Join(unlock(l.file), l.file.Close())
}
