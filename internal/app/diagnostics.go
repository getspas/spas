package app

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/getspas/spas/internal/collision"
	"github.com/getspas/spas/internal/exclude"
	"github.com/getspas/spas/internal/filesync"
	"github.com/getspas/spas/internal/gitexec"
	"github.com/getspas/spas/internal/linkstate"
	"github.com/getspas/spas/internal/mergeprotect"
	"github.com/getspas/spas/internal/pathmodel"
	"github.com/getspas/spas/internal/publicgit"
)

type DiffOptions struct {
	Paths    []string
	NameOnly bool
	Stat     bool
	Staged   bool
}

func (a App) Diff(ctx context.Context, options DiffOptions) error {
	repository, state, err := a.linked(ctx)
	if err != nil {
		return err
	}
	if state.Materializing != nil {
		return fmt.Errorf("a previous sync requires recovery; run spas sync before comparing workspace changes")
	}
	if options.Staged {
		return a.diffStaged(ctx, repository, state, options)
	}
	managed := append(append([]string{}, state.ManagedPaths...), state.PendingAdds...)
	pendingRemovals := stringSet(state.PendingRemovalPaths())
	if len(options.Paths) > 0 {
		filter := make(map[string]struct{})
		for _, value := range options.Paths {
			path, _, err := pathmodel.Resolve(repository.Root, a.PathBase, value)
			if err != nil {
				return err
			}
			filter[path.String()] = struct{}{}
		}
		var selected []string
		for _, value := range managed {
			if _, found := filter[value]; found {
				selected = append(selected, value)
			}
		}
		managed = selected
	}
	sort.Strings(managed)

	var changed []string
	privateRoot := state.Private.LocalRepositoryPath
	for _, value := range managed {
		path, err := pathmodel.Parse(value)
		if err != nil {
			return err
		}
		publicFile := path.OSPath(repository.Root)
		privateFile := path.OSPath(privateRoot)
		if _, removing := pendingRemovals[value]; removing {
			changed = append(changed, value)
			if options.NameOnly || a.JSON {
				continue
			}
			args := []string{"--no-pager", "diff", "--no-ext-diff", "--no-textconv", "--no-index"}
			if options.Stat {
				args = append(args, "--stat")
			}
			args = append(args, "--", privateFile, os.DevNull)
			diffGit := a.Git
			diffGit.Stdout = a.Out
			_, diffErr := diffGit.RunStreaming(ctx, repository.Root, args...)
			if diffErr != nil {
				if code, ok := gitexec.ExitCode(diffErr); !ok || code != 1 {
					return diffErr
				}
			}
			continue
		}
		if _, err := os.Stat(publicFile); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if !state.Private.Initialized {
			changed = append(changed, value)
			continue
		}
		equal, err := filesync.Equal(publicFile, privateFile)
		if os.IsNotExist(err) {
			equal = false
			err = nil
		}
		if err != nil {
			return err
		}
		if equal {
			continue
		}
		changed = append(changed, value)
		if options.NameOnly || a.JSON {
			continue
		}
		args := []string{"--no-pager", "diff", "--no-ext-diff", "--no-textconv", "--no-index"}
		if options.Stat {
			args = append(args, "--stat")
		}
		args = append(args, "--", privateFile, publicFile)
		diffGit := a.Git
		diffGit.Stdout = a.Out
		_, diffErr := diffGit.RunStreaming(ctx, repository.Root, args...)
		if diffErr != nil {
			if code, ok := gitexec.ExitCode(diffErr); !ok || code != 1 {
				return diffErr
			}
		}
	}
	if a.JSON {
		return a.write(map[string]any{"changedPaths": changed})
	}
	if options.NameOnly || !state.Private.Initialized {
		for _, value := range changed {
			if _, err := fmt.Fprintln(a.Out, value); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a App) diffStaged(ctx context.Context, repository publicgit.Repository, state linkstate.State, options DiffOptions) error {
	if !state.Private.Initialized {
		return fmt.Errorf("private repository is not initialized; nothing is staged")
	}
	private := a.privateRepository(state)
	var filters []pathmodel.Path
	for _, value := range options.Paths {
		path, _, err := pathmodel.Resolve(repository.Root, a.PathBase, value)
		if err != nil {
			return err
		}
		filters = append(filters, path)
	}
	changes, err := private.ChangedPaths(ctx)
	if err != nil {
		return err
	}
	filterSet := make(map[string]struct{}, len(filters))
	for _, path := range filters {
		filterSet[path.String()] = struct{}{}
	}
	var changed []string
	for _, change := range changes {
		if len(filterSet) > 0 {
			if _, found := filterSet[change.Path.String()]; !found {
				continue
			}
		}
		changed = append(changed, change.Path.String())
	}
	sort.Strings(changed)
	if a.JSON {
		return a.write(map[string]any{"stagedPaths": changed})
	}
	if options.NameOnly {
		for _, value := range changed {
			if _, err := fmt.Fprintln(a.Out, value); err != nil {
				return err
			}
		}
		return nil
	}
	return private.StreamStagedDiff(ctx, options.Stat, filters, a.Out)
}

type DoctorResult struct {
	Healthy  bool          `json:"healthy"`
	Checks   []DoctorCheck `json:"checks"`
	Warnings int           `json:"warnings"`
	Errors   int           `json:"errors"`
}

type DoctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

func (a App) Doctor(ctx context.Context) error {
	repository, state, err := a.linked(ctx)
	if err != nil {
		return err
	}
	result := DoctorResult{Healthy: true}
	add := func(name, status, message string) {
		result.Checks = append(result.Checks, DoctorCheck{Name: name, Status: status, Message: message})
		switch status {
		case "warning":
			result.Warnings++
		case "error":
			result.Errors++
			result.Healthy = false
		}
	}
	if state.Materializing != nil {
		add("pending-recovery", "error", "a previous sync has a private result waiting to be pushed or materialized; run spas sync")
	} else {
		add("pending-recovery", "ok", "no interrupted push or materialization")
	}

	version, err := a.Git.Run(ctx, repository.Root, "--version")
	if err != nil {
		add("git", "error", err.Error())
	} else {
		add("git", "ok", strings.TrimSpace(string(version.Stdout)))
	}

	worktrees, err := repository.WorktreeCount(ctx)
	if err != nil {
		add("worktrees", "error", err.Error())
	} else if worktrees > 1 {
		add("worktrees", "error", "multiple public worktrees share the repository-local exclude file; mutating commands are disabled in the current implementation")
	} else {
		add("worktrees", "ok", "single public worktree")
	}

	configCase, present, err := repository.EffectiveIgnoreCase(ctx)
	filesystemCase := false
	casePolicyKnown := err == nil
	if err != nil {
		add("case-policy", "error", err.Error())
	} else if probedCase, probeErr := repository.FilesystemIgnoresCase(); probeErr != nil {
		casePolicyKnown = false
		add("case-policy", "error", probeErr.Error())
	} else {
		filesystemCase = probedCase
		if present && configCase != filesystemCase {
			add("case-policy", "warning", fmt.Sprintf("core.ignoreCase=%t disagrees with filesystem=%t; sync uses case-insensitive checks", configCase, filesystemCase))
		} else {
			add("case-policy", "ok", fmt.Sprintf("case-insensitive=%t", configCase || filesystemCase))
		}
	}

	mergeStatus, err := mergeprotect.Inspect(ctx, repository)
	if err != nil {
		add("merge-protection", "error", err.Error())
	} else if !mergeStatus.Enabled {
		add("merge-protection", "warning", "current public branch does not enable --no-overwrite-ignore")
	} else {
		add("merge-protection", "ok", "current public branch enables --no-overwrite-ignore")
	}
	if usesRebase, err := mergeprotect.RebaseWarning(ctx, repository, mergeStatus.Branch); err != nil {
		add("pull-mode", "error", err.Error())
	} else if usesRebase {
		add("pull-mode", "warning", "pull is configured to rebase; merge overwrite protection does not apply")
	} else {
		add("pull-mode", "ok", "no configured rebase pull detected")
	}

	publicPaths, publicErr := repository.TrackedPaths(ctx)
	privatePaths := unionPaths(stringsToPaths(state.ManagedPaths), stringsToPaths(state.PendingAdds))
	interruptedMerge := false
	mergeInspectionFailed := false
	if state.Private.Initialized {
		private := a.privateRepository(state)
		mergeInProgress, mergeProbeErr := private.MergeInProgress()
		switch {
		case mergeProbeErr != nil:
			interruptedMerge = true
			mergeInspectionFailed = true
			add("interrupted-private-merge", "error", fmt.Sprintf("could not inspect private merge state: %v", mergeProbeErr))
		case mergeInProgress && state.ActiveMerge != nil:
			interruptedMerge = true
			if !state.ActiveMerge.ConflictFilesReady {
				add("interrupted-private-merge", "error",
					"private conflict materialization was interrupted before completion; run `spas sync --abort` — continuation is unsafe")
			} else {
				add("interrupted-private-merge", "error",
					"a private merge is interrupted; resolve the reported files and run `spas sync --continue --message <reason>` or `spas sync --abort` — do not clean the private clone by hand")
			}
		case mergeInProgress && state.ActiveMerge == nil:
			interruptedMerge = true
			add("interrupted-private-merge", "error", "the private clone is in a merge but SPAS recovery state is missing; do not commit or clean the private clone by hand")
		case !mergeInProgress && state.ActiveMerge != nil:
			interruptedMerge = true
			if state.ActiveMerge.PreMergeHead != "" {
				add("interrupted-private-merge", "error", "private merge abort recovery is incomplete; run `spas sync --abort` to restore the public workspace and clear recovery state")
			} else {
				add("interrupted-private-merge", "error", "SPAS merge recovery state exists but lacks abort-recovery metadata; preserve workspace resolutions and restore or relink the private clone manually")
			}
		default:
			add("interrupted-private-merge", "ok", "no interrupted private merge")
		}
		// The origin shape is checked locally without resolving the link's
		// remote URL: doctor stays offline. The full origin/URL-rewrite
		// verification runs during sync.
		if message, healthy, originErr := a.originConfigShape(ctx, private.Path); originErr != nil {
			add("remote-config", "error", originErr.Error())
		} else if !healthy {
			add("remote-config", "error", message)
		} else {
			add("remote-config", "ok", message)
		}
		if tracked, trackedErr := private.TrackedPaths(ctx); trackedErr != nil {
			add("private-clone", "error", trackedErr.Error())
		} else {
			privatePaths = unionPaths(tracked, stringsToPaths(state.PendingAdds))
			if mergeInspectionFailed {
				add("private-clone", "error", "private merge state could not be inspected")
			} else if interruptedMerge {
				add("private-clone", "error", "private merge is in progress")
			} else {
				clean, cleanErr := private.IsClean(ctx)
				if cleanErr != nil {
					add("private-clone", "error", cleanErr.Error())
				} else if !clean {
					add("private-clone", "error", "SPAS-managed private clone is not clean")
				} else {
					add("private-clone", "ok", "clean")
				}
			}
		}
		if head, headErr := private.Head(ctx); headErr != nil {
			add("expected-private-head", "error", fmt.Sprintf("could not read actual private HEAD: %v", headErr))
		} else {
			if privateHeadMatches(state, head) {
				add("expected-private-head", "ok", fmt.Sprintf("expected=%q actual=%q", state.Private.ExpectedHead, head))
			} else {
				add("expected-private-head", "error", fmt.Sprintf("expected=%q actual=%q; SPAS will not push or reset the clone automatically", state.Private.ExpectedHead, head))
			}
			if head != "" {
				if treeErr := private.ValidateTree(ctx, "HEAD"); treeErr != nil {
					add("unsupported-private-file-types", "error", treeErr.Error())
				} else {
					add("unsupported-private-file-types", "ok", "every private path is a supported regular file")
				}
			}
		}
	} else if state.Private.Initialization != nil {
		add("private-clone-initialization", "error", fmt.Sprintf("clone initialization is journaled in phase %q; run spas sync to resume", state.Private.Initialization.Phase))
	} else {
		add("private-clone", "warning", "not initialized; first sync will contact GitHub")
	}
	if pending := len(state.PendingAdds) + len(state.PendingRemoves); pending > 0 {
		add("pending-ownership-transfers", "warning", fmt.Sprintf(
			"%d pending addition(s) and %d pending removal(s) await `spas sync`",
			len(state.PendingAdds), len(state.PendingRemoves)))
	} else {
		add("pending-ownership-transfers", "ok", "none pending")
	}
	if publicErr != nil {
		add("path-ownership", "error", publicErr.Error())
	} else if !casePolicyKnown {
		add("path-ownership", "error", "case policy is unavailable")
	} else {
		ignoreCase := configCase || filesystemCase
		conflicts := collision.Detect(publicPaths, privatePaths, ignoreCase)
		if len(conflicts) > 0 {
			add("path-ownership", "error", fmt.Sprintf("%d public/private path conflict(s)", len(conflicts)))
		} else {
			add("path-ownership", "ok", "no public/private path conflict")
		}
	}

	var exclusionFailures []string
	excluded, checkErr := repository.ExcludedPaths(ctx, privatePaths)
	if checkErr != nil {
		exclusionFailures = append(exclusionFailures, fmt.Sprintf("exclusion check: %v", checkErr))
	} else {
		for _, path := range privatePaths {
			if !excluded[path] {
				exclusionFailures = append(exclusionFailures, path.String())
			}
		}
	}
	if len(exclusionFailures) > 0 {
		add("local-exclusions", "error", "not effectively excluded: "+strings.Join(exclusionFailures, ", "))
	} else {
		add("local-exclusions", "ok", fmt.Sprintf("%d private path(s) effectively excluded", len(privatePaths)))
	}

	if excludePath, excludeErr := repository.InfoExcludePath(ctx); excludeErr != nil {
		add("exclude-block-integrity", "error", excludeErr.Error())
	} else if plan, planErr := exclude.Build(excludePath, state.Exclude.BlockID, managedForExclusion(state, stringsToPaths(state.PendingAdds), nil)); planErr != nil {
		add("exclude-block-integrity", "error", planErr.Error())
	} else if plan.Changed {
		add("exclude-block-integrity", "warning", "the SPAS local-exclude block does not match the managed file set; the next mutating command rewrites it")
	} else {
		add("exclude-block-integrity", "ok", "the SPAS local-exclude block matches the managed file set")
	}

	if a.JSON {
		if err := a.write(result); err != nil {
			return err
		}
		if result.Errors > 0 {
			return OutputWrittenError{Err: fmt.Errorf("doctor found %d error(s)", result.Errors)}
		}
		return nil
	}
	for _, check := range result.Checks {
		if _, err := fmt.Fprintf(a.Out, "%-18s %-7s %s\n", check.Name, check.Status, check.Message); err != nil {
			return err
		}
	}
	if result.Errors > 0 {
		return fmt.Errorf("doctor found %d error(s)", result.Errors)
	}
	return nil
}

func (a App) originConfigShape(ctx context.Context, privatePath string) (string, bool, error) {
	result, err := a.Git.Run(ctx, privatePath, "config", "--local", "--get-all", "remote.origin.url")
	if err != nil {
		return "", false, fmt.Errorf("read private clone origin: %w", err)
	}
	var urls []string
	for _, line := range strings.Split(strings.TrimSpace(string(result.Stdout)), "\n") {
		if line != "" {
			urls = append(urls, line)
		}
	}
	if len(urls) != 1 {
		return fmt.Sprintf("private clone has %d origin URLs; expected exactly one", len(urls)), false, nil
	}
	if pushResult, pushErr := a.Git.Run(ctx, privatePath, "config", "--local", "--get-all", "remote.origin.pushurl"); pushErr == nil {
		if strings.TrimSpace(string(pushResult.Stdout)) != "" {
			return "private clone has an unsupported origin push URL", false, nil
		}
	} else if code, ok := gitexec.ExitCode(pushErr); !ok || code != 1 {
		return "", false, fmt.Errorf("inspect private clone push URL: %w", pushErr)
	}
	return "single origin URL, no push URL override", true, nil
}
