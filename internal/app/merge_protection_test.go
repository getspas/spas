package app

import (
	"context"
	"testing"

	"github.com/getspas/spas/internal/spaserr"
)

func TestMergeProtectionPoliciesRejectConflictingOptions(t *testing.T) {
	t.Parallel()
	instance, root, _, _ := fixture(t)
	repository, err := instance.publicRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "config", "branch.main.mergeOptions", "--no-overwrite-ignore --overwrite-ignore")
	for _, policy := range []MergeProtectionPolicy{MergeEnable, MergeRequire} {
		if action, err := instance.planMergeProtection(context.Background(), repository, policy); err == nil {
			t.Errorf("policy %q accepted conflicting options: %q", policy, action)
		} else if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsafeGitState {
			t.Errorf("policy %q error = %v, want unsafe_git_state", policy, err)
		}
	}
}
