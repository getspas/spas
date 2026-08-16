package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/getspas/spas/internal/collision"
	"github.com/getspas/spas/internal/exclude"
	"github.com/getspas/spas/internal/filesync"
	"github.com/getspas/spas/internal/gitexec"
	"github.com/getspas/spas/internal/interaction"
	"github.com/getspas/spas/internal/linkstate"
	"github.com/getspas/spas/internal/lock"
	"github.com/getspas/spas/internal/mergeprotect"
	"github.com/getspas/spas/internal/pathmodel"
	"github.com/getspas/spas/internal/privategit"
	"github.com/getspas/spas/internal/provider"
	"github.com/getspas/spas/internal/publicgit"
	"github.com/getspas/spas/internal/spaserr"
)

var removePrivateClone = os.RemoveAll

type App struct {
	Git      gitexec.Runner
	Store    linkstate.Store
	Prompt   interaction.Prompter
	Out      io.Writer
	Err      io.Writer
	RepoHint string
	PathBase string
	JSON     bool
	Provider provider.RepositoryProvider
}

type ExistingExcludePolicy string

const (
	ExcludeAsk      ExistingExcludePolicy = "ask"
	ExcludePreserve ExistingExcludePolicy = "preserve"
	ExcludeAbort    ExistingExcludePolicy = "abort"
)

type MergeProtectionPolicy string

const (
	MergeAsk     MergeProtectionPolicy = "ask"
	MergeEnable  MergeProtectionPolicy = "enable"
	MergeSkip    MergeProtectionPolicy = "skip"
	MergeRequire MergeProtectionPolicy = "require"
)

type LinkOptions struct {
	Repository  string
	Transport   provider.Transport
	Branch      string
	Replace     bool
	DryRun      bool
	AllowPublic bool
}

func (a App) Link(ctx context.Context, options LinkOptions) error {
	repository, err := a.publicRepository(ctx)
	if err != nil {
		return err
	}
	if err := a.requirePrimarySingleWorktree(ctx, repository); err != nil {
		return err
	}
	if a.Provider == nil {
		return fmt.Errorf("repository provider is not configured")
	}
	ref, err := a.Provider.Resolve(provider.RepositoryRequest{Raw: options.Repository, Transport: options.Transport})
	if err != nil {
		return err
	}
	if ref.Provider != a.Provider.ID() {
		return fmt.Errorf("repository provider returned identity %q, expected %q", ref.Provider, a.Provider.ID())
	}
	if ref.Provider == "" || ref.Canonical == "" || ref.Transport == "" || ref.RemoteURL == "" {
		return fmt.Errorf("repository provider returned an incomplete repository identity")
	}
	if options.Branch != "" {
		if err := privategit.ValidateBranchName(ctx, a.Git, repository.Root, options.Branch); err != nil {
			return err
		}
	}
	if !options.AllowPublic && !options.DryRun {
		isPublic, probeErr := a.Provider.ProbePublic(ctx, a.Git, ref)
		if probeErr != nil {
			return probeErr
		}
		if isPublic {
			approved, err := a.Prompt.Confirm(
				ctx,
				fmt.Sprintf("Repository %q is publicly readable on GitHub. Managed private assets will be publicly accessible. Continue?", ref.Canonical),
				false,
				false,
			)
			if err != nil {
				return err
			}
			if !approved {
				return fmt.Errorf("linking publicly readable repository declined")
			}
		}
	}
	state := linkstate.New(repository.Root, repository.CommonDir, ref, options.Branch, a.Store)
	if !options.DryRun {
		linkLock, err := lock.Acquire(filepath.Join(a.Store.DataDir, "locks"), state.LinkID)
		if err != nil {
			return err
		}
		defer linkLock.Release()
	}
	existing, loadErr := a.loadState(repository.Root, repository.CommonDir)
	if loadErr == nil && !options.Replace {
		return fmt.Errorf("public workspace is already linked to %s", existing.Private.Repository)
	}
	if loadErr != nil && !errors.Is(loadErr, linkstate.ErrNotLinked) {
		return loadErr
	}
	if loadErr == nil && options.Replace &&
		(existing.Private.Initialized ||
			existing.Private.Initialization != nil ||
			len(existing.ManagedPaths) > 0 ||
			len(existing.PendingAdds) > 0 ||
			len(existing.PendingRemoves) > 0 ||
			len(existing.Merge.ManagedBranches) > 0 ||
			existing.ActiveMerge != nil ||
			existing.Materializing != nil) {
		return fmt.Errorf("existing link has private state or managed Git configuration; run spas unlink before linking a different repository")
	}
	if options.DryRun {
		return a.write(map[string]any{
			"action":            "link",
			"publicWorkspace":   state.Public.Root,
			"privateRepository": state.Private.Repository,
			"transport":         state.Private.Transport,
			"branch":            state.Private.Branch,
			"networkAccess":     false,
		})
	}
	if loadErr == nil && options.Replace && a.Prompt.Interactive {
		approved, err := a.Prompt.Confirm(
			ctx,
			fmt.Sprintf("Replace the existing local link to %s?", existing.Private.Repository),
			false,
			false,
		)
		if err != nil {
			return err
		}
		if !approved {
			return fmt.Errorf("link replacement declined")
		}
	}
	if err := a.Store.Save(state); err != nil {
		return err
	}
	return a.write(map[string]any{
		"linked":            true,
		"publicWorkspace":   state.Public.Root,
		"privateRepository": state.Private.Repository,
		"networkAccess":     false,
	})
}

type AddOptions struct {
	Paths           []string
	SkipTracked     bool
	ExistingExclude ExistingExcludePolicy
	MergeProtection MergeProtectionPolicy
	DryRun          bool
}

func (a App) Add(ctx context.Context, options AddOptions) error {
	repository, state, err := a.linked(ctx)
	if err != nil {
		return err
	}
	if err := a.requirePrimarySingleWorktree(ctx, repository); err != nil {
		return err
	}
	if !options.DryRun {
		linkLock, err := lock.Acquire(filepath.Join(a.Store.DataDir, "locks"), state.LinkID)
		if err != nil {
			return err
		}
		defer linkLock.Release()
		state, err = a.loadState(repository.Root, repository.CommonDir)
		if err != nil {
			return err
		}
	}
	if state.ActiveMerge != nil {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("a private merge is in progress; run spas sync --continue or spas sync --abort before adding paths"))
	}
	if state.Materializing != nil {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("a previous sync requires recovery; run spas sync before adding paths"))
	}
	if state.Private.Initialized {
		mergeInProgress, mergeProbeErr := a.privateRepository(state).MergeInProgress()
		if mergeProbeErr != nil {
			return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("inspect private merge state: %w", mergeProbeErr))
		}
		if mergeInProgress {
			return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("a private merge is in progress; run spas sync --continue or spas sync --abort before adding paths"))
		}
	}
	ignoreCase, err := a.casePolicy(ctx, repository)
	if err != nil {
		return err
	}
	files, err := a.expandPaths(repository.Root, options.Paths)
	if err != nil {
		return err
	}
	publicPaths, err := repository.TrackedPaths(ctx)
	if err != nil {
		return err
	}
	publicSet := canonicalSet(publicPaths, ignoreCase)

	additions := stringsToPaths(state.PendingAdds)
	addSet := canonicalSet(additions, ignoreCase)
	managedSet := canonicalSet(stringsToPaths(state.ManagedPaths), ignoreCase)
	pendingRemoves := append([]linkstate.PendingRemoval{}, state.PendingRemoves...)
	pendingRemoveSet := make(map[string]pathmodel.Path, len(pendingRemoves))
	for _, removal := range pendingRemoves {
		path, parseErr := pathmodel.Parse(removal.Path)
		if parseErr != nil {
			return parseErr
		}
		pendingRemoveSet[pathmodel.Canonical(path, ignoreCase)] = path
	}
	var newlyAdded []pathmodel.Path
	var cancelledRemovals []pathmodel.Path
	var skipped []pathmodel.Path
	for _, file := range files {
		if _, found := publicSet[pathmodel.Canonical(file, ignoreCase)]; found {
			decision, decisionErr := a.trackedPathDecision(ctx, file, options.SkipTracked)
			if decisionErr != nil {
				return decisionErr
			}
			if decision {
				skipped = append(skipped, file)
				continue
			}
			return spaserr.Wrap(
				spaserr.KindPathConflict,
				fmt.Errorf("public Git already tracks %q; remove that path from public tracking before adding it to SPAS", file),
			)
		}
		key := pathmodel.Canonical(file, ignoreCase)
		if managed, found := managedSet[key]; found {
			file, err = authoritativeManagedPath(repository.Root, file, managed)
			if err != nil {
				return err
			}
		}
		if pending, found := addSet[key]; found {
			file, err = authoritativeManagedPath(repository.Root, file, pending)
			if err != nil {
				return err
			}
		}
		if removalPath, found := pendingRemoveSet[key]; found {
			filtered := pendingRemoves[:0]
			for _, removal := range pendingRemoves {
				removalManagedPath, parseErr := pathmodel.Parse(removal.Path)
				if parseErr != nil {
					return parseErr
				}
				if pathmodel.Canonical(removalManagedPath, ignoreCase) == key {
					cancelledRemovals = append(cancelledRemovals, removalPath)
					continue
				}
				filtered = append(filtered, removal)
			}
			pendingRemoves = filtered
			delete(pendingRemoveSet, key)
		}
		if _, found := addSet[key]; !found {
			if _, managed := managedSet[key]; managed {
				continue
			}
			additions = append(additions, file)
			addSet[key] = file
			newlyAdded = append(newlyAdded, file)
		}
	}
	sort.Slice(additions, func(i, j int) bool { return additions[i] < additions[j] })
	sort.Slice(pendingRemoves, func(i, j int) bool { return pendingRemoves[i].Path < pendingRemoves[j].Path })
	sort.Slice(cancelledRemovals, func(i, j int) bool { return cancelledRemovals[i] < cancelledRemovals[j] })
	// A tree SPAS will publish must remain checkable on every supported
	// platform, so portability is judged case-insensitively here regardless
	// of the local filesystem.
	if err := collisionPortability(managedForExclusion(state, additions, nil)); err != nil {
		return err
	}

	managed := managedForExclusion(state, additions, nil)
	excludePath, err := repository.InfoExcludePath(ctx)
	if err != nil {
		return err
	}
	excludePlan, err := exclude.Build(excludePath, state.Exclude.BlockID, managed)
	if err != nil {
		return err
	}
	if err := a.approveExistingExclude(ctx, &state, excludePlan, options.ExistingExclude); err != nil {
		return err
	}

	mergeAction, err := a.planMergeProtection(ctx, repository, options.MergeProtection)
	if err != nil {
		return err
	}
	if options.DryRun {
		return a.write(map[string]any{
			"action":                 "add",
			"added":                  pathsToStrings(newlyAdded),
			"pendingAdds":            pathsToStrings(additions),
			"canceledRemovals":       pathsToStrings(cancelledRemovals),
			"skippedTrackedPaths":    pathsToStrings(skipped),
			"localExcludeWillChange": excludePlan.Changed,
			"mergeProtection":        mergeAction,
		})
	}

	if err := exclude.Apply(excludePlan); err != nil {
		return err
	}
	if err := a.verifyExclusion(ctx, repository, managed); err != nil {
		return errors.Join(err, exclude.Restore(excludePlan))
	}
	var enabledBranch string
	if mergeAction == string(MergeEnable) {
		status, err := mergeprotect.Enable(ctx, repository, &state)
		if err != nil {
			return errors.Join(err, exclude.Restore(excludePlan))
		}
		if _, found := state.Merge.ManagedBranches[status.Branch]; found {
			enabledBranch = status.Branch
		}
	}

	state.PendingAdds = pathsToStrings(additions)
	state.PendingRemoves = pendingRemoves
	if err := a.Store.Save(state); err != nil {
		rollbackErr := exclude.Restore(excludePlan)
		if enabledBranch != "" {
			managed := state.Merge.ManagedBranches[enabledBranch]
			if mergeErr := mergeprotect.RestoreBranch(ctx, repository, enabledBranch, managed); mergeErr != nil {
				rollbackErr = errors.Join(rollbackErr, mergeErr)
			}
		}
		return errors.Join(err, rollbackErr)
	}
	return a.write(map[string]any{
		"added":               pathsToStrings(newlyAdded),
		"canceledRemovals":    pathsToStrings(cancelledRemovals),
		"skippedTrackedPaths": pathsToStrings(skipped),
		"pendingSync":         len(additions) > 0,
	})
}

type RemoveOptions struct {
	Paths     []string
	MissingOK bool
	DryRun    bool
}

func (a App) Remove(ctx context.Context, options RemoveOptions) error {
	repository, state, err := a.linked(ctx)
	if err != nil {
		return err
	}
	if err := a.requirePrimarySingleWorktree(ctx, repository); err != nil {
		return err
	}
	if !options.DryRun {
		linkLock, err := lock.Acquire(filepath.Join(a.Store.DataDir, "locks"), state.LinkID)
		if err != nil {
			return err
		}
		defer linkLock.Release()
		state, err = a.loadState(repository.Root, repository.CommonDir)
		if err != nil {
			return err
		}
	}
	if state.ActiveMerge != nil {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("a private merge is in progress; run spas sync --continue or spas sync --abort before removing paths"))
	}
	if state.Materializing != nil {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("a previous sync requires recovery; run spas sync before removing paths"))
	}
	if state.Private.Initialized {
		mergeInProgress, mergeProbeErr := a.privateRepository(state).MergeInProgress()
		if mergeProbeErr != nil {
			return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("inspect private merge state: %w", mergeProbeErr))
		}
		if mergeInProgress {
			return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("a private merge is in progress; run spas sync --continue or spas sync --abort before removing paths"))
		}
	}
	ignoreCase, err := a.casePolicy(ctx, repository)
	if err != nil {
		return err
	}
	managedSet := canonicalSet(stringsToPaths(state.ManagedPaths), ignoreCase)
	pendingAdds := canonicalSet(stringsToPaths(state.PendingAdds), ignoreCase)
	removes := append([]linkstate.PendingRemoval{}, state.PendingRemoves...)
	removeIndex := make(map[string]int, len(removes))
	for index, removal := range removes {
		path, parseErr := pathmodel.Parse(removal.Path)
		if parseErr != nil {
			return parseErr
		}
		removeIndex[pathmodel.Canonical(path, ignoreCase)] = index
	}
	var unenrolled []string
	var refreshed []string
	for _, value := range options.Paths {
		requested, _, err := pathmodel.Resolve(repository.Root, a.PathBase, value)
		if err != nil {
			return spaserr.Wrap(spaserr.KindUnsupportedPath, fmt.Errorf("resolve managed path %q: %w", value, err))
		}
		canonical := pathmodel.Canonical(requested, ignoreCase)
		pendingPath, isPending := pendingAdds[canonical]
		managedPath, isManaged := managedSet[canonical]
		if !isPending && !isManaged {
			if options.MissingOK {
				continue
			}
			return fmt.Errorf("%q is not privately tracked or pending addition", requested)
		}
		path := requested
		if isManaged {
			path, err = authoritativeManagedPath(repository.Root, requested, managedPath)
			if err != nil {
				return err
			}
		} else if isPending {
			path, err = authoritativeManagedPath(repository.Root, requested, pendingPath)
			if err != nil {
				return err
			}
		}
		if isPending {
			delete(pendingAdds, canonical)
			if !isManaged {
				unenrolled = append(unenrolled, path.String())
			}
		}
		if isManaged {
			// Re-running remove is an explicit reconfirmation. Refresh the
			// snapshot so a file intentionally edited after the first request can
			// still be removed without an add/remove workaround.
			snapshot, err := snapshotFile(path.OSPath(repository.Root))
			if err != nil {
				return err
			}
			removal := linkstate.PendingRemoval{
				Path:        path.String(),
				Existed:     snapshot.Existed,
				RequestedAt: time.Now().UTC(),
			}
			if snapshot.Existed {
				removal.Digest = hex.EncodeToString(snapshot.Digest[:])
				removal.Executable = snapshot.Executable
			}
			if index, found := removeIndex[canonical]; found {
				removes[index] = removal
				refreshed = append(refreshed, path.String())
			} else {
				removes = append(removes, removal)
				removeIndex[canonical] = len(removes) - 1
			}
		}
	}
	sort.Slice(removes, func(i, j int) bool { return removes[i].Path < removes[j].Path })
	sort.Strings(unenrolled)
	sort.Strings(refreshed)
	removePaths := make([]string, 0, len(removes))
	for _, removal := range removes {
		removePaths = append(removePaths, removal.Path)
	}
	if options.DryRun {
		return a.write(map[string]any{
			"action":          "remove",
			"pendingAdds":     pathsToStrings(mapPathValues(pendingAdds)),
			"pendingRemovals": removePaths,
			"refreshed":       refreshed,
			"unenrolled":      unenrolled,
		})
	}
	state.PendingAdds = pathsToStrings(mapPathValues(pendingAdds))
	state.PendingRemoves = removes
	excludePath, err := repository.InfoExcludePath(ctx)
	if err != nil {
		return err
	}
	remainingManaged := managedForExclusion(state, stringsToPaths(state.PendingAdds), nil)
	plan, err := exclude.Build(
		excludePath,
		state.Exclude.BlockID,
		remainingManaged,
	)
	if err != nil {
		return err
	}
	if err := exclude.Apply(plan); err != nil {
		return err
	}
	if err := a.verifyExclusion(ctx, repository, remainingManaged); err != nil {
		return errors.Join(err, exclude.Restore(plan))
	}
	if err := a.Store.Save(state); err != nil {
		return errors.Join(err, exclude.Restore(plan))
	}
	for _, value := range unenrolled {
		if err := a.warnf("warning: %q is no longer excluded from public Git\n", value); err != nil {
			return err
		}
	}
	result := map[string]any{
		"pendingRemovals": removePaths,
		"pendingSync":     len(state.PendingAdds) > 0 || len(state.PendingRemoves) > 0,
	}
	if len(refreshed) > 0 {
		result["refreshedRemovals"] = refreshed
	}
	if len(unenrolled) > 0 {
		result["unenrolled"] = unenrolled
	}
	return a.write(result)
}

// trackedPathDecision resolves what to do with an add target that public Git
// already tracks: true means skip it, false with a nil error never occurs, and
// an error aborts. Interactive runs are offered the choice; `--skip-tracked`
// answers it ahead of time.
func (a App) trackedPathDecision(ctx context.Context, path pathmodel.Path, skipTracked bool) (bool, error) {
	if skipTracked {
		return true, nil
	}
	if !a.Prompt.Interactive {
		return false, nil
	}
	value, err := a.Prompt.Select(
		ctx,
		fmt.Sprintf("Public Git already tracks %q. Remove it from public tracking before adding it to SPAS.", path),
		[]interaction.Option{
			{Key: "s", Value: "skip", Label: "Skip this path"},
			{Key: "a", Value: "abort", Label: "Abort"},
		},
	)
	if err != nil {
		return false, err
	}
	return value == "skip", nil
}

func collisionPortability(paths []pathmodel.Path) error {
	if err := collision.PrivatePortabilityConflicts(paths, true); err != nil {
		return spaserr.Wrap(spaserr.KindUnsupportedPath, err)
	}
	return nil
}

type StatusOptions struct {
	ShowPaths bool
	Short     bool
}

type Status struct {
	Linked              bool                `json:"linked"`
	LinkID              string              `json:"linkId"`
	PublicWorkspace     string              `json:"publicWorkspace,omitempty"`
	PublicBranch        string              `json:"publicBranch,omitempty"`
	PrivateRepository   string              `json:"privateRepository"`
	PrivateBranch       string              `json:"privateBranch,omitempty"`
	PrivateInitialized  bool                `json:"privateInitialized"`
	PrivateClone        string              `json:"privateClone,omitempty"`
	PendingAdds         []string            `json:"pendingAdds"`
	PendingRemovals     []string            `json:"pendingRemovals"`
	ManagedFiles        int                 `json:"managedFiles"`
	WorkspaceModified   []string            `json:"workspaceModified"`
	WorkspaceMissing    []string            `json:"workspaceMissing"`
	PrivateCloneMissing []string            `json:"privateCloneMissing"`
	ExpectedPrivateHead string              `json:"expectedPrivateHead,omitempty"`
	ActualPrivateHead   string              `json:"actualPrivateHead,omitempty"`
	PrivateHeadMismatch bool                `json:"privateHeadMismatch"`
	PrivateAhead        *int                `json:"privateAhead,omitempty"`
	PrivateBehind       *int                `json:"privateBehind,omitempty"`
	PathConflicts       []string            `json:"pathConflicts"`
	ExclusionFailures   []string            `json:"exclusionFailures"`
	PendingRecovery     bool                `json:"pendingRecovery"`
	PrivateClean        *bool               `json:"privateClean,omitempty"`
	MergeProtection     mergeprotect.Status `json:"mergeProtection"`
}

func (a App) Status(ctx context.Context, options StatusOptions) error {
	repository, state, err := a.linked(ctx)
	if err != nil {
		return err
	}
	branch, err := repository.Branch(ctx)
	if err != nil {
		return err
	}
	mergeStatus, err := mergeprotect.Inspect(ctx, repository)
	if err != nil {
		return err
	}
	status := Status{
		Linked:             true,
		LinkID:             state.LinkID,
		PublicBranch:       branch,
		PrivateRepository:  state.Private.Repository,
		PrivateBranch:      state.Private.Branch,
		PrivateInitialized: state.Private.Initialized,
		PendingAdds:        append([]string{}, state.PendingAdds...),
		PendingRemovals:    state.PendingRemovalPaths(),
		ManagedFiles:       len(state.ManagedPaths),
		PendingRecovery:    state.Private.Initialization != nil || state.Materializing != nil || state.ActiveMerge != nil,
		MergeProtection:    mergeStatus,
	}
	if options.ShowPaths {
		status.PublicWorkspace = state.Public.Root
		status.PrivateClone = state.Private.LocalRepositoryPath
	}
	if state.Private.Initialized {
		repo := a.privateRepository(state)
		clean, cleanErr := repo.IsClean(ctx)
		if cleanErr != nil {
			return cleanErr
		}
		status.PrivateClean = &clean
		status.ExpectedPrivateHead = state.Private.ExpectedHead
		actualHead, headErr := repo.Head(ctx)
		if headErr != nil {
			return headErr
		}
		status.ActualPrivateHead = actualHead
		status.PrivateHeadMismatch = !privateHeadMatches(state, actualHead)
		ahead, behind, known, err := repo.AheadBehind(ctx, state.Private.Branch)
		if err != nil {
			return err
		}
		if known {
			status.PrivateAhead = &ahead
			status.PrivateBehind = &behind
		}
		for _, value := range state.ManagedPaths {
			path, err := pathmodel.Parse(value)
			if err != nil {
				return err
			}
			publicPath := path.OSPath(repository.Root)
			privatePath := path.OSPath(repo.Path)
			if _, err := os.Stat(publicPath); errors.Is(err, os.ErrNotExist) {
				status.WorkspaceMissing = append(status.WorkspaceMissing, value)
				continue
			} else if err != nil {
				return err
			}
			if _, err := os.Stat(privatePath); errors.Is(err, os.ErrNotExist) {
				status.PrivateCloneMissing = append(status.PrivateCloneMissing, value)
				continue
			} else if err != nil {
				return err
			}
			equal, err := filesync.Equal(publicPath, privatePath)
			if err != nil {
				return err
			}
			if !equal {
				status.WorkspaceModified = append(status.WorkspaceModified, value)
			}
		}
	}
	excludedPaths := managedForExclusion(state, stringsToPaths(state.PendingAdds), nil)
	unexcluded, err := repository.UnexcludedPaths(ctx, excludedPaths)
	if err != nil {
		return err
	}
	for _, path := range unexcluded {
		status.ExclusionFailures = append(status.ExclusionFailures, path.String())
	}
	ignoreCase, present, err := repository.EffectiveIgnoreCase(ctx)
	if err != nil {
		return err
	}
	if !present {
		// Status is read-only, so it does not create a filesystem case probe.
		// The conservative policy avoids hiding a possible collision.
		ignoreCase = true
	}
	publicPaths, err := repository.TrackedPaths(ctx)
	if err != nil {
		return err
	}
	for _, conflict := range collision.Detect(publicPaths, excludedPaths, ignoreCase) {
		status.PathConflicts = append(status.PathConflicts, conflict.Error())
	}
	sort.Strings(status.WorkspaceModified)
	sort.Strings(status.WorkspaceMissing)
	sort.Strings(status.PrivateCloneMissing)
	sort.Strings(status.ExclusionFailures)
	sort.Strings(status.PathConflicts)
	if a.JSON {
		return a.write(status)
	}
	if options.Short {
		_, err := fmt.Fprintf(a.Out, "linked=%t initialized=%t managed=%d modified=%d missing=%d conflicts=%d pending_recovery=%t\n",
			status.Linked,
			status.PrivateInitialized,
			status.ManagedFiles,
			len(status.WorkspaceModified),
			len(status.WorkspaceMissing),
			len(status.PathConflicts),
			status.PendingRecovery,
		)
		return err
	}
	aheadBehind := "unknown (no cached comparison)"
	if status.PrivateAhead != nil && status.PrivateBehind != nil {
		aheadBehind = fmt.Sprintf("%d ahead, %d behind", *status.PrivateAhead, *status.PrivateBehind)
	}
	if _, err = fmt.Fprintf(a.Out, "Link:\n  Linked repository:   %s\n  Linked branch:       %s\n  Initialized:         %s\n",
		status.PrivateRepository,
		valueOr(status.PrivateBranch, "discover on first sync"),
		boolState(status.PrivateInitialized),
	); err != nil {
		return err
	}
	if options.ShowPaths {
		if _, err = fmt.Fprintf(a.Out, "  Managed checkout:    %s\n", status.PrivateClone); err != nil {
			return err
		}
	}
	if _, err = fmt.Fprintf(a.Out, "\nProject workspace:\n  Branch:               %s\n  Merge protection:    %s\n",
		valueOr(status.PublicBranch, "detached HEAD"),
		boolState(status.MergeProtection.Enabled),
	); err != nil {
		return err
	}
	if options.ShowPaths {
		if _, err = fmt.Fprintf(a.Out, "  Path:                 %s\n", status.PublicWorkspace); err != nil {
			return err
		}
	}
	privateHeadStatus := "not initialized"
	if status.PrivateInitialized {
		privateHeadStatus = "bound"
	}
	if status.PrivateHeadMismatch {
		privateHeadStatus = fmt.Sprintf("mismatch (expected %s, actual %s)", valueOr(status.ExpectedPrivateHead, "unborn"), valueOr(status.ActualPrivateHead, "unborn"))
	}
	_, err = fmt.Fprintf(a.Out, "\nPrivate files:\n  Managed:              %d\n  Modified locally:     %d\n  Missing locally:      %d\n  Pending additions:    %d\n  Pending removals:     %d\n  Cached remote state:  %s\n\nHealth:\n  Private HEAD binding:  %s\n  Path conflicts:       %d\n  Exclusion failures:   %d\n  Pending recovery:     %s\n",
		status.ManagedFiles,
		len(status.WorkspaceModified),
		len(status.WorkspaceMissing),
		len(status.PendingAdds),
		len(status.PendingRemovals),
		aheadBehind,
		privateHeadStatus,
		len(status.PathConflicts),
		len(status.ExclusionFailures),
		boolState(status.PendingRecovery),
	)
	return err
}

type UnlinkOptions struct {
	Force              bool
	RemoveFiles        bool
	ApproveRemoveFiles bool
	RemovePrivateClone bool
}

func (a App) Unlink(ctx context.Context, options UnlinkOptions) error {
	repository, state, err := a.linked(ctx)
	if err != nil {
		return err
	}
	if err := a.requirePrimarySingleWorktree(ctx, repository); err != nil {
		return err
	}
	linkLock, err := lock.Acquire(filepath.Join(a.Store.DataDir, "locks"), state.LinkID)
	if err != nil {
		return err
	}
	defer linkLock.Release()
	state, err = a.loadState(repository.Root, repository.CommonDir)
	if err != nil {
		return err
	}
	if !options.Force && (state.Materializing != nil || state.Private.Initialization != nil) {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("a previous sync or first-clone initialization requires recovery; run spas sync or use --force to unlink"))
	}
	if !options.Force && (len(state.PendingAdds) > 0 || len(state.PendingRemoves) > 0) {
		return fmt.Errorf("private changes are pending; sync them or use --force")
	}
	if state.Private.Initialized && !options.Force {
		private := a.privateRepository(state)
		clean, err := private.IsClean(ctx)
		if err != nil {
			return err
		}
		if !clean {
			return fmt.Errorf("private changes are pending; sync them or use --force")
		}
		unpushed, err := private.HasUnpushedCommits(ctx, state.Private.Branch)
		if err != nil {
			return err
		}
		if unpushed {
			return fmt.Errorf("private commits have not been pushed; sync them or use --force")
		}
	}

	if state.ActiveMerge != nil && len(state.ActiveMerge.ConflictPaths) == 0 {
		// An abort-only or interrupted merge state has no recorded conflict paths.
		// Without that evidence, unlink cannot prove which private conflict files
		// removing the local exclusion block might expose to public Git, so require
		// the merge to be aborted before unlinking.
		return spaserr.Wrap(
			spaserr.KindUnsafeGitState,
			fmt.Errorf("active private merge state does not record its materialized conflict paths; run spas sync --abort before unlinking"),
		)
	}
	removePaths, err := unlinkWorkspacePaths(state)
	if err != nil {
		return err
	}
	if options.RemoveFiles {
		if !options.ApproveRemoveFiles {
			approved, err := a.Prompt.Confirm(ctx, "Remove every SPAS-managed workspace file?", false, false)
			if err != nil {
				return err
			}
			if !approved {
				return fmt.Errorf("managed-file removal declined")
			}
		}
		// Remove files before metadata. If a later unlink step fails, the link
		// and its exclusions remain recoverable and a subsequent sync can
		// restore files that already existed in the private repository.
		for _, path := range removePaths {
			if err := filesync.RemoveManaged(repository.Root, path); err != nil {
				return err
			}
		}
	}

	excludePath, err := repository.InfoExcludePath(ctx)
	if err != nil {
		return err
	}
	plan, err := exclude.Build(excludePath, state.Exclude.BlockID, nil)
	if err != nil {
		return err
	}
	if err := exclude.Apply(plan); err != nil {
		return err
	}
	rollbackMetadata := func(primary error) error {
		return errors.Join(primary, exclude.Restore(plan), mergeprotect.Reapply(ctx, repository, state))
	}
	if err := mergeprotect.Restore(ctx, repository, state); err != nil {
		return rollbackMetadata(err)
	}
	if err := a.Store.Delete(state.LinkID); err != nil {
		return rollbackMetadata(err)
	}

	result := map[string]any{
		"unlinked":  true,
		"keptFiles": !options.RemoveFiles,
	}
	if !options.RemoveFiles && len(removePaths) > 0 {
		result["workspaceFilesNowVisibleToPublicGit"] = pathsToStrings(removePaths)
	}
	if options.RemovePrivateClone {
		clonePaths := []string{
			state.Private.LocalRepositoryPath,
			a.privateRepository(state).StagingPath(),
		}
		var cleanupFailures []error
		var retainedPaths []string
		for _, clonePath := range clonePaths {
			if err := removePrivateClone(clonePath); err != nil {
				retainedPaths = append(retainedPaths, clonePath)
				cleanupFailures = append(cleanupFailures, fmt.Errorf("remove private clone path %s: %w", clonePath, err))
			}
		}
		if len(cleanupFailures) > 0 {
			return spaserr.Wrap(
				spaserr.KindOperation,
				fmt.Errorf("workspace was successfully unlinked, but private clone cleanup failed for retained path(s) %s: %w", strings.Join(retainedPaths, ", "), errors.Join(cleanupFailures...)),
			)
		}
		result["privateCloneRemoved"] = true
	}
	return a.write(result)
}

func unlinkWorkspacePaths(state linkstate.State) ([]pathmodel.Path, error) {
	values := append([]string{}, state.ManagedPaths...)
	values = append(values, state.PendingAdds...)
	if state.ActiveMerge != nil {
		values = append(values, state.ActiveMerge.ConflictPaths...)
		values = append(values, state.ActiveMerge.RemainingPendingAdds...)
	}
	if state.Materializing != nil {
		values = append(values, state.Materializing.ExcludedPaths...)
		values = append(values, state.Materializing.RemainingPendingAdds...)
	}
	set := make(map[string]pathmodel.Path, len(values))
	for _, value := range values {
		path, err := pathmodel.Parse(value)
		if err != nil {
			return nil, err
		}
		set[path.String()] = path
	}
	paths := make([]pathmodel.Path, 0, len(set))
	for _, path := range set {
		paths = append(paths, path)
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i] < paths[j] })
	return paths, nil
}

func (a App) publicRepository(ctx context.Context) (publicgit.Repository, error) {
	return publicgit.Discover(ctx, a.Git, a.RepoHint)
}

func (a App) linked(ctx context.Context) (publicgit.Repository, linkstate.State, error) {
	repository, err := a.publicRepository(ctx)
	if err != nil {
		return publicgit.Repository{}, linkstate.State{}, err
	}
	state, err := a.loadState(repository.Root, repository.CommonDir)
	if err != nil {
		return publicgit.Repository{}, linkstate.State{}, err
	}
	if state.Private.Branch != "" {
		if err := privategit.ValidateBranchName(ctx, a.Git, repository.Root, state.Private.Branch); err != nil {
			return publicgit.Repository{}, linkstate.State{}, fmt.Errorf("link state contains invalid private branch %q", state.Private.Branch)
		}
	}
	return repository, state, nil
}

func (a App) loadState(publicRoot, commonDir string) (linkstate.State, error) {
	state, err := a.Store.Load(publicRoot, commonDir)
	if err != nil {
		return linkstate.State{}, err
	}
	if err := a.validateRepositoryIdentity(state); err != nil {
		return linkstate.State{}, err
	}
	return state, nil
}

func (a App) privateRepository(state linkstate.State) privategit.Repository {
	privateGit := a.Git
	privateGit.NoLazyFetch = true
	return privategit.Repository{
		Path: state.Private.LocalRepositoryPath,
		Git:  privateGit,
		// Safety files must be link-scoped. Mutating commands are serialized per
		// link, not globally, so one shared hooks/attributes directory would let
		// concurrent syncs for different projects reset files underneath each
		// other.
		SafetyDir: filepath.Join(a.Store.DataDir, "safety", state.LinkID),
	}
}

func (a App) validateRepositoryIdentity(state linkstate.State) error {
	if a.Provider == nil {
		return fmt.Errorf("repository provider is not configured")
	}
	if state.Private.Provider != a.Provider.ID() {
		return fmt.Errorf("link state contains unknown repository provider %q", state.Private.Provider)
	}
	ref, err := a.Provider.Resolve(provider.RepositoryRequest{
		Raw:       state.Private.Repository,
		Transport: state.Private.Transport,
	})
	if err != nil {
		return fmt.Errorf("re-resolve linked repository: %w", err)
	}
	if ref.Provider != state.Private.Provider ||
		ref.Canonical != state.Private.Repository ||
		ref.Transport != state.Private.Transport ||
		ref.RemoteURL != state.Private.RemoteURL {
		return fmt.Errorf("link state repository identity does not match provider resolution")
	}
	return nil
}

func (a App) expandPaths(root string, values []string) ([]pathmodel.Path, error) {
	set := make(map[string]pathmodel.Path)
	for _, value := range values {
		path, absolute, err := pathmodel.Resolve(root, a.PathBase, value)
		if err != nil {
			return nil, spaserr.Wrap(spaserr.KindUnsupportedPath, fmt.Errorf("resolve managed path %q: %w", value, err))
		}
		info, err := os.Lstat(absolute)
		if err != nil {
			return nil, fmt.Errorf("inspect %q: %w", value, err)
		}
		if info.Mode().IsRegular() {
			if err := privategit.ValidateManagedPath(path); err != nil {
				return nil, spaserr.Wrap(spaserr.KindUnsupportedPath, err)
			}
			if err := pathmodel.ValidateNoSymlinkComponents(root, path); err != nil {
				return nil, spaserr.Wrap(spaserr.KindUnsupportedPath, err)
			}
			if _, statErr := os.Lstat(path.OSPath(root)); statErr != nil {
				return nil, spaserr.Wrap(spaserr.KindUnsupportedPath, fmt.Errorf(
					"%q: the on-disk name does not match its Unicode NFC form and cannot be enrolled portably; rename the file to its NFC spelling", value))
			}
			set[path.String()] = path
			continue
		}
		if !info.IsDir() {
			return nil, spaserr.Wrap(spaserr.KindUnsupportedPath, fmt.Errorf("%q is not a regular file or directory", value))
		}
		err = filepath.WalkDir(absolute, func(current string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if current == absolute {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return spaserr.Wrap(spaserr.KindUnsupportedPath, fmt.Errorf("directory %q contains symbolic link %q", value, current))
			}
			if entry.IsDir() {
				if strings.EqualFold(entry.Name(), ".git") {
					return spaserr.Wrap(spaserr.KindUnsupportedPath, fmt.Errorf("directory %q contains nested Git metadata", value))
				}
				return nil
			}
			if !entry.Type().IsRegular() {
				return spaserr.Wrap(spaserr.KindUnsupportedPath, fmt.Errorf("directory %q contains unsupported file type %q", value, current))
			}
			relative, err := filepath.Rel(root, current)
			if err != nil {
				return err
			}
			managed, err := pathmodel.Parse(filepath.ToSlash(relative))
			if err != nil {
				return spaserr.Wrap(spaserr.KindUnsupportedPath, err)
			}
			if err := privategit.ValidateManagedPath(managed); err != nil {
				return spaserr.Wrap(spaserr.KindUnsupportedPath, err)
			}
			if _, statErr := os.Lstat(managed.OSPath(root)); statErr != nil {
				return spaserr.Wrap(spaserr.KindUnsupportedPath, fmt.Errorf(
					"%q: the on-disk name does not match its Unicode NFC form and cannot be enrolled portably; rename the file to its NFC spelling", current))
			}
			set[managed.String()] = managed
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	result := make([]pathmodel.Path, 0, len(set))
	for _, path := range set {
		result = append(result, path)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) == 0 {
		return nil, fmt.Errorf("no regular files were selected")
	}
	return result, nil
}

func (a App) approveExistingExclude(ctx context.Context, state *linkstate.State, plan exclude.Plan, policy ExistingExcludePolicy) error {
	if !plan.ExistingUserRules || state.Exclude.ExistingContentApproved {
		return nil
	}
	switch policy {
	case ExcludePreserve:
		state.Exclude.ExistingContentApproved = true
		return nil
	case ExcludeAbort:
		return fmt.Errorf("the public repository local exclude file already contains user rules")
	case ExcludeAsk, "":
		approved, err := a.Prompt.Confirm(
			ctx,
			"The public repository local exclude file already has rules. Preserve them and append a separate SPAS block?",
			true,
			true,
		)
		if err != nil {
			return err
		}
		if !approved {
			return fmt.Errorf("local exclude update declined")
		}
		state.Exclude.ExistingContentApproved = true
		return nil
	default:
		return fmt.Errorf("invalid existing-exclude policy %q", policy)
	}
}

func (a App) planMergeProtection(ctx context.Context, repository publicgit.Repository, policy MergeProtectionPolicy) (string, error) {
	status, err := mergeprotect.Inspect(ctx, repository)
	if err != nil {
		return "", err
	}
	if status.Enabled {
		return "already-enabled", nil
	}
	if status.Branch == "" {
		if policy == MergeEnable || policy == MergeRequire {
			return "", spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("merge protection policy %q requires a public branch, but HEAD is detached", policy))
		}
		return string(MergeSkip), nil
	}
	if status.Ambiguous {
		if policy == MergeEnable || policy == MergeRequire {
			return "", spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("merge protection policy %q cannot install protection on public branch %q because it has multiple mergeOptions values", policy, status.Branch))
		}
		if policy == MergeAsk {
			if err := a.warnf(
				"warning: public branch %q has multiple mergeOptions values; SPAS will not modify them. Add --no-overwrite-ignore to that branch's local merge options manually if you want overwrite protection.\n",
				status.Branch,
			); err != nil {
				return "", err
			}
		}
		return string(MergeSkip), nil
	}
	switch policy {
	case MergeEnable:
		return string(MergeEnable), nil
	case MergeSkip:
		return string(MergeSkip), nil
	case MergeRequire:
		return "", mergeprotect.PolicyError(status)
	case MergeAsk, "":
		approved, err := a.Prompt.Confirm(
			ctx,
			fmt.Sprintf("Enable repository-local merge overwrite protection for public branch %q?", status.Branch),
			true,
			true,
		)
		if err != nil {
			return "", err
		}
		if approved {
			return string(MergeEnable), nil
		}
		return string(MergeSkip), nil
	default:
		return "", fmt.Errorf("invalid merge-protection policy %q", policy)
	}
}

func (a App) casePolicy(ctx context.Context, repository publicgit.Repository) (bool, error) {
	configValue, present, err := repository.EffectiveIgnoreCase(ctx)
	if err != nil {
		return false, err
	}
	filesystemValue, err := repository.FilesystemIgnoresCase()
	if err != nil {
		return false, err
	}
	if present && configValue != filesystemValue {
		if err := a.warnf(
			"warning: Git core.ignoreCase=%t disagrees with the public workspace filesystem; SPAS will use case-insensitive collision checks\n",
			configValue,
		); err != nil {
			return false, err
		}
		return true, nil
	}
	if !present {
		return filesystemValue, nil
	}
	return configValue, nil
}

func (a App) requirePrimarySingleWorktree(ctx context.Context, repository publicgit.Repository) error {
	count, err := repository.WorktreeCount(ctx)
	if err != nil {
		return err
	}
	if count != 1 {
		message := fmt.Sprintf("SPAS mutating commands require the primary and only Git worktree; found %d worktree records", count)
		if count > 1 {
			message = fmt.Sprintf("the current implementation does not mutate repository-local exclusions when multiple Git worktrees share the repository; found %d worktree records", count)
		}
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("%s", message))
	}
	if repository.GitDir == "" || repository.CommonDir == "" {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("could not identify the primary Git worktree directory"))
	}
	gitInfo, err := os.Stat(repository.GitDir)
	if err != nil {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("inspect Git worktree directory %q: %w", repository.GitDir, err))
	}
	commonInfo, err := os.Stat(repository.CommonDir)
	if err != nil {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("inspect Git common directory %q: %w", repository.CommonDir, err))
	}
	if !os.SameFile(gitInfo, commonInfo) {
		return spaserr.Wrap(
			spaserr.KindUnsafeGitState,
			fmt.Errorf("SPAS mutating commands require the primary Git worktree; Git directory %q differs from common directory %q", repository.GitDir, repository.CommonDir),
		)
	}
	return nil
}

func (a App) warnf(format string, arguments ...any) error {
	if a.JSON || a.Err == nil {
		return nil
	}
	_, err := fmt.Fprintf(a.Err, format, arguments...)
	return err
}

func (a App) write(value any) error {
	if a.JSON {
		encoder := json.NewEncoder(a.Out)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(value)
	}
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			item := typed[key]
			if _, err := fmt.Fprintf(a.Out, "%s: %v\n", humanize(key), item); err != nil {
				return err
			}
		}
	default:
		return json.NewEncoder(a.Out).Encode(value)
	}
	return nil
}

type OutputWrittenError struct {
	Err error
}

func (e OutputWrittenError) Error() string {
	return e.Err.Error()
}

func (e OutputWrittenError) Unwrap() error {
	return e.Err
}

func (OutputWrittenError) OutputWritten() bool {
	return true
}

func managedForExclusion(state linkstate.State, pendingAdds, skipped []pathmodel.Path) []pathmodel.Path {
	skip := make(map[string]struct{}, len(skipped))
	for _, path := range skipped {
		skip[path.String()] = struct{}{}
	}
	set := make(map[string]pathmodel.Path)
	for _, value := range state.ManagedPaths {
		set[value] = pathmodel.Path(value)
	}
	for _, path := range pendingAdds {
		set[path.String()] = path
	}
	result := make([]pathmodel.Path, 0, len(set))
	for value, path := range set {
		if _, found := skip[value]; found {
			continue
		}
		result = append(result, path)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func stringsToPaths(values []string) []pathmodel.Path {
	result := make([]pathmodel.Path, 0, len(values))
	for _, value := range values {
		result = append(result, pathmodel.Path(value))
	}
	return result
}

func pathsToStrings(paths []pathmodel.Path) []string {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		result = append(result, path.String())
	}
	return result
}

func canonicalSet(paths []pathmodel.Path, ignoreCase bool) map[string]pathmodel.Path {
	result := make(map[string]pathmodel.Path, len(paths))
	for _, path := range paths {
		result[pathmodel.Canonical(path, ignoreCase)] = path
	}
	return result
}

// authoritativeManagedPath resolves a case-equivalent user spelling to the
// spelling already stored by SPAS. If both spellings exist as different files
// on a case-sensitive filesystem while Git is configured case-insensitively,
// treating them as the same path would operate on the wrong file.
func authoritativeManagedPath(root string, requested, authoritative pathmodel.Path) (pathmodel.Path, error) {
	if requested == authoritative {
		return authoritative, nil
	}
	requestedInfo, requestedErr := os.Lstat(requested.OSPath(root))
	authoritativeInfo, authoritativeErr := os.Lstat(authoritative.OSPath(root))
	if requestedErr == nil && authoritativeErr == nil {
		if os.SameFile(requestedInfo, authoritativeInfo) {
			return authoritative, nil
		}
		return "", spaserr.Wrap(spaserr.KindUnsupportedPath, fmt.Errorf(
			"%q and privately managed path %q are distinct files whose names collide under the current case policy",
			requested, authoritative,
		))
	}
	if requestedErr == nil && errors.Is(authoritativeErr, os.ErrNotExist) {
		return "", spaserr.Wrap(spaserr.KindUnsupportedPath, fmt.Errorf(
			"%q differs only by case from privately managed path %q; use the managed spelling",
			requested, authoritative,
		))
	}
	if requestedErr != nil && !errors.Is(requestedErr, os.ErrNotExist) {
		return "", requestedErr
	}
	if authoritativeErr != nil && !errors.Is(authoritativeErr, os.ErrNotExist) {
		return "", authoritativeErr
	}
	return authoritative, nil
}

func humanize(value string) string {
	return strings.ReplaceAll(value, "_", " ")
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func boolState(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
