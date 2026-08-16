package appdirs

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestDefaultUsesPlatformNativeDurableLocations(t *testing.T) {
	home := t.TempDir()
	configRoot := filepath.Join(home, "configured")
	dataRoot := filepath.Join(home, "durable")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_DATA_HOME", dataRoot)
	t.Setenv("APPDATA", configRoot)
	t.Setenv("LOCALAPPDATA", dataRoot)

	got, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	var wantConfig, wantData string
	switch runtime.GOOS {
	case "windows":
		wantConfig = filepath.Join(configRoot, "spas")
		wantData = filepath.Join(dataRoot, "spas")
	case "darwin":
		root := filepath.Join(home, "Library", "Application Support")
		wantConfig = filepath.Join(root, "spas")
		wantData = filepath.Join(root, "spas")
	case "linux":
		wantConfig = filepath.Join(configRoot, "spas")
		wantData = filepath.Join(dataRoot, "spas")
	default:
		t.Skip("SPAS does not support this platform")
	}
	if got.Config != wantConfig || got.Data != wantData {
		t.Fatalf("Default() = %#v, want config %q and data %q", got, wantConfig, wantData)
	}
}

func TestDefaultRejectsRelativeDurableRoots(t *testing.T) {
	switch runtime.GOOS {
	case "windows":
		t.Setenv("APPDATA", "relative-config")
		t.Setenv("LOCALAPPDATA", "relative-data")
	case "darwin":
		t.Setenv("HOME", "relative-home")
	case "linux":
		t.Setenv("HOME", t.TempDir())
		t.Setenv("XDG_CONFIG_HOME", "relative-config")
		t.Setenv("XDG_DATA_HOME", "relative-data")
	default:
		t.Skip("SPAS does not support this platform")
	}

	if _, err := Default(); err == nil {
		t.Fatal("Default() error = nil, want relative durable-root rejection")
	}
}
