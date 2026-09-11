package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/getspas/spas/internal/gitexec"
	"github.com/getspas/spas/internal/spaserr"
)

func TestAddRejectsDistinctUnicodeEntries(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		directory bool
		hardLink  bool
		parent    bool
		nfc       bool
	}{
		{name: "selected file"},
		{name: "directory", directory: true},
		{name: "hard links", hardLink: true},
		{name: "directory hard links", directory: true, hardLink: true},
		{name: "parent directories", parent: true},
		{name: "recursive parent directories", directory: true, parent: true},
		{name: "NFC selection", nfc: true},
		{name: "NFC hard-link selection", nfc: true, hardLink: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			instance, publicRoot, _, _ := fixture(t)
			assets := filepath.Join(publicRoot, "assets")
			if err := os.Mkdir(assets, 0o700); err != nil {
				t.Fatal(err)
			}
			raw := filepath.Join(assets, "re\u0301sume\u0301.txt")
			nfc := filepath.Join(assets, "r\u00e9sum\u00e9.txt")
			if test.parent {
				rawParent := filepath.Join(assets, "cafe\u0301")
				nfcParent := filepath.Join(assets, "caf\u00e9")
				if err := os.Mkdir(rawParent, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(nfcParent, 0o700); err != nil {
					if errors.Is(err, os.ErrExist) {
						t.Skip("volume aliases Unicode normalization variants")
					}
					t.Fatal(err)
				}
				raw = filepath.Join(rawParent, "secret.txt")
				nfc = filepath.Join(nfcParent, "secret.txt")
			}
			if err := os.WriteFile(raw, []byte("selected secret\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if test.hardLink {
				if err := os.Link(raw, nfc); err != nil {
					if errors.Is(err, os.ErrExist) {
						t.Skip("volume aliases Unicode normalization variants")
					}
					t.Fatal(err)
				}
			} else {
				file, err := os.OpenFile(nfc, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
				if err != nil {
					if errors.Is(err, os.ErrExist) {
						t.Skip("volume aliases Unicode normalization variants")
					}
					t.Fatal(err)
				}
				if _, err := file.WriteString("other contents\n"); err != nil {
					file.Close()
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			}
			rawInfo, err := os.Stat(raw)
			if err != nil {
				t.Fatal(err)
			}
			nfcInfo, err := os.Stat(nfc)
			if err != nil {
				t.Fatal(err)
			}
			if os.SameFile(rawInfo, nfcInfo) != test.hardLink {
				t.Fatal("fixture file identities do not match the test case")
			}
			// An existing enrollment must survive a rejected second selection.
			if err := os.WriteFile(filepath.Join(publicRoot, "keep.txt"), []byte("keep\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := instance.Add(context.Background(), AddOptions{
				Paths: []string{"keep.txt"}, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip,
			}); err != nil {
				t.Fatal(err)
			}
			state := loadState(t, instance, publicRoot)
			unchanged := []string{
				filepath.Join(instance.Store.ConfigDir, "links", state.LinkID+".json"),
				filepath.Join(publicRoot, ".git", "info", "exclude"),
				filepath.Join(publicRoot, ".git", "config"),
				raw, nfc,
			}
			before := make(map[string][]byte)
			for _, path := range unchanged {
				before[path], err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			selection := raw
			if test.nfc {
				selection = nfc
			}
			if test.directory {
				selection = assets
			}
			err = instance.Add(context.Background(), AddOptions{
				Paths: []string{selection}, ExistingExclude: ExcludePreserve, MergeProtection: MergeEnable,
			})
			if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsupportedPath {
				t.Fatalf("Add() error = %v, want unsupported_path for distinct Unicode entries", err)
			}
			for _, path := range unchanged {
				after, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before[path], after) {
					t.Errorf("rejected Add changed %s", path)
				}
			}
		})
	}
}

func TestAddUnicodeSingleEntry(t *testing.T) {
	t.Parallel()

	for _, selection := range []string{"file", "directory", "case alias"} {
		t.Run(selection, func(t *testing.T) {
			t.Parallel()
			instance, publicRoot, _, _ := fixture(t)
			raw := filepath.Join(publicRoot, "cafe\u0301", "re\u0301sume\u0301.txt")
			nfc := filepath.Join(publicRoot, "caf\u00e9", "r\u00e9sum\u00e9.txt")
			if err := os.MkdirAll(filepath.Dir(raw), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(raw, []byte("single entry\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, aliasErr := os.Stat(nfc)
			if aliasErr != nil && !errors.Is(aliasErr, os.ErrNotExist) {
				t.Fatal(aliasErr)
			}
			selected := raw
			if selection == "directory" {
				selected = filepath.Dir(raw)
			} else if selection == "case alias" {
				selected = filepath.Join(publicRoot, "CAFE\u0301", "RE\u0301SUME\u0301.TXT")
				if _, err := os.Stat(selected); err != nil {
					if errors.Is(err, os.ErrNotExist) {
						t.Skip("volume distinguishes differently cased names")
					}
					t.Fatal(err)
				}
			}
			err := instance.Add(context.Background(), AddOptions{
				Paths: []string{selected}, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip,
			})
			if aliasErr != nil {
				if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsupportedPath {
					t.Fatalf("Add() = %v, want unsupported_path on a normalization-sensitive volume", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Add() rejected a genuine Unicode alias: %v", err)
			}
			state := loadState(t, instance, publicRoot)
			want := "caf\u00e9/r\u00e9sum\u00e9.txt"
			if len(state.PendingAdds) != 1 || state.PendingAdds[0] != want {
				t.Fatalf("PendingAdds = %q, want canonical spelling", state.PendingAdds)
			}
			result, err := (gitexec.Runner{}).Run(context.Background(), publicRoot,
				"ls-files", "--others", "--exclude-standard", "-z")
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Stdout) != 0 {
				t.Fatalf("selected file remains visible to Git: %q", result.Stdout)
			}
		})
	}
}

func TestAddUsesDirectoryEntrySpelling(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, stored, selected string
	}{
		{"NFC case alias", "café/résumé.txt", "CAFÉ/RÉSUMÉ.TXT"},
		{"uppercase entry", "CAFÉ/RÉSUMÉ.TXT", "café/résumé.txt"},
		{"parent alias", "café/résumé.txt", "CAFÉ/résumé.txt"},
		{"directory alias", "café/résumé.txt", "CAFÉ"},
		{"ASCII alias", "assets/secret.txt", "ASSETS/SECRET.TXT"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			instance, publicRoot, _, _ := fixture(t)
			stored := filepath.Join(publicRoot, filepath.FromSlash(test.stored))
			if err := os.MkdirAll(filepath.Dir(stored), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(stored, []byte("selected secret\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			selected := filepath.Join(publicRoot, filepath.FromSlash(test.selected))
			if _, err := os.Stat(selected); errors.Is(err, os.ErrNotExist) {
				t.Skip("volume distinguishes the selected case spelling")
			} else if err != nil {
				t.Fatal(err)
			}
			if err := instance.Add(t.Context(), AddOptions{
				Paths: []string{selected}, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip,
			}); err != nil {
				t.Fatal(err)
			}
			state := loadState(t, instance, publicRoot)
			if len(state.PendingAdds) != 1 || state.PendingAdds[0] != test.stored {
				t.Fatalf("PendingAdds = %q, want %q", state.PendingAdds, test.stored)
			}
			result, err := instance.Git.Run(t.Context(), publicRoot, "ls-files", "--others", "--exclude-standard", "-z")
			if err != nil || len(result.Stdout) != 0 {
				t.Fatalf("ordinary Git visibility = %q, %v", result.Stdout, err)
			}
			data, err := os.ReadFile(stored)
			if err != nil || string(data) != "selected secret\n" {
				t.Fatalf("selected content = %q, %v", data, err)
			}
			if test.name == "directory alias" {
				selected = filepath.Join(selected, "résumé.txt")
			}
			if err := instance.Diff(t.Context(), DiffOptions{Paths: []string{selected}}); err != nil {
				t.Fatalf("Diff(alias): %v", err)
			}
			if err := instance.Remove(t.Context(), RemoveOptions{Paths: []string{selected}}); err != nil {
				t.Fatalf("Remove(alias): %v", err)
			}
			if state := loadState(t, instance, publicRoot); len(state.PendingAdds) != 0 {
				t.Fatalf("Remove(alias) retained pending paths: %q", state.PendingAdds)
			}
		})
	}
}
