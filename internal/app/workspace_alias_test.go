package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/getspas/spas/internal/spaserr"
)

func TestExpandPathsThroughWorkspaceAlias(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "assets", "secret.env"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	instance := App{PathBase: alias}
	for _, selected := range []string{"assets/secret.env", "assets"} {
		paths, err := instance.expandPaths(workspace, root, []string{filepath.Join(alias, filepath.FromSlash(selected))})
		if err != nil {
			t.Fatal(err)
		}
		if len(paths) != 1 || paths[0] != "assets/secret.env" {
			t.Fatalf("expandPaths(%q) = %q", selected, paths)
		}
	}
	link := filepath.Join(workspace, "link")
	if err := os.Symlink("assets", link); err != nil {
		t.Fatal(err)
	}
	_, err := instance.expandPaths(workspace, root, []string{filepath.Join(alias, "link", "secret.env")})
	if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsupportedPath {
		t.Fatalf("expandPaths(managed symlink) = %v, want KindUnsupportedPath", err)
	}
}

func TestExpandPathsThroughAliasPreservesUnicodeValidation(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	raw := filepath.Join(workspace, "cafe\u0301.env")
	nfc := filepath.Join(workspace, "caf\u00e9.env")
	if err := os.WriteFile(raw, []byte("selected"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nfc, []byte("normalized"), 0o600); err != nil {
		t.Fatal(err)
	}
	rawInfo, err := os.Stat(raw)
	if err != nil {
		t.Fatal(err)
	}
	nfcInfo, err := os.Stat(nfc)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := (App{PathBase: alias}).expandPaths(workspace, workspace,
		[]string{filepath.Join(alias, "cafe\u0301.env")})
	if os.SameFile(rawInfo, nfcInfo) {
		if err != nil || len(paths) != 1 || paths[0] != "caf\u00e9.env" {
			t.Fatalf("expandPaths(genuine Unicode alias) = %q, %v", paths, err)
		}
	} else if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsupportedPath {
		t.Fatalf("expandPaths(distinct Unicode entries) = %q, %v; want KindUnsupportedPath", paths, err)
	}
}
