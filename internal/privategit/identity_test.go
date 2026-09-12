package privategit

import (
	"context"
	"path/filepath"
	"testing"
)

func TestVerifyLayoutPreservesRootWhitespace(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	root := filepath.Join(parent, "checkout\u00a0")
	runGit(t, parent, "init", "-q", root)
	repository := Repository{Path: root}
	if err := repository.verifyLayout(context.Background()); err != nil {
		t.Fatalf("verifyLayout() changed the checkout identity: %v", err)
	}
}
