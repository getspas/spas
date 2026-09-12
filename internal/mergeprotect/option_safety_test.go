package mergeprotect

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/getspas/spas/internal/linkstate"
)

func TestInspectRejectsUnverifiableMergeOptions(t *testing.T) {
	t.Parallel()
	for _, values := range [][]string{
		{"--no-overwrite-ignore --overwrite-ignore"},
		{"--overwrite-ignore --no-overwrite-ignore"},
		{"-m --no-overwrite-ignore"},
		{"--message=--no-overwrite-ignore"},
		{"'--no-overwrite-ignore'"},
		{"--no-overwrite-ignore --unknown-option"},
		{"--no-edit\u00a0--no-overwrite-ignore"},
		{"--no-overwrite-ignore", "--overwrite-ignore"},
		{"", "--no-overwrite-ignore"},
	} {
		t.Run(values[0], func(t *testing.T) {
			t.Parallel()
			repository := testRepository(t)
			for _, value := range values {
				runGit(t, repository.Root, "config", "--local", "--add", "branch.main.mergeOptions", value)
			}
			configFile := filepath.Join(repository.CommonDir, "config")
			before, err := os.ReadFile(configFile)
			if err != nil {
				t.Fatal(err)
			}
			status, err := Inspect(context.Background(), repository)
			if err != nil {
				t.Fatal(err)
			}
			if status.Enabled || !status.Ambiguous {
				t.Errorf("Inspect() = %+v, want unverified protection", status)
			}
			state := linkstate.State{Merge: linkstate.Merge{ManagedBranches: map[string]linkstate.ManagedBranch{}}}
			if _, err := Enable(context.Background(), repository, &state); err == nil {
				t.Error("Enable() accepted unverifiable user options")
			}
			after, err := os.ReadFile(configFile)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || len(state.Merge.ManagedBranches) != 0 {
				t.Error("Enable() changed ambiguous configuration or ownership state")
			}
		})
	}
}

func TestMergeProtectionPreventsIgnoredFileOverwrite(t *testing.T) {
	t.Parallel()
	for _, options := range []string{"--no-overwrite-ignore", "--no-edit --log --no-overwrite-ignore"} {
		t.Run(options, func(t *testing.T) {
			t.Parallel()
			repository := testRepository(t)
			runGit(t, repository.Root, "checkout", "-q", "-b", "incoming")
			asset := filepath.Join(repository.Root, "secret.txt")
			if err := os.WriteFile(asset, []byte("incoming\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runGit(t, repository.Root, "add", "secret.txt")
			runGit(t, repository.Root, "commit", "-q", "-m", "incoming asset")
			runGit(t, repository.Root, "checkout", "-q", "main")
			if err := os.WriteFile(filepath.Join(repository.CommonDir, "info", "exclude"), []byte("/secret.txt\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(asset, []byte("private local bytes\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runGit(t, repository.Root, "config", "branch.main.mergeOptions", options)
			status, err := Inspect(context.Background(), repository)
			if err != nil || !status.Enabled || status.Ambiguous {
				t.Fatalf("Inspect() = %+v, %v", status, err)
			}
			if _, err := repository.Git.Run(context.Background(), repository.Root, "merge", "incoming"); err == nil {
				t.Fatal("protected merge overwrote an ignored asset")
			}
			content, err := os.ReadFile(asset)
			if err != nil || string(content) != "private local bytes\n" {
				t.Fatalf("asset = %q, %v", content, err)
			}
		})
	}
}

func TestInspectRejectsWorktreeMergeOptionOverrides(t *testing.T) {
	t.Parallel()
	repository := testRepository(t)
	runGit(t, repository.Root, "config", "extensions.worktreeConfig", "true")
	runGit(t, repository.Root, "config", "--local", "branch.main.mergeOptions", "--no-overwrite-ignore")
	runGit(t, repository.Root, "config", "--worktree", "branch.main.mergeOptions", "--overwrite-ignore")
	status, err := Inspect(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled || !status.Ambiguous {
		t.Fatalf("Inspect() = %+v, want the effective override to prevent a safety claim", status)
	}
}

func TestInspectRequiresRepositoryLocalOptions(t *testing.T) {
	for _, scope := range []string{"global", "worktree"} {
		t.Run(scope, func(t *testing.T) {
			repository := testRepository(t)
			if scope == "global" {
				global := filepath.Join(t.TempDir(), "gitconfig")
				runGit(t, repository.Root, "config", "--file", global, "branch.main.mergeOptions", requiredOption)
				t.Setenv("GIT_CONFIG_GLOBAL", global)
			} else {
				runGit(t, repository.Root, "config", "extensions.worktreeConfig", "true")
				runGit(t, repository.Root, "config", "--worktree", "branch.main.mergeOptions", requiredOption)
			}
			status, err := Inspect(context.Background(), repository)
			if err != nil || status.Enabled || !status.Ambiguous || !status.Present {
				t.Fatalf("Inspect() = %+v, %v, want unverified %s options", status, err, scope)
			}
		})
	}
}

func TestRestorePreservesMergeOptionWhitespace(t *testing.T) {
	t.Parallel()
	repository := testRepository(t)
	before := " \t--no-edit\n--log  "
	runGit(t, repository.Root, "config", "--local", "branch.main.mergeOptions", before)
	state := linkstate.State{Merge: linkstate.Merge{ManagedBranches: map[string]linkstate.ManagedBranch{}}}
	if _, err := Enable(context.Background(), repository, &state); err != nil {
		t.Fatal(err)
	}
	if err := Restore(context.Background(), repository, state); err != nil {
		t.Fatal(err)
	}
	values, present, err := read(repository.Git, context.Background(), repository.Root, "main")
	if err != nil || !present || len(values) != 1 || values[0] != before {
		t.Fatalf("restored options = %q, %t, %v, want %q", values, present, err, before)
	}
}

func TestProtectionUsesExactBranchName(t *testing.T) {
	t.Parallel()
	repository := testRepository(t)
	runGit(t, repository.Root, "config", "branch.main.mergeOptions", requiredOption)
	branch := "main\u00a0"
	runGit(t, repository.Root, "checkout", "-q", "-b", branch)
	status, err := Inspect(context.Background(), repository)
	if err != nil || status.Enabled || status.Branch != branch {
		t.Fatalf("Inspect() = %+v, %v, want the unprotected current branch %q", status, err, branch)
	}
	state := linkstate.State{Merge: linkstate.Merge{ManagedBranches: map[string]linkstate.ManagedBranch{}}}
	if _, err := Enable(context.Background(), repository, &state); err != nil {
		t.Fatal(err)
	}
	if _, exists := state.Merge.ManagedBranches[branch]; !exists {
		t.Fatalf("protection ownership = %+v, want current branch", state.Merge.ManagedBranches)
	}
	if got := config(t, repository, "branch."+branch+".mergeOptions"); got != requiredOption {
		t.Fatalf("current branch mergeOptions = %q", got)
	}
	if got := config(t, repository, "branch.main.mergeOptions"); got != requiredOption {
		t.Fatalf("neighboring branch mergeOptions changed to %q", got)
	}
}

func TestEnablePreservesIncludedMergeOptions(t *testing.T) {
	t.Parallel()
	repository := testRepository(t)
	included := filepath.Join(repository.CommonDir, "included-options")
	runGit(t, repository.Root, "config", "--file", included, "branch.main.mergeOptions", "--no-edit")
	runGit(t, repository.Root, "config", "branch.main.description", "existing section")
	runGit(t, repository.Root, "config", "include.path", included)
	configFile := filepath.Join(repository.CommonDir, "config")
	before, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	beforeInclude, err := os.ReadFile(included)
	if err != nil {
		t.Fatal(err)
	}
	state := linkstate.State{Merge: linkstate.Merge{ManagedBranches: map[string]linkstate.ManagedBranch{}}}
	if _, err := Enable(context.Background(), repository, &state); err == nil {
		t.Error("Enable() accepted a value owned by an included file")
	}
	after, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	afterInclude, err := os.ReadFile(included)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !bytes.Equal(beforeInclude, afterInclude) || len(state.Merge.ManagedBranches) != 0 {
		t.Fatal("Enable() changed included configuration or recorded incorrect ownership")
	}
}
