package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLinkAndAddPreserveWhitespaceWorkspaceIdentity(t *testing.T) {
	t.Parallel()
	instance, neighbor, _, _ := fixture(t)
	state := loadState(t, instance, neighbor)
	neighborFiles := []string{
		filepath.Join(instance.Store.ConfigDir, "links", state.LinkID+".json"),
		filepath.Join(neighbor, ".git", "config"),
		filepath.Join(neighbor, ".git", "info", "exclude"),
	}
	before := make(map[string][]byte)
	for _, path := range neighborFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = data
	}
	selected := neighbor + "\u00a0"
	if err := os.Mkdir(selected, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, selected, "init", "-q", "-b", "main")
	instance.RepoHint, instance.PathBase = selected, selected
	if err := instance.Link(context.Background(), LinkOptions{Repository: "getspas/private-files", Branch: "main"}); err != nil {
		t.Fatalf("Link() selected the wrong workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(selected, "secret.txt"), []byte("selected secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Add(context.Background(), AddOptions{
		Paths: []string{"secret.txt"}, ExistingExclude: ExcludePreserve, MergeProtection: MergeSkip,
	}); err != nil {
		t.Fatal(err)
	}
	selectedState := loadState(t, instance, selected)
	expectedRoot, err := filepath.EvalSymlinks(selected)
	if err != nil {
		t.Fatal(err)
	}
	if selectedState.LinkID == state.LinkID || selectedState.Public.Root != expectedRoot {
		t.Fatalf("selected state identity = %q, %q", selectedState.LinkID, selectedState.Public.Root)
	}
	for _, path := range neighborFiles {
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before[path], after) {
			t.Errorf("operation changed neighboring workspace file %q", path)
		}
	}
	excluded, err := os.ReadFile(filepath.Join(selected, ".git", "info", "exclude"))
	if err != nil || !bytes.Contains(excluded, []byte("/secret.txt\n")) {
		t.Fatalf("selected workspace exclusion = %q, %v", excluded, err)
	}
}
