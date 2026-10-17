package app

import (
	"os"
	"path/filepath"
	"testing"
)

func createRemoteWithInitialCommit(t *testing.T, root, remote string) {
	t.Helper()
	source := filepath.Join(root, "remote-source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "--bare", "-q", remote)
	runGit(t, source, "init", "-q", "-b", "main")
	runGit(t, source, "config", "user.name", "Source Test")
	runGit(t, source, "config", "user.email", "source@example.invalid")
	if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "file.txt")
	runGit(t, source, "commit", "-q", "-m", "initial")
	runGit(t, source, "remote", "add", "origin", remote)
	runGit(t, source, "push", "-q", "origin", "main")
	runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
}
