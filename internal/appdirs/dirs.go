package appdirs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

type Dirs struct {
	Config string
	Data   string
}

func Default() (Dirs, error) {
	configRoot, err := os.UserConfigDir()
	if err != nil {
		return Dirs{}, err
	}
	configRoot, err = requireAbsolute("user configuration directory", configRoot)
	if err != nil {
		return Dirs{}, err
	}

	dataRoot, err := dataRoot()
	if err != nil {
		return Dirs{}, err
	}
	dataRoot, err = requireAbsolute("user data directory", dataRoot)
	if err != nil {
		return Dirs{}, err
	}

	return Dirs{
		Config: filepath.Join(configRoot, "spas"),
		Data:   filepath.Join(dataRoot, "spas"),
	}, nil
}

func requireAbsolute(name, value string) (string, error) {
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s must be an absolute path: %q", name, value)
	}
	return filepath.Clean(value), nil
}

func dataRoot() (string, error) {
	switch runtime.GOOS {
	case "windows":
		if value := os.Getenv("LOCALAPPDATA"); value != "" {
			return value, nil
		}
		return os.UserConfigDir()
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support"), nil
	case "linux":
		if value := os.Getenv("XDG_DATA_HOME"); value != "" {
			return value, nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share"), nil
	default:
		return "", errors.New("spas supports Windows, macOS, and Linux")
	}
}
