package app

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
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
	"github.com/getspas/spas/internal/limits"
	"github.com/getspas/spas/internal/linkstate"
	"github.com/getspas/spas/internal/lock"
	"github.com/getspas/spas/internal/mergeprotect"
	"github.com/getspas/spas/internal/pathmodel"
	"github.com/getspas/spas/internal/privategit"
	"github.com/getspas/spas/internal/provider"
	"github.com/getspas/spas/internal/publicgit"
	"github.com/getspas/spas/internal/recovery"
	"github.com/getspas/spas/internal/spaserr"
)

type ConflictPolicy string

const (
	ConflictAsk      ConflictPolicy = "ask"
	ConflictSkip     ConflictPolicy = "skip"
	ConflictOverride ConflictPolicy = "override"
	ConflictAbort    ConflictPolicy = "abort"
)

// maxPushAttempts permits the initial push and at most one structured
// non-fast-forward refresh attempt.
const maxPushAttempts = 2

type SyncOptions struct {
	Message              string
	Conflict             ConflictPolicy
	DiscardPublicChanges bool
	ExistingExclude      ExistingExcludePolicy
	MergeProtection      MergeProtectionPolicy
	Branch               string
	Continue             bool
	Abort                bool
	DryRun               bool
	AllowPublic          bool
}

var ErrPrivateMergeConflict = errors.New("private merge conflict")

type plannedChange struct {
	Path   pathmodel.Path
	Status string
}

type fileSnapshot struct {
	Digest     [32]byte
	Existed    bool
	Executable bool
}

func (a App) Sync(ctx context.Context, options SyncOptions) (returnErr error) {
	repository, state, err := a.linked(ctx)
	if err != nil {
		return err
	}
	if options.Continue {
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
		if err := filesync.CleanupManagedTemps(repository.Root); err != nil {
			return err
		}
		return a.continueSync(ctx, repository, &state, options)
	}
	if options.Abort {
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
		if err := filesync.CleanupManagedTemps(repository.Root); err != nil {
			return err
		}
		return a.abortSync(ctx, repository, &state)
	}
	if err := a.requirePrimarySingleWorktree(ctx, repository); err != nil {
		return err
	}
	if options.DryRun {
		return a.syncDryRun(ctx, repository, state, options)
	}

	linkLock, err := lock.Acquire(filepath.Join(a.Store.DataDir, "locks"), state.LinkID)
	if err != nil {
		return err
	}
	defer linkLock.Release()
	if err := filesync.CleanupManagedTemps(repository.Root); err != nil {
		return err
	}
	state, err = a.loadState(repository.Root, repository.CommonDir)
	if err != nil {
		return err
	}

	if !options.AllowPublic && a.Provider != nil {
		ref := provider.RepositoryRef{
			Provider:  state.Private.Provider,
			Canonical: state.Private.Repository,
			Transport: state.Private.Transport,
			RemoteURL: state.Private.RemoteURL,
		}
		isPublic, probeErr := a.Provider.ProbePublic(ctx, a.Git, ref)
		if probeErr != nil {
			return probeErr
		}
		if isPublic {
			approved, err := a.Prompt.Confirm(
				ctx,
				fmt.Sprintf("Repository %q is publicly readable on GitHub. Syncing will make managed assets publicly accessible. Continue?", state.Private.Repository),
				false,
				false,
			)
			if err != nil {
				return err
			}
			if !approved {
				return fmt.Errorf("syncing to publicly readable repository declined")
			}
		}
	}
	ignoreCase, err := a.casePolicy(ctx, repository)
	if err != nil {
		return err
	}
	unmerged, err := repository.UnmergedPaths(ctx)
	if err != nil {
		return err
	}
	if len(unmerged) > 0 {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("public repository has unresolved merge conflicts"))
	}

	private := a.privateRepository(state)
	remoteURL := state.Private.RemoteURL
	if !state.Private.Initialized {
		if err := a.initializePrivateRepository(ctx, &state, private, remoteURL, options.Branch); err != nil {
			return err
		}
	} else if options.Branch != "" && options.Branch != state.Private.Branch {
		return fmt.Errorf("private branch is already fixed to %q", state.Private.Branch)
	}
	if err := private.EnsureSafety(ctx); err != nil {
		return err
	}
	if err := private.VerifyOrigin(ctx, remoteURL); err != nil {
		return err
	}
	// Complete a pushed private result before reading workspace bytes as new
	// local edits.
	if err := a.resumeMaterialization(ctx, repository, &state, private, ignoreCase); err != nil {
		return err
	}

	mergeInProgress, err := a.reconcileNormalMergeState(ctx, &state, private)
	if err != nil {
		return err
	}
	if mergeInProgress {
		if state.ActiveMerge != nil && !state.ActiveMerge.ConflictFilesReady {
			return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("private merge conflict materialization was interrupted; use spas sync --abort"))
		}
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("a private merge is in progress; use spas sync --continue or spas sync --abort"))
	}
	if err := verifyExpectedPrivateHead(ctx, state, private); err != nil {
		return err
	}
	clean, err := private.IsClean(ctx)
	if err != nil {
		return err
	}
	if !clean {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("SPAS-managed private clone is not clean; run spas doctor"))
	}
	privateBranch, err := private.Branch(ctx)
	if err != nil {
		return err
	}
	if privateBranch != state.Private.Branch {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("private clone is on branch %q, expected %q", privateBranch, state.Private.Branch))
	}
	if localHead, err := private.Head(ctx); err != nil {
		return err
	} else if localHead != "" {
		if err := private.ValidateTree(ctx, localHead); err != nil {
			return err
		}
	}

	previouslyEmpty := state.Private.RemoteEmpty
	remoteBranchExists, err := private.RemoteBranchExists(ctx, state.Private.Branch)
	if err != nil {
		return err
	}
	if !remoteBranchExists && !previouslyEmpty {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("private remote branch %q disappeared; SPAS will not recreate it automatically", state.Private.Branch))
	}
	remoteEmpty := !remoteBranchExists
	if remoteBranchExists {
		if err := private.Fetch(ctx, state.Private.Branch); err != nil {
			return err
		}
		if err := private.ValidateTree(ctx, "refs/remotes/origin/"+state.Private.Branch); err != nil {
			return err
		}
	}

	localPrivatePaths, err := private.TrackedPaths(ctx)
	if err != nil {
		return err
	}
	remotePrivatePaths := localPrivatePaths
	if !remoteEmpty {
		remotePrivatePaths, err = private.TreePaths(ctx, "refs/remotes/origin/"+state.Private.Branch)
		if err != nil {
			return err
		}
	}
	pendingAdds := stringsToPaths(state.PendingAdds)
	localCandidate := unionPaths(localPrivatePaths, pendingAdds)
	// Portability is judged per actual revision, not across the union of the
	// old and fetched trees. The union may legitimately contain both spellings
	// of a case-only rename even though no Git revision contains both. The current implementation
	// still rejects file/directory transitions between revisions because they
	// require a different ordered materialization and recovery protocol.
	if err := collision.PrivateRevisionCompatibility(localCandidate, remotePrivatePaths, true); err != nil {
		return spaserr.Wrap(spaserr.KindUnsupportedPath, err)
	}
	candidatePrivate := unionPaths(localCandidate, remotePrivatePaths)
	if err := validateProspectivePrivateTreeSize(candidatePrivate); err != nil {
		return err
	}
	// groupByCanonical supports the case-only override's ambiguity check.

	publicPaths, err := repository.TrackedPaths(ctx)
	if err != nil {
		return err
	}
	// Only paths already present in the private repository (locally or on the
	// fetched remote) can replace a public path during an ownership override.
	privateReplacements := stringSet(pathsToStrings(unionPaths(localPrivatePaths, remotePrivatePaths)))
	publicByCanonical := groupByCanonical(publicPaths, ignoreCase)
	privateByCanonical := groupByCanonical(candidatePrivate, ignoreCase)
	conflicts := collision.Detect(publicPaths, candidatePrivate, ignoreCase)
	skipped := make(map[string]struct{})
	overridePublic := make(map[string]pathmodel.Path)
	// caseOverridePrivate holds the private spelling of an approved case-only
	// override; its public counterpart is in overridePublic.
	caseOverridePrivate := make(map[string]pathmodel.Path)
	// overrideReplacement maps each approved override's public spelling to
	// the private spelling that must exist in the pushed tree before the
	// public copy may be removed.
	overrideReplacement := make(map[string]string)
	overrideStatuses := make(map[string][]byte)
	overrideSnapshots := make(map[string]fileSnapshot)
	for _, conflict := range conflicts {
		decision, err := a.conflictDecision(ctx, conflict.Error(), options.Conflict)
		if err != nil {
			return err
		}
		switch decision {
		case ConflictSkip:
			skipped[conflict.Private.String()] = struct{}{}
		case ConflictOverride:
			switch conflict.Kind {
			case collision.TrackedPath:
				if _, has := privateReplacements[conflict.Public.String()]; !has {
					return spaserr.Wrap(
						spaserr.KindPathConflict,
						fmt.Errorf("cannot override %q: the private repository has no replacement for this path; remove it from public tracking before retrying", conflict.Public),
					)
				}
				overridePublic[conflict.Public.String()] = conflict.Public
				overrideReplacement[conflict.Public.String()] = conflict.Public.String()
			case collision.CaseInsensitive:
				// A case-only override is automatic only when exactly one
				// public and one private spelling collapse to the same
				// normalized form.
				canonical := pathmodel.Canonical(conflict.Private, ignoreCase)
				if len(publicByCanonical[canonical]) != 1 || len(privateByCanonical[canonical]) != 1 {
					return spaserr.Wrap(spaserr.KindPathConflict, fmt.Errorf("%s; multiple paths collapse to one normalized form, so automatic override is not possible — rename them manually", conflict.Error()))
				}
				if _, has := privateReplacements[conflict.Private.String()]; !has {
					return spaserr.Wrap(
						spaserr.KindPathConflict,
						fmt.Errorf("cannot override %q: the private repository has no replacement for this path; remove it from public tracking before retrying", conflict.Public),
					)
				}
				overridePublic[conflict.Public.String()] = conflict.Public
				caseOverridePrivate[conflict.Private.String()] = conflict.Private
				overrideReplacement[conflict.Public.String()] = conflict.Private.String()
			default:
				return spaserr.Wrap(spaserr.KindPathConflict, fmt.Errorf("%s; automatic override supports exact tracked-path and unambiguous case-only conflicts in the current implementation", conflict.Error()))
			}
		case ConflictAbort:
			return spaserr.Wrap(spaserr.KindPathConflict, fmt.Errorf("%s", conflict.Error()))
		}
	}

	stateManaged := stringSet(state.ManagedPaths)
	pendingSet := stringSet(state.PendingAdds)
	privateSet := stringSet(pathsToStrings(candidatePrivate))
	obstructionOverrides := make(map[string]pathmodel.Path)
	for value := range privateSet {
		if _, known := stateManaged[value]; known {
			continue
		}
		if _, pending := pendingSet[value]; pending {
			continue
		}
		if _, skip := skipped[value]; skip {
			continue
		}
		path, err := pathmodel.Parse(value)
		if err != nil {
			return err
		}
		target := path.OSPath(repository.Root)
		info, err := os.Lstat(target)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return spaserr.Wrap(spaserr.KindUnsupportedPath, fmt.Errorf("untracked working tree path %q would be overwritten and is not a regular file", path))
		}
		// A workspace file byte-identical to the local private version has
		// nothing to lose — adopt it silently. This is the normal shape of
		// recovered or replayed link state.
		if equal, equalErr := filesync.Equal(target, path.OSPath(private.Path)); equalErr == nil && equal {
			continue
		}
		decision, err := a.conflictDecision(
			ctx,
			fmt.Sprintf("untracked working tree file %q would be overwritten by the private repository", path),
			options.Conflict,
		)
		if err != nil {
			return err
		}
		switch decision {
		case ConflictSkip:
			skipped[value] = struct{}{}
		case ConflictOverride:
			// The private repository remains the source for this new path.
			// A recovery copy of the unmanaged file is saved before it is
			// replaced.
			obstructionOverrides[value] = path
		case ConflictAbort:
			return spaserr.Wrap(spaserr.KindPathConflict, fmt.Errorf("untracked working tree file %q would be overwritten", path))
		}
	}

	for _, path := range overridePublic {
		status, err := repository.PathStatus(ctx, path)
		if err != nil {
			return err
		}
		snapshot, err := snapshotFile(path.OSPath(repository.Root))
		if err != nil {
			return fmt.Errorf("snapshot public override path %q: %w", path, err)
		}
		overrideSnapshots[path.String()] = snapshot
		if len(status) == 0 {
			overrideStatuses[path.String()] = nil
			continue
		}
		if !options.DiscardPublicChanges {
			approved, err := a.Prompt.Confirm(
				ctx,
				fmt.Sprintf("Public path %q has staged or unstaged changes. Discard them during ownership override?", path),
				false,
				false,
			)
			if err != nil {
				return err
			}
			if !approved {
				return spaserr.Wrap(spaserr.KindPathConflict, fmt.Errorf("ownership override for %q declined", path))
			}
		}
		overrideStatuses[path.String()] = append([]byte{}, status...)
	}

	skippedPaths := mapPaths(skipped)
	excludeManaged := filterSkipped(candidatePrivate, skipped)
	excludePath, err := repository.InfoExcludePath(ctx)
	if err != nil {
		return err
	}
	excludePlan, err := exclude.Build(excludePath, state.Exclude.BlockID, excludeManaged)
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

	pendingRemoves := make(map[string]linkstate.PendingRemoval, len(state.PendingRemoves))
	for _, removal := range state.PendingRemoves {
		pendingRemoves[removal.Path] = removal
	}
	// Both spellings of every approved override are excluded from private
	// staging: public ownership is being transferred, and the workspace bytes
	// at those paths must never be committed privately.
	overrideSkip := make(map[string]pathmodel.Path, len(overridePublic)+len(caseOverridePrivate))
	for value, path := range overridePublic {
		overrideSkip[value] = path
	}
	for value, path := range caseOverridePrivate {
		overrideSkip[value] = path
	}
	plan, err := planLocalChanges(
		repository.Root,
		private.Path,
		localPrivatePaths,
		pendingAdds,
		stateManaged,
		pendingRemoves,
		skipped,
		overrideSkip,
	)
	if err != nil {
		return err
	}
	changes := plan.Changes
	snapshots := plan.Snapshots
	deferred := stringSet(pathsToStrings(append(append([]pathmodel.Path{}, plan.DeferredAdds...), plan.DeferredRemovals...)))
	for _, path := range candidatePrivate {
		if _, found := snapshots[path.String()]; found {
			continue
		}
		snapshot, err := snapshotFile(path.OSPath(repository.Root))
		if err != nil {
			return err
		}
		snapshots[path.String()] = snapshot
	}
	message, err := a.approveCommit(ctx, changes, options.Message)
	if err != nil {
		return err
	}

	recoveryStore, err := recovery.NewStore(a.Store.DataDir, state.LinkID)
	if err != nil {
		return err
	}
	defer func() {
		if returnErr != nil && recoveryStore.Used() {
			returnErr = fmt.Errorf("%w; recovery copies retained at %s", returnErr, recoveryStore.Root)
		}
	}()

	if err := exclude.Apply(excludePlan); err != nil {
		return err
	}
	if err := a.verifyExclusion(ctx, repository, excludeManaged); err != nil {
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

	headBefore, err := private.Head(ctx)
	if err != nil {
		return err
	}
	rollbackPaths := make([]pathmodel.Path, 0, len(changes))
	for _, change := range changes {
		if change.Status != "D" {
			rollbackPaths = append(rollbackPaths, change.Path)
		}
	}
	rollbackNeeded := len(changes) > 0
	defer func() {
		if !rollbackNeeded {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if rollbackErr := private.RollbackChanges(cleanupCtx, headBefore, rollbackPaths); rollbackErr != nil {
			if returnErr == nil {
				returnErr = rollbackErr
			} else {
				returnErr = fmt.Errorf("%w; additionally failed to restore the private clone: %v", returnErr, rollbackErr)
			}
		}
	}()
	if err := verifyApprovedSnapshots(repository.Root, changes, snapshots); err != nil {
		return err
	}
	addOrModify, removals, err := applyLocalChanges(repository.Root, private.Path, changes)
	if err != nil {
		return err
	}
	if err := private.Stage(ctx, addOrModify); err != nil {
		return err
	}
	if err := private.StageRemovals(ctx, removals); err != nil {
		return err
	}
	if err := private.ValidateIndex(ctx); err != nil {
		return err
	}
	if err := verifyStagedBytes(ctx, private, changes, snapshots); err != nil {
		return err
	}
	if len(changes) > 0 {
		if err := a.ensurePrivateIdentity(ctx, repository, private); err != nil {
			return err
		}
		if err := private.Commit(ctx, message); err != nil {
			return err
		}
	}
	rollbackNeeded = false

	var finalPaths []pathmodel.Path
	materializeSkip := unionSets(skipped, deferred)
	finalPendingAdds := retainStrings(
		state.PendingAdds,
		unionSets(skipped, stringSet(pathsToStrings(plan.DeferredAdds))),
	)
	finalPendingRemoves := retainRemovals(
		state.PendingRemoves,
		unionSets(skipped, stringSet(pathsToStrings(plan.DeferredRemovals))),
	)
	for attempt := 1; ; attempt++ {
		preMergeHead, err := private.Head(ctx)
		if err != nil {
			return err
		}
		if !remoteEmpty {
			expectedMergeHead, err := private.RemoteHead(ctx, state.Private.Branch)
			if err != nil {
				return err
			}
			if expectedMergeHead == "" {
				return fmt.Errorf("private remote branch %q no longer names a commit", state.Private.Branch)
			}
			if _, err := private.MergeRemote(ctx, state.Private.Branch); err != nil {
				mergeInProgress, mergeProbeErr := private.MergeInProgress()
				if mergeProbeErr != nil {
					return errors.Join(
						err,
						spaserr.Wrap(
							spaserr.KindUnsafeGitState,
							fmt.Errorf("could not inspect private merge state; no SPAS merge binding was persisted and automatic abort was not attempted—run spas sync --abort: %w", mergeProbeErr),
						),
					)
				}
				if !mergeInProgress {
					return err
				}
				livePreMergeHead, headErr := private.Head(ctx)
				if headErr != nil {
					return errors.Join(err, spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("read live private merge pre-merge commit; no SPAS merge binding was persisted—run spas sync --abort: %w", headErr)))
				}
				if livePreMergeHead == "" {
					return errors.Join(err, spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("live private merge has an empty pre-merge commit; no SPAS merge binding was persisted—run spas sync --abort")))
				}
				liveMergeHead, mergeHeadErr := private.MergeHead(ctx)
				if mergeHeadErr != nil {
					return errors.Join(err, spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("read live private merge target; no SPAS merge binding was persisted—run spas sync --abort: %w", mergeHeadErr)))
				}
				if liveMergeHead == "" {
					return errors.Join(err, spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("live private merge has an empty merge target; no SPAS merge binding was persisted—run spas sync --abort")))
				}
				if livePreMergeHead != preMergeHead || liveMergeHead != expectedMergeHead {
					return errors.Join(err, spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("live Git merge bindings changed during merge failure; no SPAS recovery mutation was attempted—run spas sync --abort")))
				}
				unmerged, unmergedErr := private.UnmergedPaths(ctx)
				if unmergedErr != nil {
					return errors.Join(err, spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("read live private merge conflicts; no SPAS merge binding was persisted—run spas sync --abort: %w", unmergedErr)))
				}
				if len(unmerged) == 0 {
					return errors.Join(err, spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("Git stopped a private merge without unresolved paths; no SPAS merge binding was persisted—run spas sync --abort")))
				}
				state.ActiveMerge = &linkstate.ActiveMerge{PreMergeHead: livePreMergeHead, MergeHead: liveMergeHead}
				state.Private.ExpectedHead = livePreMergeHead
				if saveErr := a.Store.Save(state); saveErr != nil {
					return errors.Join(err, spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("persist live private merge binding; automatic abort was not attempted—run spas sync --abort: %w", saveErr)))
				}
				for _, path := range unmerged {
					if _, blocked := skipped[path.String()]; blocked {
						primary := fmt.Errorf("private merge conflict at skipped path %q is unsupported; reconcile the private branch before retrying", path)
						return a.abortFailedMerge(ctx, private, &state, primary)
					}
					if _, blocked := overrideSkip[path.String()]; blocked {
						primary := fmt.Errorf("private merge conflict at public-owned override path %q is unsupported; reconcile the private branch before retrying", path)
						return a.abortFailedMerge(ctx, private, &state, primary)
					}
				}
				if err := revalidatePublicOwnership(ctx, repository, unmerged, nil, nil, ignoreCase); err != nil {
					return a.abortFailedMerge(ctx, private, &state, err)
				}
				recoveryPaths := unionPaths(
					mapPathValues(obstructionOverrides),
					mapPathValues(overridePublic),
				)
				activeWorkspaceSnapshots := mergeFileSnapshots(snapshots, overrideSnapshots)
				activeSnapshots, snapshotErr := encodeWorkspaceSnapshots(
					unionPaths(unmerged, recoveryPaths),
					activeWorkspaceSnapshots,
				)
				if snapshotErr != nil {
					return a.abortFailedMerge(ctx, private, &state, snapshotErr)
				}
				activeMaterializationPaths := materializationCandidates(
					candidatePrivate,
					state.ManagedPaths,
					materializeSkip,
				)
				activeMaterializationSnapshots, snapshotErr := encodeWorkspaceSnapshots(
					activeMaterializationPaths,
					snapshots,
				)
				if snapshotErr != nil {
					return a.abortFailedMerge(ctx, private, &state, snapshotErr)
				}
				mergeHead, mergeHeadErr := private.MergeHead(ctx)
				if mergeHeadErr != nil {
					return a.abortFailedMerge(ctx, private, &state, mergeHeadErr)
				}
				if mergeHead != expectedMergeHead {
					return a.abortFailedMerge(
						ctx,
						private,
						&state,
						fmt.Errorf("private merge target changed from %s to %s", expectedMergeHead, mergeHead),
					)
				}
				state.ActiveMerge = &linkstate.ActiveMerge{
					ConflictFilesReady:       false,
					WorkspaceSnapshots:       activeSnapshots,
					MaterializationPaths:     pathsToStrings(activeMaterializationPaths),
					MaterializationSnapshots: activeMaterializationSnapshots,
					OverrideStatuses:         encodeOverrideStatuses(overridePublic, overrideStatuses),
					PreMergeHead:             preMergeHead,
					MergeHead:                mergeHead,
					ConflictPaths:            pathsToStrings(unmerged),
					SkippedPaths:             pathsToStrings(skippedPaths),
					DeferredPaths:            sortedSet(deferred),
					OverridePaths:            pathsToStrings(mapPathValues(overridePublic)),
					RecoveryPaths:            pathsToStrings(recoveryPaths),
					RemainingPendingAdds:     append([]string{}, finalPendingAdds...),
					RemainingPendingRemoves:  append([]linkstate.PendingRemoval{}, finalPendingRemoves...),
				}
				state.Private.ExpectedHead = preMergeHead
				if saveErr := a.Store.Save(state); saveErr != nil {
					return a.abortFailedMerge(ctx, private, &state, saveErr)
				}
				// Preserve every approved obstruction before writing any conflict
				// file. If the process stops before ConflictFilesReady is persisted,
				// continuation is refused and abort remains the only safe recovery.
				if saveErr := saveRecoveryCopies(
					recoveryStore,
					repository.Root,
					recoveryPaths,
					activeWorkspaceSnapshots,
				); saveErr != nil {
					return fmt.Errorf("save approved obstruction before materializing private merge conflict: %w", saveErr)
				}
				if copyErr := copyConflictFiles(private.Path, repository.Root, unmerged, snapshots); copyErr != nil {
					return fmt.Errorf("%w; additionally failed to copy conflict files: %v", err, copyErr)
				}
				state.ActiveMerge.ConflictFilesReady = true
				if saveErr := a.Store.Save(state); saveErr != nil {
					return fmt.Errorf("save completed private conflict materialization state: %w", saveErr)
				}
				return fmt.Errorf("%w: resolve %s in the public workspace, then run spas sync --continue --message <reason>", ErrPrivateMergeConflict, joinPaths(unmerged))
			}
		}

		resultHead, err := private.Head(ctx)
		if err != nil {
			return err
		}
		if resultHead == "" {
			return fmt.Errorf("private repository is empty and no local files were approved for its initial commit")
		}
		if err := private.ValidateTree(ctx, resultHead); err != nil {
			if preMergeHead != "" && resultHead != preMergeHead {
				return errors.Join(err, private.ResetHard(ctx, preMergeHead))
			}
			return err
		}
		finalPaths, err = private.TrackedPaths(ctx)
		if err != nil {
			return err
		}
		if err := revalidatePublicOwnership(ctx, repository, finalPaths, skipped, overridePublic, ignoreCase); err != nil {
			return err
		}
		finalSet := stringSet(pathsToStrings(finalPaths))
		for value := range overridePublic {
			replacement := overrideReplacement[value]
			if replacement == "" {
				replacement = value
			}
			if _, ok := finalSet[replacement]; !ok {
				return spaserr.Wrap(spaserr.KindPathConflict, fmt.Errorf("refusing to remove public %q: the private result contains no replacement", value))
			}
		}
		if err := verifyOwnershipApprovals(ctx, repository, overridePublic, overrideStatuses, overrideSnapshots); err != nil {
			return err
		}
		finalExclude := unionPaths(filterSkipped(finalPaths, skipped), plan.DeferredAdds)
		recoveryPaths := filterRecoveryPaths(
			unionPaths(mapPathValues(obstructionOverrides), mapPathValues(overridePublic)),
			finalPaths,
			overridePublic,
		)
		materialization, err := newMaterialization(
			resultHead,
			state.ManagedPaths,
			finalPaths,
			finalExclude,
			skipped,
			deferred,
			overridePublic,
			recoveryPaths,
			mergeFileSnapshots(snapshots, overrideSnapshots),
			finalPendingAdds,
			finalPendingRemoves,
		)
		if err != nil {
			return err
		}
		state.Materializing = materialization
		if err := a.Store.Save(state); err != nil {
			return fmt.Errorf("save pending private push state: %w", err)
		}
		if err := private.VerifyOrigin(ctx, remoteURL); err != nil {
			return err
		}
		err = private.Push(ctx, state.Private.Branch)
		if err == nil {
			state.Materializing.Phase = linkstate.MaterializationPushed
			if err := a.Store.Save(state); err != nil {
				return fmt.Errorf("save pushed private result state: %w", err)
			}
			break
		}
		if attempt < maxPushAttempts && privategit.IsNonFastForward(err) {
			// The remote advanced between merge and push. Fetch, validate,
			// merge, and push again a bounded number of times.
			state.Materializing = nil
			if saveErr := a.Store.Save(state); saveErr != nil {
				return fmt.Errorf("clear superseded pending push state: %w", saveErr)
			}
			if err := private.Fetch(ctx, state.Private.Branch); err != nil {
				return err
			}
			if err := private.ValidateTree(ctx, "refs/remotes/origin/"+state.Private.Branch); err != nil {
				return err
			}
			remoteEmpty = false
			continue
		}
		return err
	}
	state.Private.RemoteEmpty = false

	// The exclude block is written and proven effective before any private
	// content reaches the public working tree.
	finalExclude := unionPaths(filterSkipped(finalPaths, skipped), plan.DeferredAdds)
	finalExcludePlan, err := exclude.Build(excludePath, state.Exclude.BlockID, finalExclude)
	if err != nil {
		return err
	}
	if err := exclude.Apply(finalExcludePlan); err != nil {
		return err
	}
	if err := a.verifyExclusion(ctx, repository, finalExclude); err != nil {
		return errors.Join(err, exclude.Restore(finalExcludePlan))
	}

	// Recheck immediately before saving or removing public files. The push and
	// exclusion update above may have taken long enough for an editor or another
	// Git process to change the approved public bytes without changing their
	// porcelain status class.
	if err := verifyOwnershipApprovals(ctx, repository, overridePublic, overrideStatuses, overrideSnapshots); err != nil {
		return err
	}
	if err := saveRecoveryCopies(
		recoveryStore,
		repository.Root,
		stringsToPaths(state.Materializing.RecoveryPaths),
		mergeFileSnapshots(snapshots, overrideSnapshots),
	); err != nil {
		return err
	}
	if err := revalidatePublicOwnership(ctx, repository, finalPaths, skipped, overridePublic, ignoreCase); err != nil {
		return err
	}
	trackedOverrides, err := currentlyTrackedOverrides(ctx, repository, overridePublic)
	if err != nil {
		return err
	}
	if len(trackedOverrides) > 0 {
		if err := repository.RemoveTracked(ctx, mapPathValues(trackedOverrides)); err != nil {
			return err
		}
	}

	materializationSnapshots := snapshotsForOverrideReplacements(
		mergeFileSnapshots(snapshots, overrideSnapshots),
		overrideSnapshots,
		overrideReplacement,
	)
	if err := materializeFinal(repository.Root, private.Path, finalPaths, state.ManagedPaths, materializeSkip, overrideReplacement, materializationSnapshots, ignoreCase); err != nil {
		return err
	}
	// Re-verify after materialization: the materialized tree itself must not
	// have introduced anything that defeats the exclusion of a managed path.
	if err := a.verifyExclusion(ctx, repository, finalExclude); err != nil {
		return err
	}

	publicHead, err := repository.Head(ctx)
	if err != nil {
		return err
	}
	privateHead, err := private.Head(ctx)
	if err != nil {
		return err
	}
	remoteHead, err := private.RemoteHead(ctx, state.Private.Branch)
	if err != nil {
		return err
	}
	materializedPaths := filterSkipped(finalPaths, skipped)
	state.ManagedPaths = pathsToStrings(materializedPaths)
	state.PendingAdds = finalPendingAdds
	state.PendingRemoves = finalPendingRemoves
	state.ActiveMerge = nil
	state.Materializing = nil
	state.Private.ExpectedHead = privateHead
	state.LastSync = linkstate.LastSync{
		PublicHead:  publicHead,
		PrivateHead: privateHead,
		RemoteHead:  remoteHead,
		CompletedAt: time.Now().UTC(),
	}
	if err := a.Store.Save(state); err != nil {
		return err
	}

	summary := map[string]any{
		"synchronized":         true,
		"privateCommitCreated": len(changes) > 0,
		"managedFiles":         len(materializedPaths),
		"skippedConflicts":     pathsToStrings(skippedPaths),
		"publicRemovalsStaged": pathsToStrings(mapPathValues(trackedOverrides)),
	}
	if len(plan.DeferredAdds) > 0 {
		summary["deferredAdditions"] = pathsToStrings(plan.DeferredAdds)
	}
	if len(plan.DeferredRemovals) > 0 {
		summary["deferredRemovals"] = pathsToStrings(plan.DeferredRemovals)
	}
	if recoveryStore.Used() {
		summary["recoveryCopies"] = recoveryStore.Root
	}
	return a.write(summary)
}

func (a App) initializePrivateRepository(
	ctx context.Context,
	state *linkstate.State,
	private privategit.Repository,
	remoteURL string,
	requestedBranch string,
) error {
	if requestedBranch != "" && state.Private.Branch != "" && requestedBranch != state.Private.Branch {
		return fmt.Errorf("private branch is already fixed to %q", state.Private.Branch)
	}

	finalExists, err := pathExists(private.Path)
	if err != nil {
		return fmt.Errorf("inspect private clone destination: %w", err)
	}
	stagingExists, err := pathExists(private.StagingPath())
	if err != nil {
		return fmt.Errorf("inspect private clone staging destination: %w", err)
	}
	initialization := state.Private.Initialization
	if initialization == nil {
		if finalExists || stagingExists {
			return spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("private clone storage exists without a journaled initialization binding; inspect %s and %s, then unlink and recreate the link", private.Path, private.StagingPath()),
			)
		}
		if requestedBranch == "" {
			requestedBranch = state.Private.Branch
		}
		state.Private.Initialization = &linkstate.CloneInitialization{
			Phase:           linkstate.ClonePreparing,
			RequestedBranch: requestedBranch,
		}
		if err := a.Store.Save(*state); err != nil {
			return fmt.Errorf("save private clone preparation state: %w", err)
		}
		initialization = state.Private.Initialization
	}
	if initialization.RequestedBranch != "" && requestedBranch != "" && initialization.RequestedBranch != requestedBranch {
		return fmt.Errorf("private clone initialization is bound to branch %q", initialization.RequestedBranch)
	}
	if initialization.Phase == linkstate.ClonePrepared && requestedBranch != "" && initialization.Branch != requestedBranch {
		return fmt.Errorf("prepared private clone initialization is bound to branch %q", initialization.Branch)
	}
	if initialization.Phase == linkstate.ClonePreparing && initialization.RequestedBranch == "" && requestedBranch != "" {
		initialization.RequestedBranch = requestedBranch
		state.Private.Initialization = initialization
		if err := a.Store.Save(*state); err != nil {
			return fmt.Errorf("save requested private clone branch: %w", err)
		}
	}

	finalize := func(result privategit.InitResult) error {
		state.Private.Initialized = true
		state.Private.Branch = result.Branch
		state.Private.RemoteEmpty = result.Empty
		state.Private.ExpectedHead = result.Head
		state.Private.Initialization = nil
		if err := a.Store.Save(*state); err != nil {
			return fmt.Errorf("save initialized private repository state: %w", err)
		}
		return nil
	}

	switch initialization.Phase {
	case linkstate.ClonePreparing:
		if finalExists {
			return spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("private clone initialization is preparing but the final clone already exists at %s; SPAS will not adopt it", private.Path),
			)
		}
		if stagingExists {
			if err := private.RemoveStagingClone(); err != nil {
				return err
			}
		}
		result, err := private.PrepareClone(ctx, remoteURL, initialization.RequestedBranch)
		if err != nil {
			return err
		}
		state.Private.Initialization = &linkstate.CloneInitialization{
			Phase:           linkstate.ClonePrepared,
			RequestedBranch: initialization.RequestedBranch,
			Branch:          result.Branch,
			Head:            result.Head,
			RemoteEmpty:     result.Empty,
		}
		if err := a.Store.Save(*state); err != nil {
			return fmt.Errorf("save prepared private clone state: %w", err)
		}
		staging := privategit.Repository{Path: private.StagingPath(), Git: private.Git, SafetyDir: private.SafetyDir}
		if err := staging.VerifyPublishedClone(ctx, remoteURL, result); err != nil {
			return fmt.Errorf("verify prepared private clone: %w", err)
		}
		if err := private.PublishPreparedClone(ctx, result); err != nil {
			return err
		}
		if err := private.VerifyPublishedClone(ctx, remoteURL, result); err != nil {
			return fmt.Errorf("verify published private clone: %w", err)
		}
		return finalize(result)

	case linkstate.ClonePrepared:
		result := privategit.InitResult{
			Branch: initialization.Branch,
			Empty:  initialization.RemoteEmpty,
			Head:   initialization.Head,
		}
		if finalExists == stagingExists {
			return spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("prepared private clone initialization requires exactly one of %s and %s; inspect them and unlink and recreate the link", private.Path, private.StagingPath()),
			)
		}
		if stagingExists {
			staging := privategit.Repository{Path: private.StagingPath(), Git: private.Git, SafetyDir: private.SafetyDir}
			if err := staging.VerifyPublishedClone(ctx, remoteURL, result); err != nil {
				return fmt.Errorf("verify prepared private clone: %w", err)
			}
			if err := private.PublishPreparedClone(ctx, result); err != nil {
				return err
			}
		}
		if err := private.VerifyPublishedClone(ctx, remoteURL, result); err != nil {
			return fmt.Errorf("verify published private clone: %w", err)
		}
		return finalize(result)
	default:
		return fmt.Errorf("invalid private clone initialization phase %q", initialization.Phase)
	}
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}

func privateHeadMatches(state linkstate.State, actual string) bool {
	return actual == state.Private.ExpectedHead && (actual != "" || state.Private.RemoteEmpty)
}

func verifyExpectedPrivateHead(
	ctx context.Context,
	state linkstate.State,
	private privategit.Repository,
) error {
	actual, err := private.Head(ctx)
	if err != nil {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("read actual private HEAD while checking its binding: %w", err))
	}
	if privateHeadMatches(state, actual) {
		return nil
	}
	return spaserr.Wrap(
		spaserr.KindUnsafeGitState,
		fmt.Errorf(
			"managed private HEAD mismatch: expected commit %q, actual commit %q; SPAS will not push or reset the clone automatically; inspect or reset the managed clone at %s, or unlink and recreate the link",
			state.Private.ExpectedHead,
			actual,
			private.Path,
		),
	)
}

func (a App) syncDryRun(
	ctx context.Context,
	repository publicgit.Repository,
	state linkstate.State,
	options SyncOptions,
) error {
	if !state.Private.Initialized {
		return a.write(map[string]any{
			"action":             "sync",
			"networkRequired":    true,
			"privateInitialized": false,
			"pendingAdds":        state.PendingAdds,
			"pendingRemovals":    state.PendingRemovalPaths(),
		})
	}
	private := a.privateRepository(state)
	readOnlyGit := repository.Git
	readOnlyGit.NoOptionalLocks = true
	repository.Git = readOnlyGit
	private.Git.NoOptionalLocks = true
	if err := private.VerifyOrigin(ctx, state.Private.RemoteURL); err != nil {
		return err
	}
	unmerged, err := repository.UnmergedPaths(ctx)
	if err != nil {
		return err
	}
	if len(unmerged) > 0 {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("public repository has unresolved merge conflicts"))
	}
	mergeInProgress, err := inspectNormalMergeState(ctx, state, private)
	if err != nil {
		return err
	}
	head, err := private.Head(ctx)
	if err != nil {
		return err
	}
	if state.Materializing == nil {
		if err := verifyExpectedPrivateHead(ctx, state, private); err != nil {
			return err
		}
	}
	clean, err := private.IsClean(ctx)
	if err != nil {
		return err
	}
	branch, err := private.Branch(ctx)
	if err != nil {
		return err
	}
	if branch != state.Private.Branch {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("private clone is on branch %q, expected %q", branch, state.Private.Branch))
	}
	if head != "" {
		if err := private.ValidateTree(ctx, head); err != nil {
			return err
		}
	}
	ignoreCase, present, err := repository.EffectiveIgnoreCase(ctx)
	if err != nil {
		return err
	}
	if !present {
		ignoreCase = true
	}
	localPrivatePaths, err := private.TrackedPaths(ctx)
	if err != nil {
		return err
	}
	publicPaths, err := repository.TrackedPaths(ctx)
	if err != nil {
		return err
	}
	candidatePrivate := unionPaths(localPrivatePaths, stringsToPaths(state.PendingAdds))
	conflicts := collision.Detect(publicPaths, candidatePrivate, ignoreCase)
	pendingRemoves := make(map[string]linkstate.PendingRemoval, len(state.PendingRemoves))
	for _, removal := range state.PendingRemoves {
		pendingRemoves[removal.Path] = removal
	}
	plan, err := planLocalChanges(
		repository.Root,
		private.Path,
		localPrivatePaths,
		stringsToPaths(state.PendingAdds),
		stringSet(state.ManagedPaths),
		pendingRemoves,
		nil,
		nil,
	)
	if err != nil {
		return err
	}
	excludePath, err := repository.InfoExcludePath(ctx)
	if err != nil {
		return err
	}
	excludePlan, err := exclude.Build(excludePath, state.Exclude.BlockID, candidatePrivate)
	if err != nil {
		return err
	}
	mergeStatus, err := mergeprotect.Inspect(ctx, repository)
	if err != nil {
		return err
	}
	return a.write(map[string]any{
		"action":                 "sync",
		"networkRequired":        false,
		"privateInitialized":     true,
		"privateHead":            head,
		"expectedPrivateHead":    state.Private.ExpectedHead,
		"privateClean":           clean,
		"privateMergeInProgress": mergeInProgress,
		"pendingRecovery":        state.Materializing != nil || state.ActiveMerge != nil,
		"localChanges":           plan.Changes,
		"commitApprovalRequired": len(plan.Changes) > 0,
		"commitMessageProvided":  strings.TrimSpace(options.Message) != "",
		"conflicts":              conflicts,
		"pendingAdds":            state.PendingAdds,
		"pendingRemovals":        state.PendingRemovalPaths(),
		"localExcludeWillChange": excludePlan.Changed,
		"mergeProtection":        mergeStatus,
	})
}

func inspectNormalMergeState(
	ctx context.Context,
	state linkstate.State,
	private privategit.Repository,
) (bool, error) {
	mergeInProgress, err := private.MergeInProgress()
	if err != nil {
		return false, spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("inspect private merge state: %w", err))
	}
	if mergeInProgress {
		if state.ActiveMerge == nil {
			return true, spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("Git has a private merge in progress without SPAS recovery state; run spas sync --abort"))
		}
		head, err := private.Head(ctx)
		if err != nil {
			return true, err
		}
		mergeHead, err := private.MergeHead(ctx)
		if err != nil {
			return true, err
		}
		if head != state.ActiveMerge.PreMergeHead || mergeHead != state.ActiveMerge.MergeHead {
			return true, spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("Git merge metadata does not match the recorded SPAS merge binding; no recovery mutation was attempted"))
		}
		return true, nil
	}
	if state.ActiveMerge == nil {
		return false, nil
	}
	if len(state.ActiveMerge.ConflictPaths) > 0 {
		return false, spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("private merge recovery state exists after Git left the merge; run spas sync --abort to restore the recorded workspace result"))
	}
	head, err := private.Head(ctx)
	if err != nil {
		return false, err
	}
	terminal := head == state.ActiveMerge.PreMergeHead
	if !terminal {
		terminal, err = private.IsMergeCommitOf(ctx, head, state.ActiveMerge.PreMergeHead, state.ActiveMerge.MergeHead)
		if err != nil {
			return false, err
		}
	}
	if !terminal {
		return false, spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("stale SPAS merge state does not match an aborted or completed Git merge; no recovery mutation was attempted"))
	}
	return false, nil
}

// resumeMaterialization finishes an interrupted merge-result push or workspace
// materialization before the workspace is interpreted as local edits.
func (a App) resumeMaterialization(
	ctx context.Context,
	repository publicgit.Repository,
	state *linkstate.State,
	private privategit.Repository,
	ignoreCase bool,
) error {
	marker := state.Materializing
	if marker == nil {
		return nil
	}
	if !state.Private.Initialized {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("pending private result has no initialized private clone"))
	}
	mergeInProgress, mergeProbeErr := private.MergeInProgress()
	if mergeProbeErr != nil {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("inspect private merge state during materialization recovery: %w", mergeProbeErr))
	}
	if (marker.Phase == linkstate.MaterializationMergeContinuing ||
		marker.Phase == linkstate.MaterializationMergeStaged) && mergeInProgress {
		return spaserr.Wrap(
			spaserr.KindUnsafeGitState,
			fmt.Errorf("private merge continuation was interrupted before its commit completed; run spas sync --continue --message <reason>"),
		)
	}
	if mergeInProgress {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("pending private result cannot be recovered while a private merge is in progress"))
	}
	clean, err := private.IsClean(ctx)
	if err != nil {
		return err
	}
	if !clean {
		return spaserr.Wrap(
			spaserr.KindUnsafeGitState,
			fmt.Errorf("SPAS-managed private clone changed while materialization recovery was pending; restore it to the recorded private result or relink before retrying"),
		)
	}
	head, err := private.Head(ctx)
	if err != nil {
		return err
	}
	if marker.Phase == linkstate.MaterializationMergeContinuing || marker.Phase == linkstate.MaterializationMergeStaged {
		if head == marker.ResultPrivateHead {
			return spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("private merge continuation ended without creating its expected result commit"),
			)
		}
		validMergeResult, err := private.IsMergeResultOf(
			ctx,
			head,
			marker.ResultPrivateHead,
			marker.MergeHead,
			marker.StagedTree,
		)
		if err != nil {
			return err
		}
		if !validMergeResult {
			return spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("private clone moved to %s, which is not the expected merge result of %s", head, marker.ResultPrivateHead),
			)
		}
		marker.ResultPrivateHead = head
		marker.Phase = linkstate.MaterializationPushPending
		if err := a.Store.Save(*state); err != nil {
			return fmt.Errorf("save recovered private merge result: %w", err)
		}
	}
	if head != marker.ResultPrivateHead {
		return spaserr.Wrap(
			spaserr.KindUnsafeGitState,
			fmt.Errorf("private clone moved from pending result %s to %s; SPAS will not guess which result to materialize", marker.ResultPrivateHead, head),
		)
	}
	if err := private.ValidateTree(ctx, head); err != nil {
		return fmt.Errorf("validate pending private result: %w", err)
	}
	if marker.Phase == linkstate.MaterializationPushPending {
		if err := private.Push(ctx, state.Private.Branch); err != nil {
			if privategit.IsNonFastForward(err) {
				marker.Phase = linkstate.MaterializationRemoteAdvanced
				if saveErr := a.Store.Save(*state); saveErr != nil {
					return errors.Join(
						fmt.Errorf("resume pending private push: %w", err),
						fmt.Errorf("save remote-advanced recovery state: %w", saveErr),
					)
				}
			} else {
				return fmt.Errorf("resume pending private push: %w", err)
			}
		} else {
			marker.Phase = linkstate.MaterializationPushed
			if err := a.Store.Save(*state); err != nil {
				return fmt.Errorf("save pushed private result state: %w", err)
			}
		}
	}
	remoteAdvanced := marker.Phase == linkstate.MaterializationRemoteAdvanced
	if marker.Phase != linkstate.MaterializationPushed && !remoteAdvanced {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("unsupported pending materialization phase %q", marker.Phase))
	}

	finalPaths := stringsToPaths(marker.FinalPaths)
	skipped := stringSet(marker.SkippedPaths)
	deferred := stringSet(marker.DeferredPaths)
	materializeSkip := unionSets(skipped, deferred)
	workspaceSnapshots, err := decodeWorkspaceSnapshots(marker.WorkspaceSnapshots)
	if err != nil {
		return err
	}
	overrides := stringSet(marker.OverridePaths)
	_, _, overrideReplacement, err := resolveStoredOverrides(marker.OverridePaths, finalPaths)
	if err != nil {
		return err
	}
	publicPaths, err := repository.TrackedPaths(ctx)
	if err != nil {
		return err
	}
	for _, conflict := range collision.Detect(publicPaths, finalPaths, ignoreCase) {
		if _, blocked := skipped[conflict.Private.String()]; blocked {
			continue
		}
		if _, expected := overrides[conflict.Public.String()]; expected {
			return spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("interrupted ownership transfer for public path %q requires manual public untracking before sync can resume", conflict.Public),
			)
		}
		return spaserr.Wrap(
			spaserr.KindPathConflict,
			fmt.Errorf("public ownership changed while recovering synchronization: %s", conflict.Error()),
		)
	}

	excludePath, err := repository.InfoExcludePath(ctx)
	if err != nil {
		return err
	}
	recoveryExclude := unionPaths(
		stringsToPaths(marker.PreviousPaths),
		stringsToPaths(marker.ExcludedPaths),
	)
	recoveryPlan, err := exclude.Build(excludePath, state.Exclude.BlockID, recoveryExclude)
	if err != nil {
		return err
	}
	if err := exclude.Apply(recoveryPlan); err != nil {
		return err
	}
	if err := a.verifyExclusion(ctx, repository, recoveryExclude); err != nil {
		return errors.Join(err, exclude.Restore(recoveryPlan))
	}

	// A pending push may run after the initial clean-clone check. Bind the
	// worktree source again immediately before any recovery path
	// is compared with or copied from the private clone.
	clean, err = private.IsClean(ctx)
	if err != nil {
		return err
	}
	if !clean {
		return spaserr.Wrap(
			spaserr.KindUnsafeGitState,
			fmt.Errorf("SPAS-managed private clone changed while materialization recovery was pending; restore it to the recorded private result or relink before retrying"),
		)
	}

	finalSet := stringSet(marker.FinalPaths)
	recoveryPaths := stringsToPaths(marker.RecoveryPaths)
	recoverySet := stringSet(marker.RecoveryPaths)
	for _, recoveryPath := range recoveryPaths {
		value := recoveryPath.String()
		replacementValue := value
		if override, found := overrideReplacement[value]; found {
			replacementValue = override
		}
		replacement, err := pathmodel.Parse(replacementValue)
		if err != nil {
			return err
		}
		workspacePath := recoveryPath.OSPath(repository.Root)
		if _, err := os.Lstat(workspacePath); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		equal, err := filesync.Equal(workspacePath, replacement.OSPath(private.Path))
		if err != nil {
			return spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("inspect interrupted recovery path %q: %w", recoveryPath, err),
			)
		}
		if equal {
			if replacementValue != value {
				current, err := snapshotFile(workspacePath)
				if err != nil {
					return err
				}
				if err := filesync.RemoveManagedIfUnchanged(
					repository.Root,
					recoveryPath,
					expectedFilesyncSnapshot(current),
				); err != nil {
					return err
				}
			}
			continue
		}
		snapshot, found := workspaceSnapshots[value]
		if !found {
			return fmt.Errorf("workspace snapshot for interrupted recovery path %q is missing", recoveryPath)
		}
		if err := verifyFileSnapshot(workspacePath, snapshot); err != nil {
			return spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("interrupted recovery path %q changed after synchronization planning: %w", recoveryPath, err),
			)
		}
		return spaserr.Wrap(
			spaserr.KindUnsafeGitState,
			fmt.Errorf("interrupted recovery path %q still differs from the pushed private replacement; move or remove it before sync can resume", recoveryPath),
		)
	}
	caseRenameOld, caseRenameTargets, err := caseRenamePreRemovals(finalPaths, marker.PreviousPaths, materializeSkip, ignoreCase)
	if err != nil {
		return err
	}
	for publicValue, privateValue := range overrideReplacement {
		if publicValue == privateValue {
			continue
		}
		privatePath, err := pathmodel.Parse(privateValue)
		if err != nil {
			return err
		}
		caseRenameTargets[pathmodel.Canonical(privatePath, ignoreCase)] = struct{}{}
	}
	caseRenameSet := stringSet(pathsToStrings(caseRenameOld))
	finalByCanonical := make(map[string]pathmodel.Path, len(finalPaths))
	for _, path := range finalPaths {
		if _, blocked := materializeSkip[path.String()]; blocked {
			continue
		}
		finalByCanonical[pathmodel.Canonical(path, ignoreCase)] = path
	}
	for _, value := range marker.PreviousPaths {
		if _, remains := finalSet[value]; remains {
			continue
		}
		path, err := pathmodel.Parse(value)
		if err != nil {
			return err
		}
		workspacePath := path.OSPath(repository.Root)
		if _, err := os.Lstat(workspacePath); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if _, caseRename := caseRenameSet[value]; caseRename {
			finalPath := finalByCanonical[pathmodel.Canonical(path, ignoreCase)]
			equal, compareErr := filesync.Equal(workspacePath, finalPath.OSPath(private.Path))
			if compareErr != nil {
				return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("inspect interrupted case-only rename for %q: %w", path, compareErr))
			}
			if equal {
				current, err := snapshotFile(workspacePath)
				if err != nil {
					return err
				}
				if err := filesync.RemoveManagedIfUnchanged(
					repository.Root,
					path,
					expectedFilesyncSnapshot(current),
				); err != nil {
					return err
				}
				continue
			}
		}
		return spaserr.Wrap(
			spaserr.KindUnsafeGitState,
			fmt.Errorf("interrupted sync removed private path %q; remove or move the stale workspace copy, then run sync again", path),
		)
	}
	for _, path := range finalPaths {
		if _, blocked := materializeSkip[path.String()]; blocked {
			continue
		}
		workspacePath := path.OSPath(repository.Root)
		if _, err := os.Lstat(workspacePath); errors.Is(err, os.ErrNotExist) {
			snapshot, found := workspaceSnapshots[path.String()]
			if !found {
				return fmt.Errorf("workspace snapshot for interrupted materialization path %q is missing", path)
			}
			_, approvedRecoveryRemoval := recoverySet[path.String()]
			_, approvedCaseRenameRemoval := caseRenameTargets[pathmodel.Canonical(path, ignoreCase)]
			if snapshot.Existed && !approvedRecoveryRemoval && !approvedCaseRenameRemoval {
				return spaserr.Wrap(
					spaserr.KindUnsafeGitState,
					fmt.Errorf("workspace path %q was removed after synchronization planning; restore it or remove the pending recovery state manually", path),
				)
			}
		} else if err != nil {
			return err
		} else {
			equal, err := filesync.Equal(workspacePath, path.OSPath(private.Path))
			if err != nil {
				return spaserr.Wrap(
					spaserr.KindUnsafeGitState,
					fmt.Errorf("inspect interrupted materialization path %q: %w", path, err),
				)
			}
			if equal {
				continue
			}
			snapshot, found := workspaceSnapshots[path.String()]
			if !found {
				return fmt.Errorf("workspace snapshot for interrupted materialization path %q is missing", path)
			}
			if err := verifyFileSnapshot(workspacePath, snapshot); err != nil {
				return spaserr.Wrap(
					spaserr.KindUnsafeGitState,
					fmt.Errorf("workspace path %q changed after synchronization planning: %w", path, err),
				)
			}
		}
		current, err := snapshotFile(workspacePath)
		if err != nil {
			return err
		}
		if err := filesync.CopyManagedIfUnchanged(
			private.Path,
			path,
			repository.Root,
			path,
			expectedFilesyncSnapshot(current),
		); err != nil {
			return err
		}
	}

	finalExclude := stringsToPaths(marker.ExcludedPaths)
	finalPlan, err := exclude.Build(excludePath, state.Exclude.BlockID, finalExclude)
	if err != nil {
		return err
	}
	if err := exclude.Apply(finalPlan); err != nil {
		return err
	}
	if err := a.verifyExclusion(ctx, repository, finalExclude); err != nil {
		return errors.Join(err, exclude.Restore(finalPlan))
	}

	remoteHead, err := private.RemoteHead(ctx, state.Private.Branch)
	if err != nil {
		return err
	}
	publicHead, err := repository.Head(ctx)
	if err != nil {
		return err
	}
	state.ManagedPaths = pathsToStrings(filterSkipped(finalPaths, skipped))
	state.PendingAdds = append([]string{}, marker.RemainingPendingAdds...)
	state.PendingRemoves = append([]linkstate.PendingRemoval{}, marker.RemainingPendingRemoves...)
	state.ActiveMerge = nil
	state.Materializing = nil
	state.Private.RemoteEmpty = false
	state.Private.ExpectedHead = head
	state.LastSync.PrivateHead = head
	state.LastSync.RemoteHead = remoteHead
	state.LastSync.PublicHead = publicHead
	state.LastSync.CompletedAt = time.Now().UTC()
	if err := a.Store.Save(*state); err != nil {
		return err
	}
	if remoteAdvanced {
		return spaserr.Wrap(
			spaserr.KindUnsafeGitState,
			fmt.Errorf("the private remote advanced while a push was pending; the pending result was restored locally, so run sync again to fetch and merge"),
		)
	}
	return nil
}

func (a App) reconcileNormalMergeState(
	ctx context.Context,
	state *linkstate.State,
	private privategit.Repository,
) (bool, error) {
	mergeInProgress, err := private.MergeInProgress()
	if err != nil {
		return false, spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("inspect private merge state: %w", err))
	}
	if mergeInProgress {
		if state.ActiveMerge == nil {
			return true, spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("Git has a private merge in progress without SPAS recovery state; run spas sync --abort"),
			)
		}
		head, err := private.Head(ctx)
		if err != nil {
			return true, err
		}
		mergeHead, err := private.MergeHead(ctx)
		if err != nil {
			return true, err
		}
		if head != state.ActiveMerge.PreMergeHead || mergeHead != state.ActiveMerge.MergeHead {
			return true, spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("Git merge metadata does not match the recorded SPAS merge binding; no recovery mutation was attempted"),
			)
		}
		return true, nil
	}
	if state.ActiveMerge == nil {
		return false, nil
	}
	if len(state.ActiveMerge.ConflictPaths) > 0 {
		return false, spaserr.Wrap(
			spaserr.KindUnsafeGitState,
			fmt.Errorf("private merge recovery state exists after Git left the merge; run spas sync --abort to restore the recorded workspace result"),
		)
	}
	head, err := private.Head(ctx)
	if err != nil {
		return false, err
	}
	terminal := head == state.ActiveMerge.PreMergeHead
	if !terminal {
		terminal, err = private.IsMergeCommitOf(
			ctx,
			head,
			state.ActiveMerge.PreMergeHead,
			state.ActiveMerge.MergeHead,
		)
		if err != nil {
			return false, err
		}
	}
	if !terminal {
		return false, spaserr.Wrap(
			spaserr.KindUnsafeGitState,
			fmt.Errorf("stale SPAS merge state does not match an aborted or completed Git merge; no recovery mutation was attempted"),
		)
	}
	state.ActiveMerge = nil
	if err := a.Store.Save(*state); err != nil {
		return false, fmt.Errorf("clear terminal private merge recovery state: %w", err)
	}
	return false, nil
}

func newMaterialization(
	resultHead string,
	previous []string,
	finalPaths []pathmodel.Path,
	excluded []pathmodel.Path,
	skipped map[string]struct{},
	deferred map[string]struct{},
	overrides map[string]pathmodel.Path,
	recoveryPaths []pathmodel.Path,
	snapshots map[string]fileSnapshot,
	pendingAdds []string,
	pendingRemoves []linkstate.PendingRemoval,
) (*linkstate.Materialization, error) {
	candidates := unionPaths(
		materializationCandidates(finalPaths, previous, unionSets(skipped, deferred)),
		recoveryPaths,
	)
	persistedSnapshots, err := encodeWorkspaceSnapshots(candidates, snapshots)
	if err != nil {
		return nil, err
	}
	return &linkstate.Materialization{
		Phase:                   linkstate.MaterializationPushPending,
		ResultPrivateHead:       resultHead,
		PreviousPaths:           append([]string{}, previous...),
		FinalPaths:              pathsToStrings(finalPaths),
		ExcludedPaths:           pathsToStrings(excluded),
		SkippedPaths:            sortedSet(skipped),
		DeferredPaths:           sortedSet(deferred),
		OverridePaths:           pathsToStrings(mapPathValues(overrides)),
		RecoveryPaths:           pathsToStrings(recoveryPaths),
		WorkspaceSnapshots:      persistedSnapshots,
		RemainingPendingAdds:    append([]string{}, pendingAdds...),
		RemainingPendingRemoves: append([]linkstate.PendingRemoval{}, pendingRemoves...),
	}, nil
}

func mergeFileSnapshots(primary, additional map[string]fileSnapshot) map[string]fileSnapshot {
	merged := make(map[string]fileSnapshot, len(primary)+len(additional))
	for path, snapshot := range primary {
		merged[path] = snapshot
	}
	for path, snapshot := range additional {
		merged[path] = snapshot
	}
	return merged
}

func snapshotsForOverrideReplacements(
	base map[string]fileSnapshot,
	overrideSnapshots map[string]fileSnapshot,
	replacements map[string]string,
) map[string]fileSnapshot {
	result := mergeFileSnapshots(base, nil)
	for publicPath, privatePath := range replacements {
		if snapshot, found := overrideSnapshots[publicPath]; found {
			result[privatePath] = snapshot
		}
	}
	return result
}

func expectedFilesyncSnapshot(snapshot fileSnapshot) filesync.ExpectedSnapshot {
	return filesync.ExpectedSnapshot{
		Digest:     snapshot.Digest,
		Existed:    snapshot.Existed,
		Executable: snapshot.Executable,
	}
}

func filterRecoveryPaths(
	candidates, finalPaths []pathmodel.Path,
	overrides map[string]pathmodel.Path,
) []pathmodel.Path {
	final := stringSet(pathsToStrings(finalPaths))
	filtered := make([]pathmodel.Path, 0, len(candidates))
	for _, path := range candidates {
		if _, remains := final[path.String()]; remains {
			filtered = append(filtered, path)
			continue
		}
		if _, ownershipOverride := overrides[path.String()]; ownershipOverride {
			filtered = append(filtered, path)
		}
	}
	return filtered
}

func encodeWorkspaceSnapshots(
	paths []pathmodel.Path,
	snapshots map[string]fileSnapshot,
) ([]linkstate.WorkspaceSnapshot, error) {
	persistedSnapshots := make([]linkstate.WorkspaceSnapshot, 0, len(paths))
	for _, path := range paths {
		snapshot, found := snapshots[path.String()]
		if !found {
			return nil, fmt.Errorf("workspace snapshot for recovery path %q is missing", path)
		}
		persisted := linkstate.WorkspaceSnapshot{
			Path:       path.String(),
			Existed:    snapshot.Existed,
			Executable: snapshot.Executable,
		}
		if snapshot.Existed {
			persisted.Digest = hex.EncodeToString(snapshot.Digest[:])
		}
		persistedSnapshots = append(persistedSnapshots, persisted)
	}
	return persistedSnapshots, nil
}

// verifyActiveMergeRecoveryPaths protects approved, non-conflicting untracked
// obstructions while a developer resolves another private merge conflict.
// Public tracked overrides are re-approved from their persisted byte/mode
// fingerprint and current Git status later in continuation. Conflict paths are
// intentionally editable. A user may remove an obstruction to make room for
// the private replacement, but changing it in place requires preserving it
// elsewhere before continuation.
func verifyActiveMergeRecoveryPaths(
	publicRoot string,
	active *linkstate.ActiveMerge,
	snapshots map[string]fileSnapshot,
	ignoreCase bool,
) error {
	conflicts := stringSet(active.ConflictPaths)
	overrides := stringSet(active.OverridePaths)
	overrideCanonical := make(map[string]struct{}, len(overrides))
	if ignoreCase {
		for value := range overrides {
			path, err := pathmodel.Parse(value)
			if err != nil {
				return err
			}
			overrideCanonical[pathmodel.Canonical(path, true)] = struct{}{}
		}
	}
	for _, value := range active.RecoveryPaths {
		if _, conflict := conflicts[value]; conflict {
			continue
		}
		if _, publicOverride := overrides[value]; publicOverride {
			continue
		}
		if ignoreCase {
			path, err := pathmodel.Parse(value)
			if err != nil {
				return err
			}
			if _, publicOverride := overrideCanonical[pathmodel.Canonical(path, true)]; publicOverride {
				continue
			}
		}
		snapshot, found := snapshots[value]
		if !found {
			return fmt.Errorf("workspace snapshot for active merge recovery path %q is missing", value)
		}
		path, err := pathmodel.Parse(value)
		if err != nil {
			return err
		}
		workspacePath := path.OSPath(publicRoot)
		if _, err := os.Lstat(workspacePath); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := verifyFileSnapshot(workspacePath, snapshot); err != nil {
			return spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("approved recovery path %q changed while the private merge was being resolved: %w; move or remove it before running sync --continue", path, err),
			)
		}
	}
	return nil
}

// verifyActiveMergeMaterializationPaths protects every non-conflict workspace
// destination that continuation or abort may replace. These snapshots were
// captured before the private merge began; a later fresh materialization
// snapshot is only a race guard and must never become replacement approval.
func verifyActiveMergeMaterializationPaths(
	publicRoot string,
	active *linkstate.ActiveMerge,
	paths []pathmodel.Path,
	ignoreCase bool,
) error {
	snapshots, editable, err := activeMergeMaterializationContract(active, ignoreCase)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if _, skip := editable[pathmodel.Canonical(path, ignoreCase)]; skip {
			continue
		}
		snapshot, found := snapshots[path.String()]
		if !found {
			return spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("private merge materialization path %q was not recorded when conflict resolution began; run spas sync --abort", path),
			)
		}
		if err := verifyFileSnapshot(path.OSPath(publicRoot), snapshot); err != nil {
			return spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("workspace path %q changed while the private merge was being resolved: %w; preserve the current file elsewhere and restore its conflict-start state before retrying", path, err),
			)
		}
	}
	return nil
}

// verifyActiveMergeMaterializationSnapshots closes the interval between the
// conflict-start verification above and capture of the short-lived write
// guard. A change that lands in that interval must not become a new approval
// baseline merely because it was present when snapshotPaths ran.
func verifyActiveMergeMaterializationSnapshots(
	active *linkstate.ActiveMerge,
	paths []pathmodel.Path,
	current map[string]fileSnapshot,
	ignoreCase bool,
) error {
	expected, editable, err := activeMergeMaterializationContract(active, ignoreCase)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if _, skip := editable[pathmodel.Canonical(path, ignoreCase)]; skip {
			continue
		}
		approved, found := expected[path.String()]
		if !found {
			return spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("private merge materialization path %q was not recorded when conflict resolution began; run spas sync --abort", path),
			)
		}
		observed, found := current[path.String()]
		if !found {
			return fmt.Errorf("fresh workspace snapshot for active merge materialization path %q is missing", path)
		}
		if observed != approved {
			return spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("workspace path %q changed before the final merge materialization safety snapshot; preserve the current file elsewhere and restore its conflict-start state before retrying", path),
			)
		}
	}
	return nil
}

func activeMergeMaterializationContract(
	active *linkstate.ActiveMerge,
	ignoreCase bool,
) (map[string]fileSnapshot, map[string]struct{}, error) {
	snapshots, err := decodeWorkspaceSnapshots(active.MaterializationSnapshots)
	if err != nil {
		return nil, nil, err
	}
	editable := make(map[string]struct{}, len(active.ConflictPaths)+len(active.RecoveryPaths))
	for _, value := range append(
		append([]string{}, active.ConflictPaths...),
		active.RecoveryPaths...,
	) {
		path, err := pathmodel.Parse(value)
		if err != nil {
			return nil, nil, err
		}
		editable[pathmodel.Canonical(path, ignoreCase)] = struct{}{}
	}
	return snapshots, editable, nil
}

func decodeWorkspaceSnapshots(persisted []linkstate.WorkspaceSnapshot) (map[string]fileSnapshot, error) {
	snapshots := make(map[string]fileSnapshot, len(persisted))
	for _, value := range persisted {
		var digest [32]byte
		if value.Existed {
			decoded, err := hex.DecodeString(value.Digest)
			if err != nil || len(decoded) != len(digest) {
				return nil, fmt.Errorf("decode workspace snapshot digest for %q", value.Path)
			}
			copy(digest[:], decoded)
		}
		snapshots[value.Path] = fileSnapshot{
			Digest:     digest,
			Existed:    value.Existed,
			Executable: value.Executable,
		}
	}
	return snapshots, nil
}

func encodeOverrideStatuses(
	overrides map[string]pathmodel.Path,
	statuses map[string][]byte,
) []linkstate.OverrideStatus {
	persisted := make([]linkstate.OverrideStatus, 0, len(overrides))
	for value := range overrides {
		persisted = append(persisted, linkstate.OverrideStatus{
			Path:   value,
			Status: string(statuses[value]),
		})
	}
	return persisted
}

func decodeOverrideStatuses(persisted []linkstate.OverrideStatus) map[string][]byte {
	statuses := make(map[string][]byte, len(persisted))
	for _, value := range persisted {
		statuses[value.Path] = []byte(value.Status)
	}
	return statuses
}

// verifyExclusion proves every path is effectively excluded from public Git.
func (a App) verifyExclusion(ctx context.Context, repository publicgit.Repository, paths []pathmodel.Path) error {
	verifyPaths := append([]pathmodel.Path{}, paths...)
	if len(paths) > 0 {
		tempProbe, err := pathmodel.Parse(filesync.ManagedTempProbe)
		if err != nil {
			return err
		}
		verifyPaths = append(verifyPaths, tempProbe)
	}
	unexcluded, err := repository.UnexcludedPaths(ctx, verifyPaths)
	if err != nil {
		return err
	}
	if len(unexcluded) > 0 {
		return spaserr.Wrap(
			spaserr.KindExclusionValidation,
			fmt.Errorf("%q is not effectively excluded from public Git; a higher-precedence .gitignore rule may re-include it", unexcluded[0]),
		)
	}
	return nil
}

func (a App) ensurePrivateIdentity(ctx context.Context, public publicgit.Repository, private privategit.Repository) error {
	for _, key := range []string{"user.name", "user.email"} {
		result, err := a.Git.Run(ctx, private.Path, "config", "--get", key)
		if err == nil {
			if strings.TrimSpace(string(result.Stdout)) != "" {
				continue
			}
		} else if code, ok := gitexec.ExitCode(err); !ok || code != 1 {
			return fmt.Errorf("read private clone %s: %w", key, err)
		}
		result, err = a.Git.Run(ctx, public.Root, "config", "--get", key)
		if err != nil || strings.TrimSpace(string(result.Stdout)) == "" {
			return fmt.Errorf("private commit requires Git %s; configure it in the public repository or global Git settings", key)
		}
		if _, err := a.Git.Run(ctx, private.Path, "config", "--local", key, strings.TrimSpace(string(result.Stdout))); err != nil {
			return fmt.Errorf("configure private clone %s: %w", key, err)
		}
	}
	return nil
}

func (a App) continueSync(ctx context.Context, repository publicgit.Repository, state *linkstate.State, options SyncOptions) (returnErr error) {
	if !state.Private.Initialized {
		return fmt.Errorf("private repository is not initialized")
	}
	private := a.privateRepository(*state)
	if err := private.EnsureSafety(ctx); err != nil {
		return err
	}
	if err := a.requirePrimarySingleWorktree(ctx, repository); err != nil {
		return err
	}
	ignoreCase, err := a.casePolicy(ctx, repository)
	if err != nil {
		return err
	}
	if publicUnmerged, err := repository.UnmergedPaths(ctx); err != nil {
		return err
	} else if len(publicUnmerged) > 0 {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("public repository has unresolved merge conflicts"))
	}
	remoteURL := state.Private.RemoteURL
	if err := private.VerifyOrigin(ctx, remoteURL); err != nil {
		return err
	}
	mergeInProgress, mergeProbeErr := private.MergeInProgress()
	if mergeProbeErr != nil {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("inspect private merge state: %w", mergeProbeErr))
	}
	if state.Materializing != nil {
		phase := state.Materializing.Phase
		if phase != linkstate.MaterializationMergeContinuing && phase != linkstate.MaterializationMergeStaged {
			return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("a private result is awaiting recovery; run spas sync without --continue"))
		}
		if !mergeInProgress {
			return a.resumeMaterialization(ctx, repository, state, private, ignoreCase)
		}
		pending, err := private.UnmergedPaths(ctx)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			if phase != linkstate.MaterializationMergeStaged {
				return spaserr.Wrap(
					spaserr.KindUnsafeGitState,
					fmt.Errorf("private merge resolution staging was interrupted before its approved bytes were verified; run spas sync --abort and resolve the merge again"),
				)
			}
			mergeHead, err := private.MergeHead(ctx)
			if err != nil {
				return err
			}
			stagedTree, err := private.IndexTree(ctx)
			if err != nil {
				return err
			}
			if state.Materializing.MergeHead != mergeHead || state.Materializing.StagedTree != stagedTree {
				return spaserr.Wrap(
					spaserr.KindUnsafeGitState,
					fmt.Errorf("verified private merge-resolution index changed after staging; run spas sync --abort and resolve the merge again"),
				)
			}
			message := strings.TrimSpace(options.Message)
			if message == "" {
				message, err = a.Prompt.Input(ctx, "Reason for the private merge-resolution commit:")
				if err != nil {
					return err
				}
			}
			if err := a.ensurePrivateIdentity(ctx, repository, private); err != nil {
				return err
			}
			if err := private.ValidateIndex(ctx); err != nil {
				return err
			}
			if err := private.Commit(ctx, message); err != nil {
				return err
			}
			stillMerging, err := private.MergeInProgress()
			if err != nil {
				return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("verify Git cleared the completed private merge; recovery state was retained: %w", err))
			}
			if stillMerging {
				return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("Git completed the private merge commit but MERGE_HEAD remains; recovery state was retained"))
			}
			resultHead, err := private.Head(ctx)
			if err != nil {
				return err
			}
			validResult, err := private.IsMergeResultOf(
				ctx,
				resultHead,
				state.Materializing.ResultPrivateHead,
				state.Materializing.MergeHead,
				state.Materializing.StagedTree,
			)
			if err != nil {
				return err
			}
			if !validResult {
				return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("private merge commit does not match the verified parents and tree"))
			}
			if err := private.ValidateTree(ctx, resultHead); err != nil {
				return err
			}
			state.Materializing.ResultPrivateHead = resultHead
			state.Materializing.Phase = linkstate.MaterializationPushPending
			if err := a.Store.Save(*state); err != nil {
				return fmt.Errorf("save completed private merge result: %w", err)
			}
			return a.resumeMaterialization(ctx, repository, state, private, ignoreCase)
		}
		if phase == linkstate.MaterializationMergeStaged {
			return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("private merge has unresolved paths after its resolution was marked staged; run spas sync --abort"))
		}
	}
	if !mergeInProgress {
		return fmt.Errorf("no private merge is in progress")
	}
	if state.ActiveMerge == nil {
		return fmt.Errorf("private merge recovery state is missing; run spas sync --abort")
	}
	if !state.ActiveMerge.ConflictFilesReady {
		return spaserr.Wrap(
			spaserr.KindUnsafeGitState,
			fmt.Errorf("private merge conflict materialization was interrupted before it was safe to resolve; run spas sync --abort"),
		)
	}
	activeSnapshots, err := decodeWorkspaceSnapshots(state.ActiveMerge.WorkspaceSnapshots)
	if err != nil {
		return err
	}
	if err := verifyActiveMergeRecoveryPaths(repository.Root, state.ActiveMerge, activeSnapshots, ignoreCase); err != nil {
		return err
	}
	if err := verifyActiveMergeMaterializationPaths(
		repository.Root,
		state.ActiveMerge,
		stringsToPaths(state.ActiveMerge.MaterializationPaths),
		ignoreCase,
	); err != nil {
		return err
	}
	approvedOverrideStatuses := decodeOverrideStatuses(state.ActiveMerge.OverrideStatuses)
	unmerged, err := private.UnmergedPaths(ctx)
	if err != nil {
		return err
	}
	privateHead, err := private.Head(ctx)
	if err != nil {
		return err
	}
	if privateHead != state.ActiveMerge.PreMergeHead {
		return spaserr.Wrap(
			spaserr.KindUnsafeGitState,
			fmt.Errorf("private merge base changed from recorded commit %s to %s; run spas sync --abort", state.ActiveMerge.PreMergeHead, privateHead),
		)
	}
	mergeHead, err := private.MergeHead(ctx)
	if err != nil {
		return err
	}
	if mergeHead != state.ActiveMerge.MergeHead {
		return spaserr.Wrap(
			spaserr.KindUnsafeGitState,
			fmt.Errorf("private merge target changed from recorded commit %s to %s; run spas sync --abort", state.ActiveMerge.MergeHead, mergeHead),
		)
	}
	if !samePathSet(unmerged, state.ActiveMerge.ConflictPaths) {
		return spaserr.Wrap(
			spaserr.KindUnsafeGitState,
			fmt.Errorf("private merge conflict paths changed outside SPAS; run spas sync --abort"),
		)
	}
	if len(unmerged) == 0 {
		return fmt.Errorf("private merge has no unresolved paths")
	}
	skipped := stringSet(state.ActiveMerge.SkippedPaths)
	deferred := stringSet(state.ActiveMerge.DeferredPaths)
	materializeSkip := unionSets(skipped, deferred)
	remainingPendingAdds := append([]string{}, state.ActiveMerge.RemainingPendingAdds...)
	remainingPendingRemoves := append([]linkstate.PendingRemoval{}, state.ActiveMerge.RemainingPendingRemoves...)
	privateIndexPaths, err := private.TrackedPaths(ctx)
	if err != nil {
		return err
	}
	overrides, privateOverrides, overrideReplacement, err := resolveStoredOverrides(
		state.ActiveMerge.OverridePaths,
		privateIndexPaths,
	)
	if err != nil {
		return err
	}
	recoveryPaths := stringsToPaths(state.ActiveMerge.RecoveryPaths)
	overrideMaterialize := make(map[string]pathmodel.Path, len(overrides)+len(privateOverrides))
	for value, path := range overrides {
		overrideMaterialize[value] = path
	}
	for value, path := range privateOverrides {
		overrideMaterialize[value] = path
	}
	for _, path := range unmerged {
		if _, blocked := skipped[path.String()]; blocked {
			return fmt.Errorf("private merge contains skipped path %q; abort and reconcile the private branch before retrying", path)
		}
		if _, blocked := overrideMaterialize[path.String()]; blocked {
			return fmt.Errorf("private merge contains public-owned override path %q; abort and reconcile the private branch before retrying", path)
		}
	}
	if err := revalidatePublicOwnership(ctx, repository, unmerged, nil, nil, ignoreCase); err != nil {
		return err
	}
	excludePath, err := repository.InfoExcludePath(ctx)
	if err != nil {
		return err
	}
	preResolutionExcluded := filterSkipped(
		unionPaths(stringsToPaths(state.ManagedPaths), stringsToPaths(remainingPendingAdds), unmerged),
		skipped,
	)
	preResolutionPlan, err := exclude.Build(excludePath, state.Exclude.BlockID, preResolutionExcluded)
	if err != nil {
		return err
	}
	if err := exclude.Apply(preResolutionPlan); err != nil {
		return err
	}
	if err := a.verifyExclusion(ctx, repository, preResolutionExcluded); err != nil {
		return errors.Join(err, exclude.Restore(preResolutionPlan))
	}
	message := strings.TrimSpace(options.Message)
	if message == "" {
		message, err = a.Prompt.Input(ctx, "Reason for the private merge-resolution commit:")
		if err != nil {
			return err
		}
	}
	resolutionSnapshots, err := snapshotPaths(repository.Root, unmerged)
	if err != nil {
		return err
	}
	resolutionChanges := make([]plannedChange, 0, len(unmerged))
	var writes []pathmodel.Path
	var removals []pathmodel.Path
	for _, path := range unmerged {
		source := path.OSPath(repository.Root)
		if err := pathmodel.InspectRegularFile(source); errors.Is(err, os.ErrNotExist) {
			if err := filesync.RemoveManaged(private.Path, path); err != nil {
				return err
			}
			removals = append(removals, path)
			resolutionChanges = append(resolutionChanges, plannedChange{Path: path, Status: "D"})
			continue
		} else if err != nil {
			return fmt.Errorf("resolved file %q: %w", path, err)
		}
		if err := filesync.CopyManaged(repository.Root, path, private.Path, path); err != nil {
			return err
		}
		writes = append(writes, path)
		resolutionChanges = append(resolutionChanges, plannedChange{Path: path, Status: "M"})
	}
	if err := verifyApprovedSnapshots(repository.Root, resolutionChanges, resolutionSnapshots); err != nil {
		return err
	}
	finalPaths, err := private.TrackedPaths(ctx)
	if err != nil {
		return err
	}
	finalByPath := make(map[string]pathmodel.Path, len(finalPaths)+len(writes))
	for _, path := range unionPaths(finalPaths, writes) {
		finalByPath[path.String()] = path
	}
	for _, path := range removals {
		delete(finalByPath, path.String())
	}
	finalPaths = mapPathValues(finalByPath)
	recoveryPaths = filterRecoveryPaths(recoveryPaths, finalPaths, overrides)
	snapshotCandidates := materializationCandidates(finalPaths, state.ManagedPaths, materializeSkip)
	if err := verifyActiveMergeMaterializationPaths(
		repository.Root,
		state.ActiveMerge,
		snapshotCandidates,
		ignoreCase,
	); err != nil {
		return err
	}
	snapshots, err := snapshotPaths(repository.Root, snapshotCandidates)
	if err != nil {
		return err
	}
	if err := verifyActiveMergeMaterializationSnapshots(
		state.ActiveMerge,
		snapshotCandidates,
		snapshots,
		ignoreCase,
	); err != nil {
		return err
	}
	overrideStatuses := make(map[string][]byte)
	overrideSnapshots := make(map[string]fileSnapshot)
	trackedOverrides, err := currentlyTrackedOverrides(ctx, repository, overrides)
	if err != nil {
		return err
	}
	for _, path := range overrides {
		status, err := repository.PathStatus(ctx, path)
		if err != nil {
			return err
		}
		snapshot, err := snapshotFile(path.OSPath(repository.Root))
		if err != nil {
			return fmt.Errorf("snapshot public override path %q: %w", path, err)
		}
		approvedSnapshot, found := activeSnapshots[path.String()]
		if !found {
			return fmt.Errorf("ownership override snapshot for active merge path %q is missing", path)
		}
		overrideSnapshots[path.String()] = snapshot
		approvedStatus, found := approvedOverrideStatuses[path.String()]
		if !found {
			return fmt.Errorf("original public status for active merge override path %q is missing", path)
		}
		changedSinceApproval := snapshot != approvedSnapshot || !bytes.Equal(status, approvedStatus)
		_, stillTracked := trackedOverrides[path.String()]
		ownershipAlreadyTransferred := !snapshot.Existed && !stillTracked
		if !ownershipAlreadyTransferred &&
			changedSinceApproval &&
			!options.DiscardPublicChanges {
			approved, err := a.Prompt.Confirm(
				ctx,
				fmt.Sprintf("Public path %q changed after ownership override approval while the private merge was being resolved. Discard its current content during ownership override?", path),
				false,
				false,
			)
			if err != nil {
				return err
			}
			if !approved {
				return spaserr.Wrap(spaserr.KindPathConflict, fmt.Errorf("ownership override for %q declined", path))
			}
		}
		overrideStatuses[path.String()] = append([]byte{}, status...)
	}
	if err := private.VerifyOrigin(ctx, remoteURL); err != nil {
		return err
	}
	mergeHead, err = private.MergeHead(ctx)
	if err != nil {
		return err
	}
	preMergeHead, err := private.Head(ctx)
	if err != nil {
		return err
	}
	managed := filterSkipped(finalPaths, skipped)
	excluded := unionPaths(managed, stringsToPaths(remainingPendingAdds))
	materialization, err := newMaterialization(
		preMergeHead,
		state.ManagedPaths,
		finalPaths,
		excluded,
		skipped,
		deferred,
		overrides,
		recoveryPaths,
		mergeFileSnapshots(snapshots, overrideSnapshots),
		remainingPendingAdds,
		remainingPendingRemoves,
	)
	if err != nil {
		return err
	}
	state.Materializing = materialization
	state.Materializing.Phase = linkstate.MaterializationMergeContinuing
	if err := a.Store.Save(*state); err != nil {
		return fmt.Errorf("save private merge continuation state: %w", err)
	}
	abortUnverifiedResolution := func(primary error) error {
		return a.abortFailedMerge(ctx, private, state, primary)
	}
	if err := private.StageMergeResolution(ctx, writes, removals); err != nil {
		return abortUnverifiedResolution(err)
	}
	if err := verifyApprovedSnapshots(repository.Root, resolutionChanges, resolutionSnapshots); err != nil {
		return abortUnverifiedResolution(err)
	}
	if err := verifyStagedBytes(ctx, private, resolutionChanges, resolutionSnapshots); err != nil {
		return abortUnverifiedResolution(err)
	}
	stagedTree, err := private.IndexTree(ctx)
	if err != nil {
		return abortUnverifiedResolution(err)
	}
	state.Materializing.MergeHead = mergeHead
	state.Materializing.StagedTree = stagedTree
	state.Materializing.Phase = linkstate.MaterializationMergeStaged
	if err := a.Store.Save(*state); err != nil {
		return fmt.Errorf("save verified private merge-resolution state: %w", err)
	}
	if err := private.Commit(ctx, message); err != nil {
		return err
	}
	stillMerging, err := private.MergeInProgress()
	if err != nil {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("verify Git cleared the completed private merge; recovery state was retained: %w", err))
	}
	if stillMerging {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("Git completed the private merge commit but MERGE_HEAD remains; recovery state was retained"))
	}
	resultHead, err := private.Head(ctx)
	if err != nil {
		return err
	}
	validResult, err := private.IsMergeResultOf(ctx, resultHead, preMergeHead, mergeHead, stagedTree)
	if err != nil {
		return err
	}
	if !validResult {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("private merge commit does not match the verified parents and tree"))
	}
	if err := private.ValidateTree(ctx, resultHead); err != nil {
		return err
	}
	state.Materializing.ResultPrivateHead = resultHead
	state.Materializing.Phase = linkstate.MaterializationPushPending
	if err := a.Store.Save(*state); err != nil {
		return fmt.Errorf("save pending private push state: %w", err)
	}
	if err := private.Push(ctx, state.Private.Branch); err != nil {
		return err
	}
	state.Materializing.Phase = linkstate.MaterializationPushed
	if err := a.Store.Save(*state); err != nil {
		return fmt.Errorf("save pushed private result state: %w", err)
	}
	if err := revalidatePublicOwnership(ctx, repository, finalPaths, skipped, overrides, ignoreCase); err != nil {
		return err
	}
	finalSet := stringSet(pathsToStrings(finalPaths))
	for value := range overrides {
		replacement := overrideReplacement[value]
		if _, ok := finalSet[replacement]; !ok {
			return spaserr.Wrap(spaserr.KindPathConflict, fmt.Errorf("refusing to remove public %q: the pushed private tree contains no replacement", value))
		}
	}
	if err := verifyOwnershipApprovals(ctx, repository, overrides, overrideStatuses, overrideSnapshots); err != nil {
		return err
	}

	// Exclusion is written and verified before any private content reaches
	// the public working tree.
	excludePlan, err := exclude.Build(excludePath, state.Exclude.BlockID, excluded)
	if err != nil {
		return err
	}
	if err := exclude.Apply(excludePlan); err != nil {
		return err
	}
	if err := a.verifyExclusion(ctx, repository, excluded); err != nil {
		return errors.Join(err, exclude.Restore(excludePlan))
	}

	recoveryStore, err := recovery.NewStore(a.Store.DataDir, state.LinkID)
	if err != nil {
		return err
	}
	defer func() {
		if returnErr != nil && recoveryStore.Used() {
			returnErr = fmt.Errorf("%w; recovery copies retained at %s", returnErr, recoveryStore.Root)
		}
	}()
	// Recheck immediately before destructive ownership transfer, after the
	// private push and exclusion update.
	if err := verifyOwnershipApprovals(ctx, repository, overrides, overrideStatuses, overrideSnapshots); err != nil {
		return err
	}
	if err := saveRecoveryCopies(
		recoveryStore,
		repository.Root,
		recoveryPaths,
		mergeFileSnapshots(snapshots, overrideSnapshots),
	); err != nil {
		return err
	}
	if err := revalidatePublicOwnership(ctx, repository, finalPaths, skipped, overrides, ignoreCase); err != nil {
		return err
	}
	trackedOverrides, err = currentlyTrackedOverrides(ctx, repository, overrides)
	if err != nil {
		return err
	}
	if len(trackedOverrides) > 0 {
		if err := repository.RemoveTracked(ctx, mapPathValues(trackedOverrides)); err != nil {
			return err
		}
	}
	materializationSnapshots := snapshotsForOverrideReplacements(
		mergeFileSnapshots(snapshots, overrideSnapshots),
		overrideSnapshots,
		overrideReplacement,
	)
	if err := materializeFinal(repository.Root, private.Path, finalPaths, state.ManagedPaths, materializeSkip, overrideReplacement, materializationSnapshots, ignoreCase); err != nil {
		return err
	}
	if err := a.verifyExclusion(ctx, repository, excluded); err != nil {
		return err
	}
	privateHead, err = private.Head(ctx)
	if err != nil {
		return err
	}
	remoteHead, err := private.RemoteHead(ctx, state.Private.Branch)
	if err != nil {
		return err
	}
	publicHead, err := repository.Head(ctx)
	if err != nil {
		return err
	}
	state.ManagedPaths = pathsToStrings(managed)
	state.PendingAdds = remainingPendingAdds
	state.PendingRemoves = remainingPendingRemoves
	state.ActiveMerge = nil
	state.Materializing = nil
	state.Private.RemoteEmpty = false
	state.Private.ExpectedHead = privateHead
	state.LastSync.PrivateHead = privateHead
	state.LastSync.RemoteHead = remoteHead
	state.LastSync.PublicHead = publicHead
	state.LastSync.CompletedAt = time.Now().UTC()
	if err := a.Store.Save(*state); err != nil {
		return err
	}
	summary := map[string]any{"synchronized": true, "mergeContinued": true}
	if recoveryStore.Used() {
		summary["recoveryCopies"] = recoveryStore.Root
	}
	return a.write(summary)
}

func (a App) abortFailedMerge(
	ctx context.Context,
	private privategit.Repository,
	state *linkstate.State,
	primary error,
) error {
	// Persist a deny-continue marker before rollback. If the process stops or
	// Git cannot abort, the next invocation remains constrained to --abort.
	markerSaveErr := a.Store.Save(*state)
	if markerSaveErr != nil {
		return errors.Join(
			primary,
			spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("could not persist private merge recovery state, so Git merge abort was not attempted; preserve the workspace and SPAS-managed clone, restore application-data storage, then retry the command or relink: %w", markerSaveErr),
			),
		)
	}
	abortErr := private.AbortMerge(ctx)
	if abortErr != nil {
		return errors.Join(
			primary,
			spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("automatic private merge abort failed; recovery state was retained—do not modify the SPAS-managed clone, then run spas sync --abort: %w", abortErr),
			),
		)
	}
	mergeInProgress, mergeProbeErr := private.MergeInProgress()
	if mergeProbeErr != nil {
		return errors.Join(
			primary,
			spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("could not verify private merge state after automatic abort; recovery state was retained—do not modify the SPAS-managed clone, then run spas sync --abort: %w", mergeProbeErr),
			),
		)
	}
	if mergeInProgress {
		return errors.Join(
			primary,
			spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("Git reported a successful merge abort but MERGE_HEAD remains; recovery state was retained—do not modify the SPAS-managed clone, then run spas sync --abort"),
			),
		)
	}
	if err := verifyPrivateAbortSource(ctx, private, "after automatic Git merge abort"); err != nil {
		return errors.Join(primary, err)
	}
	head, headErr := private.Head(ctx)
	if headErr != nil || state.ActiveMerge == nil || head != state.ActiveMerge.PreMergeHead {
		return errors.Join(
			primary,
			spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("automatic private merge abort could not verify the restored commit; recovery state was retained—do not modify the SPAS-managed clone, then run spas sync --abort"),
			),
			headErr,
		)
	}
	if err := verifyPrivateAbortSource(ctx, private, "after verifying the restored HEAD"); err != nil {
		return errors.Join(primary, err)
	}

	cleared := *state
	cleared.ActiveMerge = nil
	cleared.Materializing = nil
	if err := a.Store.Save(cleared); err != nil {
		return errors.Join(
			primary,
			fmt.Errorf("private merge was aborted but clearing its recovery state failed; run spas sync --abort: %w", err),
		)
	}
	*state = cleared
	return primary
}

func verifyPrivateAbortSource(ctx context.Context, private privategit.Repository, phase string) error {
	clean, err := private.IsClean(ctx)
	if err != nil {
		return spaserr.Wrap(
			spaserr.KindUnsafeGitState,
			fmt.Errorf("inspect private clone %s: %w", phase, err),
		)
	}
	if !clean {
		return spaserr.Wrap(
			spaserr.KindUnsafeGitState,
			fmt.Errorf("private clone is not clean %s; recovery state was retained—do not modify the SPAS-managed clone, then retry spas sync --abort", phase),
		)
	}
	return nil
}

func (a App) abortSync(ctx context.Context, repository publicgit.Repository, state *linkstate.State) (returnErr error) {
	if !state.Private.Initialized {
		return fmt.Errorf("private repository is not initialized")
	}
	private := a.privateRepository(*state)
	if err := private.EnsureSafety(ctx); err != nil {
		return err
	}
	if err := a.requirePrimarySingleWorktree(ctx, repository); err != nil {
		return err
	}
	mergeInProgress, err := private.MergeInProgress()
	if err != nil {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("could not inspect private merge state: %w", err))
	}
	if state.ActiveMerge == nil {
		if !mergeInProgress {
			return fmt.Errorf("no private merge is in progress")
		}
		if err := private.AbortMerge(ctx); err != nil {
			return spaserr.Wrap(spaserr.KindUnsafeGitState, err)
		}
		stillMerging, err := private.MergeInProgress()
		if err != nil {
			return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("verify private merge abort: %w", err))
		}
		if stillMerging {
			return spaserr.Wrap(spaserr.KindUnsafeGitState, fmt.Errorf("Git reported a successful merge abort but MERGE_HEAD remains"))
		}
		if err := verifyPrivateAbortSource(ctx, private, "after Git-native recovery abort"); err != nil {
			return err
		}
		return a.write(map[string]any{"mergeAborted": true, "gitNativeRecovery": true})
	}
	if mergeInProgress {
		head, err := private.Head(ctx)
		if err != nil {
			return err
		}
		mergeHead, err := private.MergeHead(ctx)
		if err != nil {
			return err
		}
		if head != state.ActiveMerge.PreMergeHead || mergeHead != state.ActiveMerge.MergeHead {
			return spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("Git merge metadata does not match the recorded SPAS merge binding; no abort was attempted"),
			)
		}
	}
	if len(state.ActiveMerge.ConflictPaths) == 0 {
		if mergeInProgress {
			if err := private.AbortMerge(ctx); err != nil {
				return spaserr.Wrap(
					spaserr.KindUnsafeGitState,
					fmt.Errorf("abort private merge; recovery state was retained—do not modify the SPAS-managed clone, then retry spas sync --abort: %w", err),
				)
			}
		}
		mergeInProgress, mergeProbeErr := private.MergeInProgress()
		if mergeProbeErr != nil {
			return spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("could not verify private merge state after abort; recovery state was retained—do not modify the SPAS-managed clone, then retry spas sync --abort: %w", mergeProbeErr),
			)
		}
		if mergeInProgress {
			return spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("Git reported a successful merge abort but MERGE_HEAD remains; recovery state was retained—do not modify the SPAS-managed clone, then retry spas sync --abort"),
			)
		}
		if err := verifyPrivateAbortSource(ctx, private, "after Git merge abort"); err != nil {
			return err
		}
		privateHead, err := private.Head(ctx)
		if err != nil {
			return err
		}
		completed, completedErr := private.IsMergeCommitOf(
			ctx,
			privateHead,
			state.ActiveMerge.PreMergeHead,
			state.ActiveMerge.MergeHead,
		)
		if completedErr != nil {
			return completedErr
		}
		if privateHead != state.ActiveMerge.PreMergeHead && !completed {
			return spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("private branch moved while abort-only recovery was pending: found %q, expected an aborted or completed bound merge; no recovery state was cleared", privateHead),
			)
		}
		if err := verifyPrivateAbortSource(ctx, private, "after verifying the restored HEAD"); err != nil {
			return err
		}
		mergeAborted := privateHead == state.ActiveMerge.PreMergeHead
		cleared := *state
		cleared.ActiveMerge = nil
		cleared.Materializing = nil
		if err := a.Store.Save(cleared); err != nil {
			return fmt.Errorf("private merge was aborted but clearing its recovery state failed; retry spas sync --abort: %w", err)
		}
		*state = cleared
		return a.write(map[string]any{"mergeAborted": mergeAborted, "mergeRecoveryCleared": true})
	}
	if !mergeInProgress {
		privateHead, err := private.Head(ctx)
		if err != nil {
			return err
		}
		if privateHead != state.ActiveMerge.PreMergeHead {
			return spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("private branch moved while merge abort recovery was pending: found %q, expected %q", privateHead, state.ActiveMerge.PreMergeHead),
			)
		}
		if err := verifyPrivateAbortSource(ctx, private, "before preparing merge abort recovery"); err != nil {
			return err
		}
	}
	conflictPaths := stringsToPaths(state.ActiveMerge.ConflictPaths)
	abortRecoveryPaths := unionPaths(
		conflictPaths,
		stringsToPaths(state.ActiveMerge.RecoveryPaths),
	)
	preAbortPaths, err := private.TreePaths(ctx, state.ActiveMerge.PreMergeHead)
	if err != nil {
		return err
	}
	preAbortIgnoreCase, err := a.casePolicy(ctx, repository)
	if err != nil {
		return err
	}
	preAbortPublicPaths, err := repository.TrackedPaths(ctx)
	if err != nil {
		return err
	}
	preAbortSkipped := stringSet(state.ActiveMerge.SkippedPaths)
	for _, conflict := range collision.Detect(preAbortPublicPaths, preAbortPaths, preAbortIgnoreCase) {
		preAbortSkipped[conflict.Private.String()] = struct{}{}
	}
	preAbortMaterializeSkip := unionSets(
		preAbortSkipped,
		stringSet(state.ActiveMerge.DeferredPaths),
	)
	if err := verifyActiveMergeMaterializationPaths(
		repository.Root,
		state.ActiveMerge,
		filterSkipped(preAbortPaths, preAbortMaterializeSkip),
		preAbortIgnoreCase,
	); err != nil {
		return err
	}
	recoveryStore, err := recovery.NewStore(a.Store.DataDir, state.LinkID)
	if err != nil {
		return err
	}
	defer func() {
		if returnErr != nil && recoveryStore.Used() {
			returnErr = fmt.Errorf("%w; recovery copies retained at %s", returnErr, recoveryStore.Root)
		}
	}()
	// Aborting a merge intentionally discards conflict resolutions in the
	// private clone and restores the pre-merge private commit. Preserve both
	// conflict paths and approved workspace obstructions first so --abort never
	// makes either copy unrecoverable.
	abortRecoverySnapshots, err := snapshotPaths(repository.Root, abortRecoveryPaths)
	if err != nil {
		return err
	}
	if err := saveRecoveryCopies(
		recoveryStore,
		repository.Root,
		abortRecoveryPaths,
		abortRecoverySnapshots,
	); err != nil {
		return err
	}

	mergeInProgress, mergeProbeErr := private.MergeInProgress()
	if mergeProbeErr != nil {
		return spaserr.Wrap(
			spaserr.KindUnsafeGitState,
			fmt.Errorf("could not inspect private merge state; recovery state and workspace files were retained—do not modify the SPAS-managed clone, then retry spas sync --abort: %w", mergeProbeErr),
		)
	}
	if mergeInProgress {
		if err := private.AbortMerge(ctx); err != nil {
			return spaserr.Wrap(
				spaserr.KindUnsafeGitState,
				fmt.Errorf("abort private merge; recovery state and workspace files were retained—do not modify the SPAS-managed clone, then retry spas sync --abort: %w", err),
			)
		}
	}
	mergeInProgress, mergeProbeErr = private.MergeInProgress()
	if mergeProbeErr != nil {
		return spaserr.Wrap(
			spaserr.KindUnsafeGitState,
			fmt.Errorf("could not verify private merge state after abort; recovery state and workspace files were retained—retry spas sync --abort: %w", mergeProbeErr),
		)
	}
	if mergeInProgress {
		return spaserr.Wrap(
			spaserr.KindUnsafeGitState,
			fmt.Errorf("Git reported a successful merge abort but MERGE_HEAD remains; recovery state and workspace files were retained—retry spas sync --abort"),
		)
	}
	if err := verifyPrivateAbortSource(ctx, private, "after Git merge abort"); err != nil {
		return err
	}
	if err := verifyPathSnapshots(repository.Root, abortRecoveryPaths, abortRecoverySnapshots); err != nil {
		return spaserr.Wrap(
			spaserr.KindUnsafeGitState,
			fmt.Errorf("workspace changed while Git was aborting the private merge; recovery state and copies were retained: %w", err),
		)
	}
	// A crash may happen after git merge --abort but before SPAS restores the
	// public workspace or clears ActiveMerge. Permit a repeated --abort only
	// when the private branch is exactly the commit recorded before the merge.
	privateHead, err := private.Head(ctx)
	if err != nil {
		return err
	}
	if privateHead != state.ActiveMerge.PreMergeHead {
		return spaserr.Wrap(
			spaserr.KindUnsafeGitState,
			fmt.Errorf("private branch moved while merge abort recovery was pending: found %q, expected %q", privateHead, state.ActiveMerge.PreMergeHead),
		)
	}
	if err := verifyPrivateAbortSource(ctx, private, "after verifying the restored HEAD"); err != nil {
		return err
	}
	paths, err := private.TrackedPaths(ctx)
	if err != nil {
		return err
	}
	if err := private.ValidateTree(ctx, privateHead); err != nil {
		return err
	}
	ignoreCase, err := a.casePolicy(ctx, repository)
	if err != nil {
		return err
	}
	skipped := stringSet(state.ActiveMerge.SkippedPaths)
	deferred := stringSet(state.ActiveMerge.DeferredPaths)
	remainingPendingAdds := append([]string{}, state.ActiveMerge.RemainingPendingAdds...)
	remainingPendingRemoves := append([]linkstate.PendingRemoval{}, state.ActiveMerge.RemainingPendingRemoves...)
	publicPaths, err := repository.TrackedPaths(ctx)
	if err != nil {
		return err
	}
	for _, conflict := range collision.Detect(publicPaths, paths, ignoreCase) {
		skipped[conflict.Private.String()] = struct{}{}
	}

	// Conflict files copied from the temporary merge can include paths that no
	// longer exist in the restored pre-merge private commit (for example a
	// modify/delete conflict). Remove those stale copies while the old local
	// exclusion block is still active. Never remove a path now owned by public
	// Git; report it as skipped instead.
	blockedConflictPaths := make(map[string]struct{})
	for _, conflict := range collision.Detect(publicPaths, conflictPaths, ignoreCase) {
		blockedConflictPaths[conflict.Private.String()] = struct{}{}
		skipped[conflict.Private.String()] = struct{}{}
	}
	currentPrivatePaths := stringSet(pathsToStrings(paths))
	if err := verifyPrivateAbortSource(ctx, private, "before restoring the public workspace"); err != nil {
		return err
	}
	for _, path := range conflictPaths {
		if _, remains := currentPrivatePaths[path.String()]; remains {
			continue
		}
		if _, preserve := deferred[path.String()]; preserve {
			continue
		}
		if _, blocked := blockedConflictPaths[path.String()]; blocked {
			continue
		}
		snapshot, found := abortRecoverySnapshots[path.String()]
		if !found {
			return fmt.Errorf("workspace snapshot for merge-abort stale path %q is missing", path)
		}
		if err := filesync.RemoveManagedIfUnchanged(
			repository.Root,
			path,
			expectedFilesyncSnapshot(snapshot),
		); err != nil {
			return err
		}
	}

	// Managed paths still include deferred local edits, but those paths are not
	// overwritten while abort restores the pre-merge private snapshot. Pending
	// additions remain excluded even though they are not yet in the private
	// commit.
	managed := filterSkipped(paths, skipped)
	materializeSkip := unionSets(skipped, deferred)
	materialized := filterSkipped(paths, materializeSkip)
	pendingExcluded := filterSkipped(stringsToPaths(remainingPendingAdds), skipped)
	excluded := unionPaths(managed, pendingExcluded)
	excludePath, err := repository.InfoExcludePath(ctx)
	if err != nil {
		return err
	}
	excludePlan, err := exclude.Build(excludePath, state.Exclude.BlockID, excluded)
	if err != nil {
		return err
	}
	if err := exclude.Apply(excludePlan); err != nil {
		return err
	}
	if err := a.verifyExclusion(ctx, repository, excluded); err != nil {
		return errors.Join(err, exclude.Restore(excludePlan))
	}
	if err := verifyActiveMergeMaterializationPaths(
		repository.Root,
		state.ActiveMerge,
		materialized,
		ignoreCase,
	); err != nil {
		return err
	}
	abortSnapshots, err := snapshotPaths(repository.Root, materialized)
	if err != nil {
		return err
	}
	if err := verifyActiveMergeMaterializationSnapshots(
		state.ActiveMerge,
		materialized,
		abortSnapshots,
		ignoreCase,
	); err != nil {
		return err
	}
	if err := verifyPrivateAbortSource(ctx, private, "immediately before materialization"); err != nil {
		return err
	}
	for _, path := range materialized {
		snapshot, found := abortSnapshots[path.String()]
		if !found {
			return fmt.Errorf("workspace snapshot for merge-abort materialization path %q is missing", path)
		}
		if recoverySnapshot, editable := abortRecoverySnapshots[path.String()]; editable {
			snapshot = recoverySnapshot
		}
		if err := filesync.CopyManagedIfUnchanged(
			private.Path,
			path,
			repository.Root,
			path,
			expectedFilesyncSnapshot(snapshot),
		); err != nil {
			return err
		}
	}
	if err := a.verifyExclusion(ctx, repository, excluded); err != nil {
		return err
	}
	state.ManagedPaths = pathsToStrings(managed)
	state.PendingAdds = remainingPendingAdds
	state.PendingRemoves = remainingPendingRemoves
	state.ActiveMerge = nil
	state.Materializing = nil
	if err := a.Store.Save(*state); err != nil {
		return err
	}
	summary := map[string]any{
		"mergeAborted":       true,
		"skippedPublicPaths": pathsToStrings(mapPaths(skipped)),
		"deferredPaths":      pathsToStrings(mapPaths(deferred)),
	}
	if recoveryStore.Used() {
		summary["recoveryCopies"] = recoveryStore.Root
	}
	return a.write(summary)
}

func (a App) conflictDecision(ctx context.Context, description string, policy ConflictPolicy) (ConflictPolicy, error) {
	switch policy {
	case ConflictSkip, ConflictOverride, ConflictAbort:
		return policy, nil
	case ConflictAsk, "":
		value, err := a.Prompt.Select(ctx, description, []interaction.Option{
			{Key: "s", Value: string(ConflictSkip), Label: "Skip this private path for this sync"},
			{Key: "o", Value: string(ConflictOverride), Label: "Override the local public path with the private version"},
			{Key: "a", Value: string(ConflictAbort), Label: "Abort synchronization"},
		})
		return ConflictPolicy(value), err
	default:
		return "", fmt.Errorf("invalid conflict policy %q", policy)
	}
}

func (a App) approveCommit(ctx context.Context, changes []plannedChange, supplied string) (string, error) {
	if len(changes) == 0 {
		return "", nil
	}
	if message := strings.TrimSpace(supplied); message != "" {
		return message, nil
	}
	if a.JSON {
		paths := make([]pathmodel.Path, 0, len(changes))
		for _, change := range changes {
			paths = append(paths, change.Path)
		}
		return "", spaserr.Wrap(
			spaserr.KindDecisionRequired,
			fmt.Errorf("local managed asset changes require approval to commit %s to the linked repository; provide --message", joinPaths(paths)),
		)
	}
	if _, err := fmt.Fprintln(a.Out, "Local managed asset changes require one commit in the linked repository:"); err != nil {
		return "", err
	}
	for _, change := range changes {
		if _, err := fmt.Fprintf(a.Out, "  %s  %s\n", change.Status, change.Path); err != nil {
			return "", err
		}
	}
	approved, err := a.Prompt.Confirm(ctx, "Create this commit in the linked repository?", false, false)
	if err != nil {
		return "", err
	}
	if !approved {
		return "", fmt.Errorf("commit in the linked repository declined")
	}
	return a.Prompt.Input(ctx, "Reason for the commit in the linked repository:")
}

// localChangePlan is the result of comparing the workspace against the
// private clone.
type localChangePlan struct {
	Changes   []plannedChange
	Snapshots map[string]fileSnapshot
	// DeferredAdds are pending additions whose workspace file is currently
	// missing; enrollment and exclusion are retained without staging.
	DeferredAdds []pathmodel.Path
	// DeferredRemovals are pending removals whose workspace file changed
	// after removal was requested; nothing is deleted for them.
	DeferredRemovals []pathmodel.Path
}

func planLocalChanges(
	publicRoot, privateRoot string,
	privatePaths, pendingAdds []pathmodel.Path,
	managed map[string]struct{},
	pendingRemoves map[string]linkstate.PendingRemoval,
	skipped map[string]struct{},
	overridePublic map[string]pathmodel.Path,
) (localChangePlan, error) {
	candidates := unionPaths(privatePaths, pendingAdds)
	plan := localChangePlan{Snapshots: make(map[string]fileSnapshot)}
	for _, path := range candidates {
		value := path.String()
		if _, skip := skipped[value]; skip {
			continue
		}
		publicPath := path.OSPath(publicRoot)
		snapshot, err := snapshotFile(publicPath)
		if err != nil {
			return localChangePlan{}, err
		}
		digest, existed := snapshot.Digest, snapshot.Existed
		plan.Snapshots[value] = snapshot
		if removal, removing := pendingRemoves[value]; removing {
			// The removal proceeds only when the workspace file still
			// matches its state at request time; a later edit defers it.
			changedSinceRequest := removal.Existed != existed ||
				(existed && hex.EncodeToString(digest[:]) != removal.Digest)
			if !changedSinceRequest && existed {
				changedSinceRequest = removal.Executable != snapshot.Executable
			}
			if changedSinceRequest {
				plan.DeferredRemovals = append(plan.DeferredRemovals, path)
			} else {
				plan.Changes = append(plan.Changes, plannedChange{Path: path, Status: "D"})
			}
			continue
		}
		if _, override := overridePublic[value]; override {
			continue
		}
		_, isManaged := managed[value]
		isPending := containsPath(pendingAdds, path)
		if !isManaged && !isPending {
			continue
		}
		if !existed {
			if isPending && !isManaged {
				// The enrolled file is temporarily missing; keep the
				// enrollment and its exclusion instead of dropping them.
				plan.DeferredAdds = append(plan.DeferredAdds, path)
			}
			continue
		}
		if err := pathmodel.InspectRegularFile(publicPath); err != nil {
			return localChangePlan{}, fmt.Errorf("%q: %w", path, err)
		}
		privatePath := path.OSPath(privateRoot)
		equal, err := filesync.Equal(publicPath, privatePath)
		if errors.Is(err, os.ErrNotExist) {
			plan.Changes = append(plan.Changes, plannedChange{Path: path, Status: "A"})
			continue
		}
		if err != nil {
			return localChangePlan{}, err
		}
		if !equal {
			plan.Changes = append(plan.Changes, plannedChange{Path: path, Status: "M"})
		}
	}
	sort.Slice(plan.Changes, func(i, j int) bool { return plan.Changes[i].Path < plan.Changes[j].Path })
	sort.Slice(plan.DeferredAdds, func(i, j int) bool { return plan.DeferredAdds[i] < plan.DeferredAdds[j] })
	sort.Slice(plan.DeferredRemovals, func(i, j int) bool { return plan.DeferredRemovals[i] < plan.DeferredRemovals[j] })
	return plan, nil
}

func applyLocalChanges(publicRoot, privateRoot string, changes []plannedChange) ([]pathmodel.Path, []pathmodel.Path, error) {
	var writes []pathmodel.Path
	var removals []pathmodel.Path
	for _, change := range changes {
		if change.Status == "D" {
			removals = append(removals, change.Path)
			continue
		}
		if err := filesync.CopyManaged(publicRoot, change.Path, privateRoot, change.Path); err != nil {
			return nil, nil, err
		}
		writes = append(writes, change.Path)
	}
	return writes, removals, nil
}

func verifyApprovedSnapshots(publicRoot string, changes []plannedChange, snapshots map[string]fileSnapshot) error {
	for _, change := range changes {
		snapshot, found := snapshots[change.Path.String()]
		if !found {
			return fmt.Errorf("approved workspace snapshot for %q is missing", change.Path)
		}
		if err := verifyFileSnapshot(change.Path.OSPath(publicRoot), snapshot); err != nil {
			return fmt.Errorf("%q changed after commit approval for the linked repository: %w", change.Path, err)
		}
	}
	return nil
}

func verifyStagedBytes(
	ctx context.Context,
	private privategit.Repository,
	changes []plannedChange,
	snapshots map[string]fileSnapshot,
) error {
	for _, change := range changes {
		snapshot, found := snapshots[change.Path.String()]
		if change.Status == "D" {
			if !found {
				return fmt.Errorf("approved workspace snapshot for %q is missing", change.Path)
			}
			contained, err := private.IndexContains(ctx, change.Path)
			if err != nil {
				return err
			}
			if contained {
				return fmt.Errorf("private Git staging still contains approved deletion %q", change.Path)
			}
			continue
		}
		if !found || !snapshot.Existed {
			return fmt.Errorf("approved workspace snapshot for %q is missing", change.Path)
		}
		privateSnapshot, err := snapshotFile(change.Path.OSPath(private.Path))
		if err != nil {
			return err
		}
		if !privateSnapshot.Existed || privateSnapshot.Digest != snapshot.Digest ||
			privateSnapshot.Executable != snapshot.Executable {
			return fmt.Errorf("private working copy does not match the approved content and mode of %q", change.Path)
		}
		indexObjectID, err := private.IndexBlobOID(ctx, change.Path)
		if err != nil {
			return err
		}
		workingObjectID, err := private.FileBlobOID(ctx, change.Path.OSPath(private.Path))
		if err != nil {
			return err
		}
		if indexObjectID != workingObjectID {
			return fmt.Errorf("private Git staging does not match the approved bytes of %q", change.Path)
		}
	}
	return nil
}

func materializeFinal(
	publicRoot, privateRoot string,
	finalPaths []pathmodel.Path,
	previous []string,
	skipped map[string]struct{},
	overrideReplacements map[string]string,
	snapshots map[string]fileSnapshot,
	ignoreCase bool,
) error {
	finalSet := stringSet(pathsToStrings(finalPaths))

	// On a case-insensitive filesystem, a case-only rename such as Foo -> foo
	// aliases the same directory entry. Remove the stale spelling before copying
	// the final one; copying first and then removing Foo would delete foo too.
	// The old workspace bytes are verified before removal, and the final copy
	// requires the aliased destination to remain absent.
	preRemovals, aliasTargets, err := caseRenamePreRemovals(finalPaths, previous, skipped, ignoreCase)
	if err != nil {
		return err
	}
	for publicValue, privateValue := range overrideReplacements {
		if publicValue == privateValue {
			continue
		}
		publicPath, err := pathmodel.Parse(publicValue)
		if err != nil {
			return err
		}
		privatePath, err := pathmodel.Parse(privateValue)
		if err != nil {
			return err
		}
		preRemovals = unionPaths(preRemovals, []pathmodel.Path{publicPath})
		aliasTargets[pathmodel.Canonical(privatePath, ignoreCase)] = struct{}{}
	}
	preRemoved := make(map[string]struct{}, len(preRemovals))
	for _, path := range preRemovals {
		value := path.String()
		snapshot, found := snapshots[value]
		if !found {
			return fmt.Errorf("workspace snapshot for case-only removal %q is missing", path)
		}
		if err := filesync.RemoveManagedIfUnchanged(
			publicRoot,
			path,
			expectedFilesyncSnapshot(snapshot),
		); err != nil {
			return err
		}
		preRemoved[value] = struct{}{}
	}

	for _, path := range finalPaths {
		value := path.String()
		if _, skip := skipped[value]; skip {
			continue
		}
		snapshot, found := snapshots[value]
		if !found {
			return fmt.Errorf("workspace snapshot for materialization path %q is missing", path)
		}
		if _, aliasWasRemoved := aliasTargets[pathmodel.Canonical(path, ignoreCase)]; aliasWasRemoved {
			snapshot = fileSnapshot{Existed: false}
		}
		if err := filesync.CopyManagedIfUnchanged(
			privateRoot,
			path,
			publicRoot,
			path,
			expectedFilesyncSnapshot(snapshot),
		); err != nil {
			return err
		}
	}
	for _, value := range previous {
		if _, alreadyRemoved := preRemoved[value]; alreadyRemoved {
			continue
		}
		if _, remains := finalSet[value]; remains {
			continue
		}
		if _, skip := skipped[value]; skip {
			continue
		}
		path, err := pathmodel.Parse(value)
		if err != nil {
			return err
		}
		snapshot, found := snapshots[value]
		if !found {
			return fmt.Errorf("workspace snapshot for stale materialization path %q is missing", path)
		}
		if err := filesync.RemoveManagedIfUnchanged(
			publicRoot,
			path,
			expectedFilesyncSnapshot(snapshot),
		); err != nil {
			return err
		}
	}
	return nil
}

// caseRenamePreRemovals returns stale previous spellings that must be removed
// before final paths are copied on a case-insensitive filesystem. The second
// result is the set of final canonical destinations whose old alias was
// removed, allowing materialization to avoid validating an intentionally
// absent destination against its pre-sync snapshot.
func caseRenamePreRemovals(
	finalPaths []pathmodel.Path,
	previous []string,
	skipped map[string]struct{},
	ignoreCase bool,
) ([]pathmodel.Path, map[string]struct{}, error) {
	aliasTargets := make(map[string]struct{})
	if !ignoreCase {
		return nil, aliasTargets, nil
	}
	finalSet := stringSet(pathsToStrings(finalPaths))
	finalByCanonical := make(map[string]pathmodel.Path, len(finalPaths))
	for _, path := range finalPaths {
		if _, skip := skipped[path.String()]; skip {
			continue
		}
		canonical := pathmodel.Canonical(path, true)
		if existing, found := finalByCanonical[canonical]; found && existing != path {
			return nil, nil, fmt.Errorf("final private tree contains ambiguous case-equivalent paths %q and %q", existing, path)
		}
		finalByCanonical[canonical] = path
	}
	var result []pathmodel.Path
	for _, value := range previous {
		if _, remains := finalSet[value]; remains {
			continue
		}
		if _, skip := skipped[value]; skip {
			continue
		}
		path, err := pathmodel.Parse(value)
		if err != nil {
			return nil, nil, err
		}
		canonical := pathmodel.Canonical(path, true)
		final, found := finalByCanonical[canonical]
		if !found || final == path {
			continue
		}
		result = append(result, path)
		aliasTargets[canonical] = struct{}{}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, aliasTargets, nil
}

func copyConflictFiles(
	privateRoot, publicRoot string,
	paths []pathmodel.Path,
	snapshots map[string]fileSnapshot,
) error {
	for _, path := range paths {
		snapshot, found := snapshots[path.String()]
		if !found {
			return fmt.Errorf("workspace snapshot for private merge conflict %q is missing", path)
		}
		if err := verifyFileSnapshot(path.OSPath(publicRoot), snapshot); err != nil {
			return err
		}
		source := path.OSPath(privateRoot)
		if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := filesync.CopyManagedIfUnchanged(
			privateRoot,
			path,
			publicRoot,
			path,
			expectedFilesyncSnapshot(snapshot),
		); err != nil {
			return err
		}
	}
	return nil
}

func saveRecoveryCopies(
	store recovery.Store,
	publicRoot string,
	paths []pathmodel.Path,
	snapshots map[string]fileSnapshot,
) error {
	for _, path := range paths {
		snapshot, found := snapshots[path.String()]
		if !found {
			return fmt.Errorf("workspace snapshot for recovery path %q is missing", path)
		}
		if !snapshot.Existed {
			if err := verifyFileSnapshot(path.OSPath(publicRoot), snapshot); err != nil {
				return fmt.Errorf("verify absent recovery path %q: %w", path, err)
			}
			continue
		}
		saved, err := store.Save(publicRoot, path)
		if err != nil {
			return err
		}
		if !saved {
			return fmt.Errorf("approved recovery path %q no longer contains a regular file", path)
		}
		if err := verifyFileSnapshot(path.OSPath(store.Root), snapshot); err != nil {
			return fmt.Errorf("verify recovery copy of %q: %w", path, err)
		}
	}
	// A source may change while a later path is being copied. Recheck the
	// complete approved set before any caller proceeds to workspace mutation.
	for _, path := range paths {
		snapshot, found := snapshots[path.String()]
		if !found {
			return fmt.Errorf("workspace snapshot for recovery path %q is missing", path)
		}
		if err := verifyFileSnapshot(path.OSPath(publicRoot), snapshot); err != nil {
			return fmt.Errorf("reverify recovery source %q: %w", path, err)
		}
	}
	return nil
}

func verifyPathSnapshots(
	root string,
	paths []pathmodel.Path,
	snapshots map[string]fileSnapshot,
) error {
	for _, path := range paths {
		snapshot, found := snapshots[path.String()]
		if !found {
			return fmt.Errorf("workspace snapshot for %q is missing", path)
		}
		if err := verifyFileSnapshot(path.OSPath(root), snapshot); err != nil {
			return fmt.Errorf("verify workspace path %q: %w", path, err)
		}
	}
	return nil
}

func verifyOwnershipApprovals(
	ctx context.Context,
	repository publicgit.Repository,
	overrides map[string]pathmodel.Path,
	statuses map[string][]byte,
	snapshots map[string]fileSnapshot,
) error {
	for _, path := range overrides {
		current, err := repository.PathStatus(ctx, path)
		if err != nil {
			return err
		}
		if !bytes.Equal(current, statuses[path.String()]) {
			return spaserr.Wrap(spaserr.KindPathConflict, fmt.Errorf("public path %q changed after ownership override approval", path))
		}
		snapshot, found := snapshots[path.String()]
		if !found {
			return fmt.Errorf("ownership override snapshot for %q is missing", path)
		}
		if err := verifyFileSnapshot(path.OSPath(repository.Root), snapshot); err != nil {
			return spaserr.Wrap(
				spaserr.KindPathConflict,
				fmt.Errorf("public path %q changed after ownership override approval: %w", path, err),
			)
		}
	}
	return nil
}

// currentlyTrackedOverrides returns only approved ownership-transfer paths
// that public Git still tracks at the destructive boundary. A developer may
// explicitly complete the public untracking while a private merge is being
// resolved; in that case SPAS still materializes the approved private
// replacement but must not ask git rm to remove a path the public index no
// longer owns.
func currentlyTrackedOverrides(
	ctx context.Context,
	repository publicgit.Repository,
	overrides map[string]pathmodel.Path,
) (map[string]pathmodel.Path, error) {
	trackedPaths, err := repository.TrackedPaths(ctx)
	if err != nil {
		return nil, err
	}
	tracked := make(map[string]struct{}, len(trackedPaths))
	for _, path := range trackedPaths {
		tracked[path.String()] = struct{}{}
	}
	result := make(map[string]pathmodel.Path, len(overrides))
	for value, path := range overrides {
		if _, ok := tracked[value]; ok {
			result[value] = path
		}
	}
	return result, nil
}

func samePathSet(paths []pathmodel.Path, recorded []string) bool {
	if len(paths) != len(recorded) {
		return false
	}
	expected := stringSet(recorded)
	for _, path := range paths {
		if _, found := expected[path.String()]; !found {
			return false
		}
	}
	return true
}

func revalidatePublicOwnership(
	ctx context.Context,
	repository publicgit.Repository,
	privatePaths []pathmodel.Path,
	skipped map[string]struct{},
	overrides map[string]pathmodel.Path,
	ignoreCase bool,
) error {
	publicPaths, err := repository.TrackedPaths(ctx)
	if err != nil {
		return err
	}
	for _, conflict := range collision.Detect(publicPaths, privatePaths, ignoreCase) {
		if _, allowed := skipped[conflict.Private.String()]; allowed {
			continue
		}
		if conflict.Kind == collision.TrackedPath || conflict.Kind == collision.CaseInsensitive {
			if override, allowed := overrides[conflict.Public.String()]; allowed && override == conflict.Public {
				continue
			}
		}
		return spaserr.Wrap(spaserr.KindPathConflict, fmt.Errorf("public ownership changed during synchronization: %s", conflict.Error()))
	}
	return nil
}

func snapshotPaths(root string, paths []pathmodel.Path) (map[string]fileSnapshot, error) {
	result := make(map[string]fileSnapshot, len(paths))
	for _, path := range paths {
		snapshot, err := snapshotFile(path.OSPath(root))
		if err != nil {
			return nil, err
		}
		result[path.String()] = snapshot
	}
	return result, nil
}

func snapshotFile(path string) (fileSnapshot, error) {
	digest, existed, err := filesync.Snapshot(path)
	if err != nil || !existed {
		return fileSnapshot{Digest: digest, Existed: existed}, err
	}
	executable, err := filesync.Executable(path)
	if err != nil {
		return fileSnapshot{}, err
	}
	return fileSnapshot{Digest: digest, Existed: true, Executable: executable}, nil
}

func verifyFileSnapshot(path string, expected fileSnapshot) error {
	if err := filesync.VerifySnapshot(path, expected.Digest, expected.Existed); err != nil {
		return err
	}
	if !expected.Existed {
		return nil
	}
	executable, err := filesync.Executable(path)
	if err != nil {
		return err
	}
	if executable != expected.Executable {
		return fmt.Errorf("file executable mode changed after synchronization planning: %s", path)
	}
	return nil
}

func materializationCandidates(
	finalPaths []pathmodel.Path,
	previous []string,
	skipped map[string]struct{},
) []pathmodel.Path {
	return filterSkipped(unionPaths(finalPaths, stringsToPaths(previous)), skipped)
}

func validateProspectivePrivateTreeSize(paths []pathmodel.Path) error {
	if len(paths) > limits.MaxPrivateTreeEntries {
		return &privategit.TreeLimitError{
			Metric: "prospective tree-entry",
			Limit:  limits.MaxPrivateTreeEntries,
		}
	}
	return nil
}

// resolveStoredOverrides reconstructs the private spelling associated with each
// public override path. Active merge state stores public paths because those
// are the paths whose public ownership was approved for removal. A case-only
// override can have a different private spelling, so continuation must derive
// it from the private index instead of assuming both spellings are identical.
func resolveStoredOverrides(
	values []string,
	privatePaths []pathmodel.Path,
) (map[string]pathmodel.Path, map[string]pathmodel.Path, map[string]string, error) {
	privateExact := make(map[string]pathmodel.Path, len(privatePaths))
	privateCanonical := make(map[string][]pathmodel.Path, len(privatePaths))
	for _, path := range privatePaths {
		if _, found := privateExact[path.String()]; found {
			continue
		}
		privateExact[path.String()] = path
		key := pathmodel.Canonical(path, true)
		privateCanonical[key] = append(privateCanonical[key], path)
	}

	public := make(map[string]pathmodel.Path, len(values))
	private := make(map[string]pathmodel.Path, len(values))
	replacements := make(map[string]string, len(values))
	for _, value := range values {
		publicPath, err := pathmodel.Parse(value)
		if err != nil {
			return nil, nil, nil, err
		}
		replacement, found := privateExact[value]
		if !found {
			matches := privateCanonical[pathmodel.Canonical(publicPath, true)]
			if len(matches) != 1 {
				return nil, nil, nil, spaserr.Wrap(
					spaserr.KindUnsafeGitState,
					fmt.Errorf("cannot recover ownership override for public path %q: expected exactly one private replacement, found %d", publicPath, len(matches)),
				)
			}
			replacement = matches[0]
		}
		public[value] = publicPath
		private[replacement.String()] = replacement
		replacements[value] = replacement.String()
	}
	return public, private, replacements, nil
}

func groupByCanonical(paths []pathmodel.Path, ignoreCase bool) map[string][]pathmodel.Path {
	result := make(map[string][]pathmodel.Path, len(paths))
	for _, path := range paths {
		key := pathmodel.Canonical(path, ignoreCase)
		result[key] = append(result[key], path)
	}
	return result
}

func unionPaths(groups ...[]pathmodel.Path) []pathmodel.Path {
	set := make(map[string]pathmodel.Path)
	for _, group := range groups {
		for _, path := range group {
			set[path.String()] = path
		}
	}
	result := make([]pathmodel.Path, 0, len(set))
	for _, path := range set {
		result = append(result, path)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func unionSets(sets ...map[string]struct{}) map[string]struct{} {
	result := make(map[string]struct{})
	for _, set := range sets {
		for value := range set {
			result[value] = struct{}{}
		}
	}
	return result
}

func filterSkipped(paths []pathmodel.Path, skipped map[string]struct{}) []pathmodel.Path {
	result := make([]pathmodel.Path, 0, len(paths))
	for _, path := range paths {
		if _, found := skipped[path.String()]; !found {
			result = append(result, path)
		}
	}
	return result
}

func mapPaths(values map[string]struct{}) []pathmodel.Path {
	result := make([]pathmodel.Path, 0, len(values))
	for value := range values {
		result = append(result, pathmodel.Path(value))
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func mapPathValues(values map[string]pathmodel.Path) []pathmodel.Path {
	result := make([]pathmodel.Path, 0, len(values))
	for _, path := range values {
		result = append(result, path)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func retainStrings(values []string, keep map[string]struct{}) []string {
	var result []string
	for _, value := range values {
		if _, found := keep[value]; found {
			result = append(result, value)
		}
	}
	return result
}

func retainRemovals(values []linkstate.PendingRemoval, keep map[string]struct{}) []linkstate.PendingRemoval {
	var result []linkstate.PendingRemoval
	for _, value := range values {
		if _, found := keep[value.Path]; found {
			result = append(result, value)
		}
	}
	return result
}

func containsPath(paths []pathmodel.Path, target pathmodel.Path) bool {
	for _, path := range paths {
		if path == target {
			return true
		}
	}
	return false
}

func joinPaths(paths []pathmodel.Path) string {
	values := pathsToStrings(paths)
	return strings.Join(values, ", ")
}
