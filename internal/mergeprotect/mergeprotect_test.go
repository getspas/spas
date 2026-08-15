package mergeprotect

import (
	"context"
	"strings"
	"testing"

	"github.com/getspas/spas/internal/gitexec"
	"github.com/getspas/spas/internal/linkstate"
	"github.com/getspas/spas/internal/publicgit"
	"github.com/getspas/spas/internal/spaserr"
)

func TestEnablePreservesExistingOptionsAndRestore(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository := testRepository(t)
	runGit(t, repository.Root, "config", "--local", "branch.main.mergeOptions", "--no-edit --log")
	state := linkstate.State{Merge: linkstate.Merge{ManagedBranches: map[string]linkstate.ManagedBranch{}}}

	status, err := Enable(ctx, repository, &state)
	if err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	if !status.Enabled || status.Value != "--no-edit --log --no-overwrite-ignore" {
		t.Fatalf("Enable() = %#v", status)
	}
	if got := config(t, repository, "branch.main.mergeOptions"); got != status.Value {
		t.Fatalf("configured mergeOptions = %q", got)
	}

	if err := Restore(ctx, repository, state); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if got := config(t, repository, "branch.main.mergeOptions"); got != "--no-edit --log" {
		t.Fatalf("restored mergeOptions = %q", got)
	}
}

func TestRestorePreservesUserChangedValue(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository := testRepository(t)
	state := linkstate.State{Merge: linkstate.Merge{ManagedBranches: map[string]linkstate.ManagedBranch{}}}
	if _, err := Enable(ctx, repository, &state); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository.Root, "config", "--local", "branch.main.mergeOptions", "--no-ff")
	if err := Restore(ctx, repository, state); err != nil {
		t.Fatal(err)
	}
	if got := config(t, repository, "branch.main.mergeOptions"); got != "--no-ff" {
		t.Fatalf("Restore() overwrote user value: %q", got)
	}
}

func TestInspectReportsMultipleMergeOptionValuesAsAmbiguous(t *testing.T) {
	t.Parallel()

	// Multiple existing values must never make read-only inspection fail;
	// they are reported as ambiguous and SPAS never rewrites them.
	repository := testRepository(t)
	runGit(t, repository.Root, "config", "--local", "--add", "branch.main.mergeOptions", "--no-edit")
	runGit(t, repository.Root, "config", "--local", "--add", "branch.main.mergeOptions", "--log")
	status, err := Inspect(context.Background(), repository)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if !status.Ambiguous || status.Enabled {
		t.Fatalf("Inspect() = %+v, want ambiguous status", status)
	}

	state := linkstate.State{Merge: linkstate.Merge{ManagedBranches: map[string]linkstate.ManagedBranch{}}}
	enabled, err := Enable(context.Background(), repository, &state)
	if err == nil {
		t.Fatalf("Enable() error = nil, status = %+v", enabled)
	}
	if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsafeGitState {
		t.Fatalf("Enable() error = %v, kind = %v, want KindUnsafeGitState", err, kind)
	}
	if len(state.Merge.ManagedBranches) != 0 {
		t.Fatalf("Enable() rewrote ambiguous mergeOptions: %+v", state.Merge.ManagedBranches)
	}
	result, err := repository.Git.Run(context.Background(), repository.Root, "config", "--local", "--get-all", "branch.main.mergeOptions")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(result.Stdout)); got != "--no-edit\n--log" {
		t.Fatalf("Enable() modified user configuration: %q", got)
	}
}

func TestRebaseWarningReadsRepositoryAndBranchSettings(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	for _, key := range []string{"pull.rebase", "branch.main.rebase"} {
		t.Run(key, func(t *testing.T) {
			repository := testRepository(t)
			runGit(t, repository.Root, "config", "--local", key, "true")
			got, err := RebaseWarning(ctx, repository, "main")
			if err != nil {
				t.Fatalf("RebaseWarning() error = %v", err)
			}
			if !got {
				t.Fatalf("RebaseWarning() = false for %s", key)
			}
		})
	}
}

func TestInspectDetachedHead(t *testing.T) {
	t.Parallel()

	repository := testRepository(t)
	runGit(t, repository.Root, "checkout", "--detach", "-q")
	status, err := Inspect(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if status.Branch != "" || status.Enabled {
		t.Fatalf("Inspect() = %#v, want detached status", status)
	}
}

func testRepository(t *testing.T) publicgit.Repository {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "config", "user.name", "SPAS Test")
	runGit(t, root, "config", "user.email", "spas@example.invalid")
	runGit(t, root, "commit", "--allow-empty", "-q", "-m", "initial")
	repository, err := publicgit.Discover(context.Background(), gitexec.Runner{}, root)
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func config(t *testing.T, repository publicgit.Repository, key string) string {
	t.Helper()
	result, err := repository.Git.Run(context.Background(), repository.Root, "config", "--local", "--get", key)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(result.Stdout))
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := (gitexec.Runner{}).Run(context.Background(), dir, args...); err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}

func TestReapplyRestoresOnlyUnchangedPreSPASValue(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository := testRepository(t)
	state := linkstate.State{Merge: linkstate.Merge{ManagedBranches: map[string]linkstate.ManagedBranch{}}}
	if _, err := Enable(ctx, repository, &state); err != nil {
		t.Fatal(err)
	}
	if err := Restore(ctx, repository, state); err != nil {
		t.Fatal(err)
	}
	if err := Reapply(ctx, repository, state); err != nil {
		t.Fatal(err)
	}
	if got := config(t, repository, "branch.main.mergeOptions"); got != "--no-overwrite-ignore" {
		t.Fatalf("Reapply() value = %q", got)
	}

	if err := Restore(ctx, repository, state); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository.Root, "config", "--local", "branch.main.mergeOptions", "--no-ff")
	if err := Reapply(ctx, repository, state); err != nil {
		t.Fatal(err)
	}
	if got := config(t, repository, "branch.main.mergeOptions"); got != "--no-ff" {
		t.Fatalf("Reapply() overwrote user value: %q", got)
	}
}

func TestRestorePreservesExplicitEmptyValue(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository := testRepository(t)
	runGit(t, repository.Root, "config", "--local", "branch.main.mergeOptions", "")
	state := linkstate.State{Merge: linkstate.Merge{ManagedBranches: map[string]linkstate.ManagedBranch{}}}

	status, err := Enable(ctx, repository, &state)
	if err != nil {
		t.Fatal(err)
	}
	managed := state.Merge.ManagedBranches["main"]
	if !managed.BeforePresent || managed.Before != "" {
		t.Fatalf("managed state = %#v, want an explicitly present empty value", managed)
	}
	if !status.Enabled {
		t.Fatalf("Enable() = %#v", status)
	}
	if err := Restore(ctx, repository, state); err != nil {
		t.Fatal(err)
	}
	values, present, err := read(repository.Git, ctx, repository.Root, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !present || len(values) != 1 || values[0] != "" {
		t.Fatalf("restored values = %#v, present=%t", values, present)
	}
	if err := Reapply(ctx, repository, state); err != nil {
		t.Fatal(err)
	}
	if got := config(t, repository, "branch.main.mergeOptions"); got != "--no-overwrite-ignore" {
		t.Fatalf("Reapply() value = %q", got)
	}
}
