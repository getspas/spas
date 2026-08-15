package linkstate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/getspas/spas/internal/atomicfile"
	"github.com/getspas/spas/internal/pathmodel"
	"github.com/getspas/spas/internal/provider"
)

const SchemaVersion = 2

const (
	ClonePreparing = "preparing"
	ClonePrepared  = "prepared"

	MaterializationPushPending     = "push-pending"
	MaterializationPushed          = "pushed"
	MaterializationRemoteAdvanced  = "remote-advanced"
	MaterializationMergeContinuing = "merge-continuing"
	MaterializationMergeStaged     = "merge-staged"
)

type State struct {
	SchemaVersion  int              `json:"schemaVersion"`
	LinkID         string           `json:"linkId"`
	Public         Public           `json:"public"`
	Private        Private          `json:"private"`
	PendingAdds    []string         `json:"pendingAdds"`
	PendingRemoves []PendingRemoval `json:"pendingRemovals"`
	ManagedPaths   []string         `json:"managedPaths"`
	Exclude        Exclude          `json:"exclude"`
	Merge          Merge            `json:"mergeProtection"`
	ActiveMerge    *ActiveMerge     `json:"activeMerge,omitempty"`
	Materializing  *Materialization `json:"pendingMaterialization,omitempty"`
	LastSync       LastSync         `json:"lastSync"`
}

type Public struct {
	Root         string `json:"root"`
	GitCommonDir string `json:"gitCommonDir"`
}

type Private struct {
	Provider            provider.ID          `json:"provider"`
	Repository          string               `json:"repository"`
	Transport           provider.Transport   `json:"transport"`
	RemoteURL           string               `json:"remoteURL"`
	Branch              string               `json:"branch,omitempty"`
	LocalRepositoryPath string               `json:"localRepositoryPath"`
	Initialized         bool                 `json:"initialized"`
	ExpectedHead        string               `json:"expectedHead,omitempty"`
	Initialization      *CloneInitialization `json:"initialization,omitempty"`
	RemoteEmpty         bool                 `json:"remoteEmpty,omitempty"`
}

type CloneInitialization struct {
	Phase           string `json:"phase"`
	RequestedBranch string `json:"requestedBranch,omitempty"`
	Branch          string `json:"branch,omitempty"`
	Head            string `json:"head,omitempty"`
	RemoteEmpty     bool   `json:"remoteEmpty,omitempty"`
}

type Exclude struct {
	BlockID                 string `json:"blockId"`
	ExistingContentApproved bool   `json:"existingContentApproved"`
}

type Merge struct {
	ManagedBranches map[string]ManagedBranch `json:"managedBranches"`
}

type ManagedBranch struct {
	Before        string `json:"before"`
	BeforePresent bool   `json:"beforePresent,omitempty"`
	After         string `json:"after"`
}

type ActiveMerge struct {
	// ConflictFilesReady is set only after SPAS has saved every approved
	// recovery copy and materialized all private conflict files into the public
	// workspace. An interrupted preparation must be aborted rather than treated
	// as a developer-approved merge resolution.
	ConflictFilesReady bool `json:"conflictFilesReady"`
	// WorkspaceSnapshots bind every conflict and recovery path to its exact
	// workspace state before conflict materialization begins.
	WorkspaceSnapshots []WorkspaceSnapshot `json:"workspaceSnapshots,omitempty"`
	// MaterializationPaths and MaterializationSnapshots bind every workspace
	// destination that continuation or abort may later replace to its state
	// before the private merge began. Conflict and approved recovery paths keep
	// their separate editable/recovery contracts above.
	MaterializationPaths     []string            `json:"materializationPaths,omitempty"`
	MaterializationSnapshots []WorkspaceSnapshot `json:"materializationSnapshots,omitempty"`
	// OverrideStatuses bind each approved public ownership override to its
	// original Git porcelain status. Continuation compares both this status and
	// WorkspaceSnapshots so a later commit cannot make changed content appear
	// clean.
	OverrideStatuses []OverrideStatus `json:"overrideStatuses,omitempty"`
	// PreMergeHead records the exact local private commit restored by
	// git merge --abort. It lets an interrupted abort resume without trusting
	// arbitrary later movement of the SPAS-managed private branch.
	PreMergeHead string `json:"preMergeHead,omitempty"`
	// MergeHead records the exact fetched commit that Git is merging.
	MergeHead string `json:"mergeHead,omitempty"`
	// ConflictPaths records every private merge-conflict file copied into the
	// public workspace. It lets unlink remove or report those files even when
	// they were newly introduced by the remote and were never in ManagedPaths.
	ConflictPaths           []string         `json:"conflictPaths,omitempty"`
	SkippedPaths            []string         `json:"skippedPaths"`
	DeferredPaths           []string         `json:"deferredPaths,omitempty"`
	OverridePaths           []string         `json:"overridePaths"`
	RecoveryPaths           []string         `json:"recoveryPaths,omitempty"`
	RemainingPendingAdds    []string         `json:"remainingPendingAdds,omitempty"`
	RemainingPendingRemoves []PendingRemoval `json:"remainingPendingRemovals,omitempty"`
}

type OverrideStatus struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

// PendingRemoval records one explicitly requested private deletion together
// with the content identity of the workspace file at request time, so a later
// edit is never destroyed by the removal.
type PendingRemoval struct {
	Path string `json:"path"`
	// Digest is the hex SHA-256 of the workspace file when removal was
	// requested; empty when the file did not exist.
	Digest      string    `json:"digest,omitempty"`
	Existed     bool      `json:"existed"`
	Executable  bool      `json:"executable,omitempty"`
	RequestedAt time.Time `json:"requestedAt"`
}

// Materialization records a private result from merge-resolution commit
// through push and workspace materialization. The workspace must not be
// interpreted as new private edits until this state is completed.
type Materialization struct {
	Phase string `json:"phase"`
	// ResultPrivateHead normally names the exact result to push/materialize.
	// During merge-continuing and merge-staged it temporarily records the
	// required first parent; recovery replaces it with the completed merge
	// commit before pushing.
	ResultPrivateHead string `json:"resultPrivateHead"`
	// MergeHead and StagedTree cryptographically bind a verified
	// merge-resolution index to the merge commit that recovery may accept.
	// Merge-continuing state does not have this binding yet; merge-staged state
	// requires both values. Ordinary non-conflict materialization uses neither.
	MergeHead               string              `json:"mergeHead,omitempty"`
	StagedTree              string              `json:"stagedTree,omitempty"`
	PreviousPaths           []string            `json:"previousPaths"`
	FinalPaths              []string            `json:"finalPaths"`
	ExcludedPaths           []string            `json:"excludedPaths"`
	SkippedPaths            []string            `json:"skippedPaths,omitempty"`
	DeferredPaths           []string            `json:"deferredPaths,omitempty"`
	OverridePaths           []string            `json:"overridePaths,omitempty"`
	RecoveryPaths           []string            `json:"recoveryPaths,omitempty"`
	WorkspaceSnapshots      []WorkspaceSnapshot `json:"workspaceSnapshots,omitempty"`
	RemainingPendingAdds    []string            `json:"remainingPendingAdds,omitempty"`
	RemainingPendingRemoves []PendingRemoval    `json:"remainingPendingRemovals,omitempty"`
}

// WorkspaceSnapshot binds one workspace path to its exact state before a
// private result is pushed. Recovery accepts that state or the exact pushed
// private file and rejects later edits.
type WorkspaceSnapshot struct {
	Path       string `json:"path"`
	Digest     string `json:"digest,omitempty"`
	Existed    bool   `json:"existed"`
	Executable bool   `json:"executable,omitempty"`
}

type LastSync struct {
	PublicHead  string    `json:"publicHead,omitempty"`
	PrivateHead string    `json:"privateHead,omitempty"`
	RemoteHead  string    `json:"remoteHead,omitempty"`
	CompletedAt time.Time `json:"completedAt,omitempty"`
}

type Store struct {
	ConfigDir string
	DataDir   string
}

func New(publicRoot, commonDir string, ref provider.RepositoryRef, branch string, store Store) State {
	id := ID(publicRoot, commonDir)
	return State{
		SchemaVersion: SchemaVersion,
		LinkID:        id,
		Public: Public{
			Root:         publicRoot,
			GitCommonDir: commonDir,
		},
		Private: Private{
			Provider:            ref.Provider,
			Repository:          ref.Canonical,
			Transport:           ref.Transport,
			RemoteURL:           ref.RemoteURL,
			Branch:              branch,
			LocalRepositoryPath: privatePath(store.DataDir, id, ref.Canonical, ref.Transport),
		},
		Exclude: Exclude{BlockID: id},
		Merge:   Merge{ManagedBranches: map[string]ManagedBranch{}},
	}
}

func ID(publicRoot, commonDir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(publicRoot) + "\x00" + filepath.Clean(commonDir)))
	return "lnk_" + hex.EncodeToString(sum[:6])
}

// PendingRemovalPaths returns the removal paths in order.
func (s State) PendingRemovalPaths() []string {
	result := make([]string, 0, len(s.PendingRemoves))
	for _, removal := range s.PendingRemoves {
		result = append(result, removal.Path)
	}
	return result
}

func (s Store) Load(publicRoot, commonDir string) (State, error) {
	id := ID(publicRoot, commonDir)
	path := s.path(id)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return State{}, ErrNotLinked
		}
		return State{}, err
	}

	var state State
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return State{}, fmt.Errorf("decode link state: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return State{}, fmt.Errorf("decode link state: trailing JSON value")
		}
		return State{}, fmt.Errorf("decode link state: trailing data: %w", err)
	}
	if state.SchemaVersion != SchemaVersion {
		return State{}, fmt.Errorf("unsupported link-state schema %d", state.SchemaVersion)
	}
	if err := validate(state, s); err != nil {
		return State{}, err
	}
	if filepath.Clean(state.Public.Root) != filepath.Clean(publicRoot) ||
		filepath.Clean(state.Public.GitCommonDir) != filepath.Clean(commonDir) {
		return State{}, fmt.Errorf("link state does not match this public workspace")
	}
	return state, nil
}

func privatePath(dataDir, linkID, repository string, transport provider.Transport) string {
	sum := sha256.Sum256([]byte(repository + "\x00" + string(transport)))
	suffix := hex.EncodeToString(sum[:4])
	return filepath.Join(dataDir, "repos", linkID+"-"+suffix)
}

func (s Store) Save(state State) error {
	if state.SchemaVersion != SchemaVersion {
		return fmt.Errorf("refuse to save unsupported link-state schema %d", state.SchemaVersion)
	}
	if err := validate(state, s); err != nil {
		return err
	}
	sort.Strings(state.PendingAdds)
	sort.Slice(state.PendingRemoves, func(i, j int) bool {
		return state.PendingRemoves[i].Path < state.PendingRemoves[j].Path
	})
	sort.Strings(state.ManagedPaths)
	if state.ActiveMerge != nil {
		sort.Strings(state.ActiveMerge.ConflictPaths)
		sort.Strings(state.ActiveMerge.SkippedPaths)
		sort.Strings(state.ActiveMerge.DeferredPaths)
		sort.Strings(state.ActiveMerge.OverridePaths)
		sort.Strings(state.ActiveMerge.RecoveryPaths)
		sort.Strings(state.ActiveMerge.MaterializationPaths)
		sort.Slice(state.ActiveMerge.WorkspaceSnapshots, func(i, j int) bool {
			return state.ActiveMerge.WorkspaceSnapshots[i].Path <
				state.ActiveMerge.WorkspaceSnapshots[j].Path
		})
		sort.Slice(state.ActiveMerge.MaterializationSnapshots, func(i, j int) bool {
			return state.ActiveMerge.MaterializationSnapshots[i].Path <
				state.ActiveMerge.MaterializationSnapshots[j].Path
		})
		sort.Slice(state.ActiveMerge.OverrideStatuses, func(i, j int) bool {
			return state.ActiveMerge.OverrideStatuses[i].Path <
				state.ActiveMerge.OverrideStatuses[j].Path
		})
		sort.Strings(state.ActiveMerge.RemainingPendingAdds)
		sort.Slice(state.ActiveMerge.RemainingPendingRemoves, func(i, j int) bool {
			return state.ActiveMerge.RemainingPendingRemoves[i].Path < state.ActiveMerge.RemainingPendingRemoves[j].Path
		})
	}
	if state.Materializing != nil {
		sort.Strings(state.Materializing.PreviousPaths)
		sort.Strings(state.Materializing.FinalPaths)
		sort.Strings(state.Materializing.ExcludedPaths)
		sort.Strings(state.Materializing.SkippedPaths)
		sort.Strings(state.Materializing.DeferredPaths)
		sort.Strings(state.Materializing.OverridePaths)
		sort.Strings(state.Materializing.RecoveryPaths)
		sort.Slice(state.Materializing.WorkspaceSnapshots, func(i, j int) bool {
			return state.Materializing.WorkspaceSnapshots[i].Path <
				state.Materializing.WorkspaceSnapshots[j].Path
		})
		sort.Strings(state.Materializing.RemainingPendingAdds)
		sort.Slice(state.Materializing.RemainingPendingRemoves, func(i, j int) bool {
			return state.Materializing.RemainingPendingRemoves[i].Path <
				state.Materializing.RemainingPendingRemoves[j].Path
		})
	}

	dir := filepath.Join(s.ConfigDir, "links")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create link-state directory: %w", err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode link state: %w", err)
	}
	data = append(data, '\n')

	if err := atomicfile.Write(s.path(state.LinkID), data, 0o600); err != nil {
		return fmt.Errorf("replace link state: %w", err)
	}
	return nil
}

func (s Store) Delete(linkID string) error {
	err := os.Remove(s.path(linkID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s Store) path(linkID string) string {
	return filepath.Join(s.ConfigDir, "links", linkID+".json")
}

func validate(state State, store Store) error {
	if state.Merge.ManagedBranches == nil {
		return fmt.Errorf("link state is missing merge-protection managed branches")
	}
	expectedLinkID := ID(state.Public.Root, state.Public.GitCommonDir)
	if state.LinkID != expectedLinkID {
		return fmt.Errorf("link state contains an invalid link ID")
	}
	if state.Exclude.BlockID != state.LinkID {
		return fmt.Errorf("link state contains an invalid local-exclude block ID")
	}
	if state.Private.Provider == "" || state.Private.Repository == "" ||
		state.Private.Transport == "" || state.Private.RemoteURL == "" {
		return fmt.Errorf("link state contains an incomplete private repository identity")
	}
	expectedPrivatePath := privatePath(
		store.DataDir,
		state.LinkID,
		state.Private.Repository,
		state.Private.Transport,
	)
	if filepath.Clean(state.Private.LocalRepositoryPath) != filepath.Clean(expectedPrivatePath) {
		return fmt.Errorf("link state contains an unsupported private storage path")
	}
	if err := validatePrivateLifecycle(state.Private); err != nil {
		return err
	}
	if state.ActiveMerge != nil {
		if state.ActiveMerge.PreMergeHead != state.Private.ExpectedHead {
			return fmt.Errorf("link state active merge pre-merge commit %q does not match expected private HEAD %q", state.ActiveMerge.PreMergeHead, state.Private.ExpectedHead)
		}
		if err := validateOptionalObjectID("active merge pre-merge commit", state.ActiveMerge.PreMergeHead); err != nil {
			return err
		}
		if err := validateOptionalObjectID("active merge commit", state.ActiveMerge.MergeHead); err != nil {
			return err
		}
		for _, removal := range state.ActiveMerge.RemainingPendingRemoves {
			if err := validatePendingRemoval(removal); err != nil {
				return err
			}
		}
		if err := validateActiveMergeRecovery(state.ActiveMerge); err != nil {
			return err
		}
	}
	for _, removal := range state.PendingRemoves {
		if err := validatePendingRemoval(removal); err != nil {
			return err
		}
	}
	if err := validateOptionalObjectID("last public commit", state.LastSync.PublicHead); err != nil {
		return err
	}
	if err := validateOptionalObjectID("last private commit", state.LastSync.PrivateHead); err != nil {
		return err
	}
	if err := validateOptionalObjectID("last remote commit", state.LastSync.RemoteHead); err != nil {
		return err
	}
	groups := [][]string{state.PendingAdds, state.PendingRemovalPaths(), state.ManagedPaths}
	if state.ActiveMerge != nil {
		groups = append(
			groups,
			state.ActiveMerge.ConflictPaths,
			state.ActiveMerge.SkippedPaths,
			state.ActiveMerge.DeferredPaths,
			state.ActiveMerge.OverridePaths,
			state.ActiveMerge.RecoveryPaths,
			state.ActiveMerge.MaterializationPaths,
			state.ActiveMerge.RemainingPendingAdds,
		)
		for _, removal := range state.ActiveMerge.RemainingPendingRemoves {
			groups = append(groups, []string{removal.Path})
		}
	}
	if state.Materializing != nil {
		if state.Materializing.Phase != MaterializationPushPending &&
			state.Materializing.Phase != MaterializationPushed &&
			state.Materializing.Phase != MaterializationRemoteAdvanced &&
			state.Materializing.Phase != MaterializationMergeContinuing &&
			state.Materializing.Phase != MaterializationMergeStaged {
			return fmt.Errorf("link state contains invalid materialization phase %q", state.Materializing.Phase)
		}
		if state.Materializing.ResultPrivateHead == "" {
			return fmt.Errorf("link state contains materialization without a private result commit")
		}
		if err := validateObjectID("materialization result commit", state.Materializing.ResultPrivateHead); err != nil {
			return err
		}
		if (state.Materializing.MergeHead == "") != (state.Materializing.StagedTree == "") {
			return fmt.Errorf("link state contains an incomplete verified merge-resolution binding")
		}
		if state.Materializing.Phase == MaterializationMergeStaged &&
			state.Materializing.MergeHead == "" {
			return fmt.Errorf("link state contains merge-staged materialization without a verified merge-resolution binding")
		}
		if err := validateOptionalObjectID("materialization merge commit", state.Materializing.MergeHead); err != nil {
			return err
		}
		if err := validateOptionalObjectID("materialization staged tree", state.Materializing.StagedTree); err != nil {
			return err
		}
		for _, removal := range state.Materializing.RemainingPendingRemoves {
			if err := validatePendingRemoval(removal); err != nil {
				return err
			}
		}
		if err := validateMaterializationSnapshots(state.Materializing); err != nil {
			return err
		}
		groups = append(
			groups,
			state.Materializing.PreviousPaths,
			state.Materializing.FinalPaths,
			state.Materializing.ExcludedPaths,
			state.Materializing.SkippedPaths,
			state.Materializing.DeferredPaths,
			state.Materializing.OverridePaths,
			state.Materializing.RecoveryPaths,
			state.Materializing.RemainingPendingAdds,
		)
		for _, removal := range state.Materializing.RemainingPendingRemoves {
			groups = append(groups, []string{removal.Path})
		}
	}
	for _, group := range groups {
		for _, value := range group {
			if _, err := pathmodel.Parse(value); err != nil {
				return fmt.Errorf("link state contains invalid managed path %q: %w", value, err)
			}
		}
	}
	return nil
}

func validatePrivateLifecycle(private Private) error {
	if private.Initialized {
		if private.Initialization != nil {
			return fmt.Errorf("link state contains initialization and initialized private state")
		}
		if private.Branch == "" {
			return fmt.Errorf("initialized private state is missing its branch")
		}
		if private.RemoteEmpty {
			if private.ExpectedHead != "" {
				return fmt.Errorf("empty private state must not contain an expected private HEAD")
			}
			return nil
		}
		if private.ExpectedHead == "" {
			return fmt.Errorf("initialized private state is missing its expected private HEAD")
		}
		return validateObjectID("expected private HEAD", private.ExpectedHead)
	}
	if private.ExpectedHead != "" {
		return fmt.Errorf("uninitialized private state must not contain an expected private HEAD")
	}
	if private.RemoteEmpty {
		return fmt.Errorf("uninitialized private state must not be marked remote-empty")
	}
	if private.Initialization == nil {
		return nil
	}

	initialization := private.Initialization
	switch initialization.Phase {
	case ClonePreparing:
		if initialization.Branch != "" || initialization.Head != "" || initialization.RemoteEmpty {
			return fmt.Errorf("preparing initialization contains finalized clone data")
		}
	case ClonePrepared:
		if initialization.Branch == "" {
			return fmt.Errorf("prepared initialization is missing its branch")
		}
		if initialization.RemoteEmpty {
			if initialization.Head != "" {
				return fmt.Errorf("prepared initialization for an empty remote must not contain a head")
			}
		} else if err := validateObjectID("prepared initialization HEAD", initialization.Head); err != nil {
			return err
		}
	default:
		return fmt.Errorf("link state contains invalid clone initialization phase %q", initialization.Phase)
	}
	return nil
}

func validateActiveMergeRecovery(active *ActiveMerge) error {
	if active.PreMergeHead == "" {
		return fmt.Errorf("link state contains active merge without its pre-merge commit")
	}
	if active.MergeHead == "" {
		return fmt.Errorf("link state contains active merge without its merge commit")
	}
	if len(active.ConflictPaths) == 0 {
		if active.ConflictFilesReady {
			return fmt.Errorf("link state contains abort-only merge with ready conflict files")
		}
		if len(active.WorkspaceSnapshots) != 0 ||
			len(active.MaterializationPaths) != 0 ||
			len(active.MaterializationSnapshots) != 0 ||
			len(active.OverrideStatuses) != 0 ||
			len(active.SkippedPaths) != 0 ||
			len(active.DeferredPaths) != 0 ||
			len(active.OverridePaths) != 0 ||
			len(active.RecoveryPaths) != 0 ||
			len(active.RemainingPendingAdds) != 0 ||
			len(active.RemainingPendingRemoves) != 0 {
			return fmt.Errorf("link state contains abort-only merge with continuation metadata")
		}
		return nil
	}
	recovery := make(map[string]struct{}, len(active.RecoveryPaths))
	for _, path := range active.RecoveryPaths {
		if _, duplicate := recovery[path]; duplicate {
			return fmt.Errorf("link state contains duplicate active-merge recovery path %q", path)
		}
		recovery[path] = struct{}{}
	}
	for _, path := range active.OverridePaths {
		if _, ok := recovery[path]; !ok {
			return fmt.Errorf("link state active-merge override %q is missing its recovery path", path)
		}
	}
	overridePaths := make(map[string]struct{}, len(active.OverridePaths))
	for _, path := range active.OverridePaths {
		overridePaths[path] = struct{}{}
	}
	statuses := make(map[string]struct{}, len(active.OverrideStatuses))
	for _, status := range active.OverrideStatuses {
		if _, err := pathmodel.Parse(status.Path); err != nil {
			return fmt.Errorf("link state contains invalid active-merge override-status path %q: %w", status.Path, err)
		}
		if _, duplicate := statuses[status.Path]; duplicate {
			return fmt.Errorf("link state contains duplicate active-merge override status for %q", status.Path)
		}
		statuses[status.Path] = struct{}{}
		if _, expected := overridePaths[status.Path]; !expected {
			return fmt.Errorf("link state contains active-merge override status for unapproved path %q", status.Path)
		}
	}
	for path := range overridePaths {
		if _, found := statuses[path]; !found {
			return fmt.Errorf("link state active-merge override %q is missing its original public status", path)
		}
	}
	expected := make(map[string]struct{}, len(active.ConflictPaths)+len(active.RecoveryPaths))
	for _, path := range active.ConflictPaths {
		expected[path] = struct{}{}
	}
	for _, path := range active.RecoveryPaths {
		expected[path] = struct{}{}
	}
	if err := validateWorkspaceSnapshots("active-merge", active.WorkspaceSnapshots, expected); err != nil {
		return err
	}

	materializationPaths := make(map[string]struct{}, len(active.MaterializationPaths))
	for _, path := range active.MaterializationPaths {
		if _, duplicate := materializationPaths[path]; duplicate {
			return fmt.Errorf("link state contains duplicate active-merge materialization path %q", path)
		}
		materializationPaths[path] = struct{}{}
	}
	if len(materializationPaths) == 0 {
		return fmt.Errorf("link state contains active merge without materialization paths")
	}
	for _, path := range active.ConflictPaths {
		if _, found := materializationPaths[path]; !found {
			return fmt.Errorf("link state active-merge conflict %q is missing from its materialization plan", path)
		}
	}
	return validateWorkspaceSnapshots(
		"active-merge materialization",
		active.MaterializationSnapshots,
		materializationPaths,
	)
}

func validateMaterializationSnapshots(materialization *Materialization) error {
	skipped := make(map[string]struct{}, len(materialization.SkippedPaths)+len(materialization.DeferredPaths))
	for _, path := range materialization.SkippedPaths {
		skipped[path] = struct{}{}
	}
	for _, path := range materialization.DeferredPaths {
		skipped[path] = struct{}{}
	}
	expected := make(map[string]struct{})
	for _, path := range append(
		append(
			append([]string{}, materialization.PreviousPaths...),
			materialization.FinalPaths...,
		),
		materialization.RecoveryPaths...,
	) {
		if _, blocked := skipped[path]; !blocked {
			expected[path] = struct{}{}
		}
	}
	if err := validateWorkspaceSnapshots("materialization", materialization.WorkspaceSnapshots, expected); err != nil {
		return err
	}

	recovery := make(map[string]struct{}, len(materialization.RecoveryPaths))
	for _, path := range materialization.RecoveryPaths {
		if _, duplicate := recovery[path]; duplicate {
			return fmt.Errorf("link state contains duplicate materialization recovery path %q", path)
		}
		recovery[path] = struct{}{}
	}
	for _, path := range materialization.OverridePaths {
		if _, ok := recovery[path]; !ok {
			return fmt.Errorf("link state materialization override %q is missing its recovery path", path)
		}
	}
	return nil
}

func validateWorkspaceSnapshots(kind string, snapshots []WorkspaceSnapshot, expected map[string]struct{}) error {
	found := make(map[string]struct{}, len(snapshots))
	for _, snapshot := range snapshots {
		if _, err := pathmodel.Parse(snapshot.Path); err != nil {
			return fmt.Errorf("link state contains invalid %s snapshot path %q: %w", kind, snapshot.Path, err)
		}
		if _, duplicate := found[snapshot.Path]; duplicate {
			return fmt.Errorf("link state contains duplicate %s snapshot for %q", kind, snapshot.Path)
		}
		found[snapshot.Path] = struct{}{}
		if snapshot.Existed {
			if len(snapshot.Digest) != sha256.Size*2 {
				return fmt.Errorf("link state contains an invalid %s digest for %q", kind, snapshot.Path)
			}
			if _, err := hex.DecodeString(snapshot.Digest); err != nil {
				return fmt.Errorf("link state contains an invalid %s digest for %q", kind, snapshot.Path)
			}
		} else if snapshot.Digest != "" || snapshot.Executable {
			return fmt.Errorf("link state contains impossible absent %s snapshot for %q", kind, snapshot.Path)
		}
		if _, required := expected[snapshot.Path]; !required {
			return fmt.Errorf("link state contains unexpected %s snapshot for %q", kind, snapshot.Path)
		}
	}
	for path := range expected {
		if _, ok := found[path]; !ok {
			return fmt.Errorf("link state is missing %s snapshot for %q", kind, path)
		}
	}
	return nil
}

func validatePendingRemoval(removal PendingRemoval) error {
	if !removal.Existed {
		if removal.Digest != "" || removal.Executable {
			return fmt.Errorf("link state contains impossible absent pending-removal snapshot for %q", removal.Path)
		}
		return nil
	}
	if len(removal.Digest) != sha256.Size*2 {
		return fmt.Errorf("link state contains an invalid pending-removal digest for %q", removal.Path)
	}
	if _, err := hex.DecodeString(removal.Digest); err != nil {
		return fmt.Errorf("link state contains an invalid pending-removal digest for %q", removal.Path)
	}
	return nil
}

func validateOptionalObjectID(name, value string) error {
	if value == "" {
		return nil
	}
	return validateObjectID(name, value)
}

// validateObjectID accepts the full SHA-1 and SHA-256 object formats that Git
// repositories can persist. Recovery state must never contain abbreviated or
// revision-expression input because these values are later passed back to Git.
func validateObjectID(name, value string) error {
	if len(value) != 40 && len(value) != 64 {
		return fmt.Errorf("link state contains an invalid %s object ID", name)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("link state contains an invalid %s object ID", name)
	}
	return nil
}

var ErrNotLinked = errors.New("public workspace is not linked")
