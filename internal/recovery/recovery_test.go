package recovery

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/getspas/spas/internal/pathmodel"
)

func TestStoreSavePreservesOwnerOnlyModes(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	publicRoot := t.TempDir()

	store, err := NewStore(dataDir, "lnk_test")
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if store.Used() {
		t.Fatal("store.Used() = true before any save")
	}

	plainRel, err := pathmodel.Parse("config/plain.env")
	if err != nil {
		t.Fatal(err)
	}
	execRel, err := pathmodel.Parse("scripts/run.sh")
	if err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Dir(plainRel.OSPath(publicRoot)), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plainRel.OSPath(publicRoot), []byte("SECRET=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Dir(execRel.OSPath(publicRoot)), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(execRel.OSPath(publicRoot), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	saved, err := store.Save(publicRoot, plainRel)
	if err != nil || !saved {
		t.Fatalf("store.Save(plain) = %v, %v; want true, nil", saved, err)
	}
	saved, err = store.Save(publicRoot, execRel)
	if err != nil || !saved {
		t.Fatalf("store.Save(exec) = %v, %v; want true, nil", saved, err)
	}

	if !store.Used() {
		t.Fatal("store.Used() = false after save")
	}

	if runtime.GOOS != "windows" {
		plainInfo, err := os.Stat(plainRel.OSPath(store.Root))
		if err != nil {
			t.Fatal(err)
		}
		if got := plainInfo.Mode().Perm(); got != 0o600 {
			t.Errorf("plain recovery copy mode = %o, want 0600", got)
		}

		execInfo, err := os.Stat(execRel.OSPath(store.Root))
		if err != nil {
			t.Fatal(err)
		}
		if got := execInfo.Mode().Perm(); got != 0o700 {
			t.Errorf("exec recovery copy mode = %o, want 0700", got)
		}

		dirInfo, err := os.Stat(filepath.Dir(plainRel.OSPath(store.Root)))
		if err != nil {
			t.Fatal(err)
		}
		if got := dirInfo.Mode().Perm(); got&0o077 != 0 {
			t.Errorf("recovery directory mode = %o, want no group/other access", got)
		}
	}
}

func TestStoreSaveIgnoresNonExistentAndNonRegular(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	publicRoot := t.TempDir()

	store, err := NewStore(dataDir, "lnk_test")
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	missingRel, err := pathmodel.Parse("missing.txt")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.Save(publicRoot, missingRel)
	if err != nil || saved {
		t.Fatalf("store.Save(missing) = %v, %v; want false, nil", saved, err)
	}

	dirRel, err := pathmodel.Parse("dir")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dirRel.OSPath(publicRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	saved, err = store.Save(publicRoot, dirRel)
	if err != nil || saved {
		t.Fatalf("store.Save(dir) = %v, %v; want false, nil", saved, err)
	}
}
