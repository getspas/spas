package mergeprotect

import (
	"context"
	"fmt"
	"strings"

	"github.com/getspas/spas/internal/gitexec"
	"github.com/getspas/spas/internal/linkstate"
	"github.com/getspas/spas/internal/publicgit"
	"github.com/getspas/spas/internal/spaserr"
)

const requiredOption = "--no-overwrite-ignore"

type Status struct {
	Branch  string `json:"branch"`
	Enabled bool   `json:"enabled"`
	Value   string `json:"value,omitempty"`
	Present bool   `json:"present,omitempty"`
	// Ambiguous reports multiple or inherited values, or options whose effect
	// SPAS cannot verify. These require manual configuration.
	Ambiguous bool `json:"ambiguous,omitempty"`
}

func Inspect(ctx context.Context, repository publicgit.Repository) (Status, error) {
	branch, err := repository.Branch(ctx)
	if err != nil {
		return Status{}, err
	}
	if branch == "" {
		return Status{}, nil
	}
	options, err := readEffectiveOptions(repository.Git, ctx, repository.Root, branch)
	if err != nil {
		return Status{}, err
	}
	values := make([]string, len(options))
	for i, option := range options {
		values[i] = option.value
	}
	status := Status{Branch: branch, Value: strings.Join(values, "\n"), Present: len(options) > 0}
	if len(options) == 0 {
		return status, nil
	}
	if len(options) != 1 || options[0].scope != "local" {
		status.Ambiguous = true
		return status, nil
	}
	direct, present, err := read(repository.Git, ctx, repository.Root, branch)
	if err != nil {
		return Status{}, err
	}
	if !present || len(direct) != 1 || direct[0] != values[0] {
		status.Ambiguous = true
		return status, nil
	}
	// Verify exact operand-free flags using Git's ASCII whitespace separators.
	for _, option := range strings.FieldsFunc(values[0], func(r rune) bool {
		return strings.ContainsRune(" \t\r\n\v\f", r)
	}) {
		switch option {
		case requiredOption:
			status.Enabled = true
		case "--no-edit", "--log", "--no-ff":
		default:
			status.Enabled = false
			status.Ambiguous = true
			return status, nil
		}
	}
	return status, nil
}

func Enable(ctx context.Context, repository publicgit.Repository, state *linkstate.State) (Status, error) {
	status, err := Inspect(ctx, repository)
	if err != nil {
		return Status{}, err
	}
	if status.Enabled {
		return status, nil
	}
	if status.Branch == "" {
		return status, spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("cannot configure merge protection in detached HEAD state"))
	}
	if status.Ambiguous {
		return status, PolicyError(status)
	}

	before := status.Value
	after := strings.TrimSpace(strings.Join([]string{before, requiredOption}, " "))
	key := "branch." + status.Branch + ".mergeOptions"
	if _, err := repository.Git.Run(ctx, repository.Root, "config", "--local", "--replace-all", key, after); err != nil {
		return Status{}, fmt.Errorf("configure merge overwrite protection: %w", err)
	}
	state.Merge.ManagedBranches[status.Branch] = linkstate.ManagedBranch{
		Before:        before,
		BeforePresent: status.Present,
		After:         after,
	}
	return Status{Branch: status.Branch, Enabled: true, Value: after}, nil
}

func Restore(ctx context.Context, repository publicgit.Repository, state linkstate.State) error {
	for branch, managed := range state.Merge.ManagedBranches {
		if err := RestoreBranch(ctx, repository, branch, managed); err != nil {
			return err
		}
	}
	return nil
}

// Reapply restores SPAS's previously written merge options during rollback of
// an unlink operation. It writes only when the current value still exactly
// matches the pre-SPAS value, so a concurrent user edit is never overwritten.
func Reapply(ctx context.Context, repository publicgit.Repository, state linkstate.State) error {
	for branch, managed := range state.Merge.ManagedBranches {
		values, present, err := read(repository.Git, ctx, repository.Root, branch)
		if err != nil {
			return err
		}
		if !managed.BeforePresent {
			if present {
				continue
			}
		} else if len(values) != 1 || values[0] != managed.Before {
			continue
		}
		key := "branch." + branch + ".mergeOptions"
		if _, err := repository.Git.Run(ctx, repository.Root, "config", "--local", "--replace-all", key, managed.After); err != nil {
			return fmt.Errorf("reapply merge options for branch %q: %w", branch, err)
		}
	}
	return nil
}

// RestoreBranch restores one value written by Enable, but only when the
// current value still exactly matches that write.
func RestoreBranch(
	ctx context.Context,
	repository publicgit.Repository,
	branch string,
	managed linkstate.ManagedBranch,
) error {
	values, _, err := read(repository.Git, ctx, repository.Root, branch)
	if err != nil {
		return err
	}
	if len(values) != 1 || values[0] != managed.After {
		return nil
	}
	key := "branch." + branch + ".mergeOptions"
	if !managed.BeforePresent {
		if _, err := repository.Git.Run(ctx, repository.Root, "config", "--local", "--unset-all", key); err != nil {
			if code, ok := gitexec.ExitCode(err); !ok || (code != 5 && code != 1) {
				return fmt.Errorf("restore merge options for branch %q: %w", branch, err)
			}
		}
		return nil
	}
	if _, err := repository.Git.Run(ctx, repository.Root, "config", "--local", "--replace-all", key, managed.Before); err != nil {
		return fmt.Errorf("restore merge options for branch %q: %w", branch, err)
	}
	return nil
}

// RebaseWarning reports whether a plain `git pull` on the branch may rebase
// instead of merge, in which case merge overwrite protection does not apply.
// branch.<name>.rebase takes precedence over pull.rebase, matching Git.
func RebaseWarning(ctx context.Context, repository publicgit.Repository, branch string) (bool, error) {
	if branch != "" {
		value, present, err := readRaw(repository.Git, ctx, repository.Root, "branch."+branch+".rebase")
		if err != nil {
			return false, err
		}
		if present {
			return rebaseValueMayRebase(value), nil
		}
	}
	value, present, err := readRaw(repository.Git, ctx, repository.Root, "pull.rebase")
	if err != nil {
		return false, err
	}
	if present {
		return rebaseValueMayRebase(value), nil
	}
	return false, nil
}

// rebaseValueMayRebase interprets every documented pull.rebase value; unknown
// values are conservatively treated as "may rebase" so the warning is never
// silently suppressed.
func rebaseValueMayRebase(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "false", "no", "off", "0", "":
		return false
	default:
		// true, yes, on, 1, merges, preserve, interactive, or anything new.
		return true
	}
}

func readRaw(git gitexec.Runner, ctx context.Context, root, key string) (string, bool, error) {
	result, err := git.Run(ctx, root, "config", "--get", key)
	if err != nil {
		if code, ok := gitexec.ExitCode(err); ok && code == 1 {
			return "", false, nil
		}
		return "", false, err
	}
	return strings.TrimSpace(string(result.Stdout)), true, nil
}

// PolicyError converts a `require` policy failure into its typed error.
func PolicyError(status Status) error {
	if status.Ambiguous {
		return spaserr.Wrap(
			spaserr.KindUnsafeGitState,
			fmt.Errorf("public branch %q has unverifiable mergeOptions; configure one direct repository-local value using supported flags and %s", status.Branch, requiredOption),
		)
	}
	return spaserr.Wrap(
		spaserr.KindUnsafeGitState,
		fmt.Errorf("public branch %q does not have %s merge protection; enable it locally before retrying", status.Branch, requiredOption),
	)
}

func read(git gitexec.Runner, ctx context.Context, root, branch string) ([]string, bool, error) {
	key := "branch." + branch + ".mergeOptions"
	result, err := git.Run(ctx, root, "config", "--local", "--no-includes", "--null", "--get-all", key)
	if err != nil {
		if code, ok := gitexec.ExitCode(err); ok && code == 1 {
			return nil, false, nil
		}
		return nil, false, err
	}
	raw, terminated := strings.CutSuffix(string(result.Stdout), "\x00")
	if !terminated {
		return nil, false, fmt.Errorf("Git returned malformed mergeOptions values")
	}
	return strings.Split(raw, "\x00"), true, nil
}

type scopedOption struct {
	scope string
	value string
}

func readEffectiveOptions(git gitexec.Runner, ctx context.Context, root, branch string) ([]scopedOption, error) {
	result, err := git.Run(ctx, root, "config", "--null", "--show-scope", "--get-all", "branch."+branch+".mergeOptions")
	if err != nil {
		if code, ok := gitexec.ExitCode(err); ok && code == 1 {
			return nil, nil
		}
		return nil, err
	}
	raw, terminated := strings.CutSuffix(string(result.Stdout), "\x00")
	fields := strings.Split(raw, "\x00")
	if !terminated || len(fields)%2 != 0 {
		return nil, fmt.Errorf("Git returned malformed scoped mergeOptions values")
	}
	options := make([]scopedOption, 0, len(fields)/2)
	for i := 0; i < len(fields); i += 2 {
		options = append(options, scopedOption{scope: fields[i], value: fields[i+1]})
	}
	return options, nil
}
