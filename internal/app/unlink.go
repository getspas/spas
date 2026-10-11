package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/getspas/spas/internal/collision"
	"github.com/getspas/spas/internal/linkstate"
	"github.com/getspas/spas/internal/pathmodel"
	"github.com/getspas/spas/internal/privategit"
	"github.com/getspas/spas/internal/publicgit"
	"github.com/getspas/spas/internal/spaserr"
)

func (a App) validateUnlinkOwnership(ctx context.Context, repository publicgit.Repository, paths []pathmodel.Path) error {
	tracked, err := repository.TrackedPaths(ctx)
	if err != nil {
		return err
	}
	ignoreCase, err := a.casePolicy(ctx, repository)
	if err != nil {
		return err
	}
	conflicts := collision.Detect(tracked, paths, ignoreCase)
	if len(conflicts) > 0 {
		messages := make([]string, 0, len(conflicts))
		for _, conflict := range conflicts {
			messages = append(messages, conflict.Error())
		}
		return spaserr.Wrap(spaserr.KindPathConflict, fmt.Errorf("cannot remove workspace files owned by project Git: %s; resolve ownership or unlink without --remove-files", strings.Join(messages, "; ")))
	}
	return nil
}

func (a App) inspectUnlinkFiles(ctx context.Context, root string, state linkstate.State, paths []pathmodel.Path) (map[string]fileSnapshot, []pathmodel.Path, error) {
	// ExpectedHead can contain local, unpushed work. Only the last completed
	// synchronization supplies the baseline for workspace-discard approval.
	private := a.privateRepository(state)
	baseline := map[pathmodel.Path]privategit.FileIdentity{}
	if state.LastSync.PrivateHead != "" {
		var err error
		baseline, err = private.TreeFiles(ctx, state.LastSync.PrivateHead)
		if err != nil {
			return nil, nil, err
		}
	}
	snapshots := make(map[string]fileSnapshot, len(paths))
	pending := stringSet(state.PendingAdds)
	if state.ActiveMerge != nil {
		for _, value := range state.ActiveMerge.RemainingPendingAdds {
			pending[value] = struct{}{}
		}
	}
	if state.Materializing != nil {
		for _, value := range state.Materializing.RemainingPendingAdds {
			pending[value] = struct{}{}
		}
	}
	var unsynced []pathmodel.Path
	for _, path := range paths {
		if err := pathmodel.ValidateNoSymlinkComponents(root, path); err != nil {
			return nil, nil, spaserr.Wrap(spaserr.KindUnsupportedPath, err)
		}
		snapshot, err := snapshotFile(path.OSPath(root))
		if err != nil {
			return nil, nil, fmt.Errorf("inspect unlink target %q: %w", path, err)
		}
		snapshots[path.String()] = snapshot
		if !snapshot.Existed {
			continue
		}
		// A pending enrollment has never synchronized as a managed workspace
		// asset, even if its bytes match a formerly skipped remote file.
		if _, isPending := pending[path.String()]; isPending {
			unsynced = append(unsynced, path)
			continue
		}
		identity, found := baseline[path]
		if !found || identity.Executable != snapshot.Executable {
			unsynced = append(unsynced, path)
			continue
		}
		digest, err := private.BlobDigest(ctx, identity.ObjectID)
		if err != nil {
			return nil, nil, err
		}
		if digest != snapshot.Digest {
			unsynced = append(unsynced, path)
		}
	}
	return snapshots, unsynced, nil
}

func verifyUnlinkSnapshots(root string, paths []pathmodel.Path, snapshots map[string]fileSnapshot) error {
	for _, path := range paths {
		if err := pathmodel.ValidateNoSymlinkComponents(root, path); err != nil {
			return spaserr.Wrap(spaserr.KindUnsupportedPath, err)
		}
	}
	return verifyPathSnapshots(root, paths, snapshots)
}
