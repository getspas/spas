package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/getspas/spas/internal/exclude"
	"github.com/getspas/spas/internal/jsonoutput"
	"github.com/getspas/spas/internal/linkstate"
	"github.com/getspas/spas/internal/pathmodel"
	"github.com/getspas/spas/internal/presentation"
	"github.com/getspas/spas/internal/provider"
)

func (a App) warn(warning jsonoutput.Warning) error {
	if a.Err == nil {
		return nil
	}
	if a.JSON {
		return jsonoutput.WriteWarning(a.Err, warning)
	}
	_, err := fmt.Fprintln(a.Err, a.Presenter.Err("warning:", presentation.Warning), warning.Message)
	return err
}

func (a App) warnPublicRepository(ctx context.Context, ref provider.RepositoryRef) error {
	a.Presenter.Phase("Checking repository visibility", -1)
	readable, err := a.Provider.ProbePublic(ctx, a.Git, ref)
	if err != nil {
		return err
	}
	if !readable {
		return nil
	}
	return a.warn(jsonoutput.Warning{
		Code:    "public_repository",
		Message: fmt.Sprintf("Repository %q is publicly readable. Anyone may read managed assets stored there.", ref.Canonical),
	})
}

func (a App) applyExclusions(plan exclude.Plan, expected ...[]pathmodel.Path) error {
	repaired, err := exclude.Apply(plan, expected...)
	if err != nil {
		return err
	}
	if !repaired {
		return nil
	}
	if err := a.warn(jsonoutput.Warning{
		Code:    "exclusion_drift_repaired",
		Message: fmt.Sprintf("Reconciled unexpected changes to the owned exclusion block in %q with link state. Edit rules outside the SPAS block and avoid concurrent writes. Rules without exact ownership markers remain external and can survive unlink.", plan.Path),
	}); err != nil {
		return errors.Join(err, exclude.Restore(plan))
	}
	return nil
}

// An interrupted operation can leave exclusions from either side of its
// journaled write. Both are expected; neither changes asset enrollment.
func expectedExclusions(state linkstate.State) [][]pathmodel.Path {
	expected := [][]pathmodel.Path{managedForExclusion(state, stringsToPaths(state.PendingAdds), nil)}
	if active := state.ActiveMerge; active != nil {
		skipped := stringSet(active.SkippedPaths)
		expected = append(expected,
			filterSkipped(unionPaths(stringsToPaths(state.ManagedPaths), stringsToPaths(active.RemainingPendingAdds), stringsToPaths(active.MaterializationPaths), stringsToPaths(active.ConflictPaths), stringsToPaths(active.DeferredPaths)), skipped),
			filterSkipped(unionPaths(stringsToPaths(state.ManagedPaths), stringsToPaths(active.RemainingPendingAdds), stringsToPaths(active.ConflictPaths)), skipped),
		)
	}
	if marker := state.Materializing; marker != nil {
		excluded := stringsToPaths(marker.ExcludedPaths)
		expected = append(expected,
			excluded,
			unionPaths(stringsToPaths(marker.PreviousPaths), excluded),
			filterSkipped(
				unionPaths(stringsToPaths(marker.PreviousPaths), stringsToPaths(state.PendingAdds), excluded),
				stringSet(marker.SkippedPaths),
			),
		)
	}
	return expected
}
