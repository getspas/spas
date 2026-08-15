package mergeprotect

import (
	"context"
	"testing"
)

// Every documented pull.rebase value must produce a warning decision, never
// an error: `merges`, `preserve`, and `interactive` are not booleans and
// previously made doctor fail on exactly the configurations most at risk.
func TestRebaseWarningHandlesEveryDocumentedValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value string
		want  bool
	}{
		{"true", true},
		{"yes", true},
		{"on", true},
		{"1", true},
		{"merges", true},
		{"preserve", true},
		{"interactive", true},
		{"some-future-mode", true},
		{"false", false},
		{"no", false},
		{"off", false},
		{"0", false},
	}
	ctx := context.Background()
	for _, test := range tests {
		test := test
		t.Run(test.value, func(t *testing.T) {
			t.Parallel()
			repository := testRepository(t)
			runGit(t, repository.Root, "config", "--local", "pull.rebase", test.value)
			got, err := RebaseWarning(ctx, repository, "main")
			if err != nil {
				t.Fatalf("RebaseWarning(%q) error = %v, want none", test.value, err)
			}
			if got != test.want {
				t.Fatalf("RebaseWarning(%q) = %t, want %t", test.value, got, test.want)
			}
		})
	}
}

// branch.<name>.rebase takes precedence over pull.rebase, matching Git.
func TestRebaseWarningBranchPrecedence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository := testRepository(t)
	runGit(t, repository.Root, "config", "--local", "pull.rebase", "true")
	runGit(t, repository.Root, "config", "--local", "branch.main.rebase", "false")
	got, err := RebaseWarning(ctx, repository, "main")
	if err != nil {
		t.Fatalf("RebaseWarning() error = %v", err)
	}
	if got {
		t.Fatal("RebaseWarning() = true; branch.main.rebase=false must take precedence")
	}
}
