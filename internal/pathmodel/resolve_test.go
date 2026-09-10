package pathmodel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWorkspaceAliasPreservesSelectedComponents(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	for _, selection := range []string{"secret.env", "cafe\u0301/re\u0301sume\u0301.txt", "missing/parent/file"} {
		for _, absoluteInput := range []bool{false, true} {
			value := filepath.FromSlash(selection)
			if absoluteInput {
				value = filepath.Join(alias, value)
			}
			path, observed, err := Resolve(workspace, alias, value)
			if err != nil {
				t.Fatalf("Resolve(%q): %v", value, err)
			}
			wantPath := selection
			if selection == "cafe\u0301/re\u0301sume\u0301.txt" {
				wantPath = "caf\u00e9/r\u00e9sum\u00e9.txt"
			}
			if path.String() != wantPath || observed != filepath.Join(workspace, filepath.FromSlash(selection)) {
				t.Fatalf("Resolve(%q) = %q, %q", value, path, observed)
			}
		}
	}
	if _, _, err := Resolve(workspace, alias, filepath.Join(root, "outside.env")); err == nil {
		t.Fatal("Resolve accepted an outside selection")
	}
}

func TestResolvePreservesManagedDirectorySymlinks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{workspace, root} {
		link := filepath.Join(workspace, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symbolic links unavailable: %v", err)
		}
		for _, absoluteInput := range []bool{false, true} {
			value := "file.env"
			if absoluteInput {
				value = filepath.Join(link, value)
			}
			path, observed, err := Resolve(workspace, link, value)
			if err != nil {
				t.Fatal(err)
			}
			if path != "link/file.env" || observed != filepath.Join(link, "file.env") {
				t.Fatalf("Resolve() = %q, %q; managed directory symlink was followed", path, observed)
			}
			if err := ValidateNoSymlinkComponents(workspace, path); err == nil {
				t.Fatal("managed directory symlink escaped validation")
			}
		}
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
	}
}
