package linkstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/getspas/spas/internal/githubref"
	"github.com/getspas/spas/internal/provider"
)

func testRepositoryRef() provider.RepositoryRef {
	return provider.RepositoryRef{
		Provider:  githubref.ID,
		Canonical: "getspas/private",
		Transport: provider.SSH,
		RemoteURL: "git@github.com:getspas/private.git",
	}
}

func validActiveMerge(conflictPaths, recoveryPaths, overridePaths []string) ActiveMerge {
	paths := append(append([]string{}, conflictPaths...), recoveryPaths...)
	seen := map[string]struct{}{}
	snapshots := make([]WorkspaceSnapshot, 0, len(paths))
	for _, path := range paths {
		if _, duplicate := seen[path]; duplicate {
			continue
		}
		seen[path] = struct{}{}
		snapshots = append(snapshots, WorkspaceSnapshot{Path: path})
	}
	materializationPaths := append([]string{}, paths...)
	materializationSnapshots := append([]WorkspaceSnapshot{}, snapshots...)
	statuses := make([]OverrideStatus, 0, len(overridePaths))
	for _, path := range overridePaths {
		statuses = append(statuses, OverrideStatus{Path: path})
	}
	return ActiveMerge{
		ConflictFilesReady:       true,
		WorkspaceSnapshots:       snapshots,
		MaterializationPaths:     materializationPaths,
		MaterializationSnapshots: materializationSnapshots,
		OverrideStatuses:         statuses,
		PreMergeHead:             strings.Repeat("a", 40),
		MergeHead:                strings.Repeat("b", 40),
		ConflictPaths:            conflictPaths,
		RecoveryPaths:            recoveryPaths,
		OverridePaths:            overridePaths,
	}
}

func TestSaveLoad(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := Store{
		ConfigDir: filepath.Join(root, "config"),
		DataDir:   filepath.Join(root, "data"),
	}
	state := New(
		filepath.Join(root, "public"),
		filepath.Join(root, "public", ".git"),
		testRepositoryRef(),
		"main",
		store,
	)
	state.PendingAdds = []string{"z", "a"}

	if err := store.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := store.Load(state.Public.Root, state.Public.GitCommonDir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.LinkID != state.LinkID {
		t.Fatalf("Load().LinkID = %q, want %q", got.LinkID, state.LinkID)
	}
	if len(got.PendingAdds) != 2 || got.PendingAdds[0] != "a" {
		t.Fatalf("Load().PendingAdds = %v", got.PendingAdds)
	}
}

func TestSaveLoadAcceptsAbortOnlyMergeRecovery(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := Store{ConfigDir: filepath.Join(root, "config"), DataDir: filepath.Join(root, "data")}
	state := New(
		filepath.Join(root, "public"),
		filepath.Join(root, "public", ".git"),
		testRepositoryRef(),
		"main",
		store,
	)
	state.Private.Initialized = true
	state.Private.ExpectedHead = strings.Repeat("a", 40)
	state.ActiveMerge = &ActiveMerge{
		PreMergeHead: strings.Repeat("a", 40),
		MergeHead:    strings.Repeat("b", 40),
	}
	if err := store.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := store.Load(state.Public.Root, state.Public.GitCommonDir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.ActiveMerge == nil || len(loaded.ActiveMerge.ConflictPaths) != 0 ||
		loaded.ActiveMerge.ConflictFilesReady {
		t.Fatalf("Load().ActiveMerge = %#v, want abort-only marker", loaded.ActiveMerge)
	}
}

func TestInitializedStateRoundTripsAuthoritativePrivateHead(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := Store{ConfigDir: filepath.Join(root, "config"), DataDir: filepath.Join(root, "data")}
	state := New(
		filepath.Join(root, "public"),
		filepath.Join(root, "public", ".git"),
		testRepositoryRef(),
		"main",
		store,
	)
	state.Private.Initialized = true
	state.Private.ExpectedHead = strings.Repeat("a", 40)

	if err := store.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := store.Load(state.Public.Root, state.Public.GitCommonDir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !loaded.Private.Initialized || loaded.Private.ExpectedHead != state.Private.ExpectedHead || loaded.Private.Initialization != nil {
		t.Fatalf("loaded private lifecycle = %+v", loaded.Private)
	}
}

func TestLinkStateLifecycleValidation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := Store{ConfigDir: filepath.Join(root, "config"), DataDir: filepath.Join(root, "data")}
	newState := func() State {
		return New(
			filepath.Join(root, "public"),
			filepath.Join(root, "public", ".git"),
			testRepositoryRef(),
			"main",
			store,
		)
	}
	validHead := strings.Repeat("a", 40)
	for _, test := range []struct {
		name   string
		mutate func(*State)
		want   string
	}{
		{
			name: "initialized state requires expected head",
			mutate: func(state *State) {
				state.Private.Initialized = true
			},
			want: "expected private HEAD",
		},
		{
			name: "initialized empty branch permits unborn head",
			mutate: func(state *State) {
				state.Private.Initialized = true
				state.Private.RemoteEmpty = true
			},
		},
		{
			name: "initialization and initialized state cannot coexist",
			mutate: func(state *State) {
				state.Private.Initialized = true
				state.Private.ExpectedHead = validHead
				state.Private.Initialization = &CloneInitialization{Phase: ClonePreparing}
			},
			want: "initialization and initialized",
		},
		{
			name: "prepared initialization requires branch and head",
			mutate: func(state *State) {
				state.Private.Initialization = &CloneInitialization{Phase: ClonePrepared}
			},
			want: "prepared initialization",
		},
		{
			name: "active merge must bind expected head",
			mutate: func(state *State) {
				state.Private.Initialized = true
				state.Private.ExpectedHead = validHead
				active := validActiveMerge([]string{"conflict.txt"}, nil, nil)
				active.PreMergeHead = strings.Repeat("b", 40)
				state.ActiveMerge = &active
			},
			want: "pre-merge commit",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			state := newState()
			test.mutate(&state)
			err := store.Save(state)
			if test.want == "" {
				if err != nil {
					t.Fatalf("Save() error = %v, want success", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Save() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSaveRejectsTamperedLinkID(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := Store{
		ConfigDir: filepath.Join(root, "config"),
		DataDir:   filepath.Join(root, "data"),
	}
	state := New(
		filepath.Join(root, "public"),
		filepath.Join(root, "public", ".git"),
		testRepositoryRef(),
		"main",
		store,
	)
	state.LinkID = "../../outside"
	state.Exclude.BlockID = state.LinkID

	if err := store.Save(state); err == nil {
		t.Fatal("Save() error = nil, want tampered link ID rejection")
	}
}

func TestLoadRejectsTamperedSchemaPrivatePathsAndUnknownFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		mutate      func(map[string]any, State)
		wantInError string
	}{
		{
			name: "old schema",
			mutate: func(document map[string]any, _ State) {
				document["schemaVersion"] = float64(1)
			},
			wantInError: "unsupported link-state schema",
		},
		{
			name: "future schema",
			mutate: func(document map[string]any, _ State) {
				document["schemaVersion"] = float64(SchemaVersion + 1)
			},
			wantInError: "unsupported link-state schema",
		},
		{
			name: "missing or zero schema",
			mutate: func(document map[string]any, _ State) {
				document["schemaVersion"] = float64(0)
			},
			wantInError: "unsupported link-state schema",
		},
		{
			name: "private clone path",
			mutate: func(document map[string]any, state State) {
				private := document["private"].(map[string]any)
				private["localRepositoryPath"] = filepath.Join(filepath.Dir(state.Private.LocalRepositoryPath), "outside")
			},
			wantInError: "unsupported private storage path",
		},
		{
			name: "managed traversal",
			mutate: func(document map[string]any, _ State) {
				document["managedPaths"] = []any{"../outside"}
			},
			wantInError: "invalid managed path",
		},
		{
			name: "removed storage field",
			mutate: func(document map[string]any, _ State) {
				document["storage"] = map[string]any{"mode": "default"}
			},
			wantInError: "unknown field",
		},
		{
			name: "unknown field",
			mutate: func(document map[string]any, _ State) {
				document["surprise"] = true
			},
			wantInError: "unknown field",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			store := Store{
				ConfigDir: filepath.Join(root, "config"),
				DataDir:   filepath.Join(root, "data"),
			}
			state := New(
				filepath.Join(root, "public"),
				filepath.Join(root, "public", ".git"),
				testRepositoryRef(),
				"main",
				store,
			)
			if err := store.Save(state); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(store.path(state.LinkID))
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err := json.Unmarshal(data, &document); err != nil {
				t.Fatal(err)
			}
			test.mutate(document, state)
			tampered, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(store.path(state.LinkID), tampered, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = store.Load(state.Public.Root, state.Public.GitCommonDir)
			if err == nil || !strings.Contains(err.Error(), test.wantInError) {
				t.Fatalf("Load() error = %v, want %q", err, test.wantInError)
			}
		})
	}
}

func TestLoadRejectsMissingMergeProtectionFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "missing merge protection",
			mutate: func(document map[string]any) {
				delete(document, "mergeProtection")
			},
		},
		{
			name: "missing managed branches",
			mutate: func(document map[string]any) {
				mergeProtection, ok := document["mergeProtection"].(map[string]any)
				if !ok {
					panic("mergeProtection is not an object")
				}
				delete(mergeProtection, "managedBranches")
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			store := Store{ConfigDir: filepath.Join(root, "config"), DataDir: filepath.Join(root, "data")}
			state := New(
				filepath.Join(root, "public"),
				filepath.Join(root, "public", ".git"),
				testRepositoryRef(),
				"main",
				store,
			)
			if err := store.Save(state); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(store.path(state.LinkID))
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err := json.Unmarshal(data, &document); err != nil {
				t.Fatal(err)
			}
			test.mutate(document)
			tampered, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(store.path(state.LinkID), tampered, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load(state.Public.Root, state.Public.GitCommonDir); err == nil {
				t.Fatal("Load() error = nil, want missing merge protection field rejection")
			}
		})
	}
}

func TestLoadRejectsDifferentPublicWorkspace(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := Store{
		ConfigDir: filepath.Join(root, "config"),
		DataDir:   filepath.Join(root, "data"),
	}
	state := New(
		filepath.Join(root, "public"),
		filepath.Join(root, "public", ".git"),
		testRepositoryRef(),
		"main",
		store,
	)
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(filepath.Join(root, "different"), state.Public.GitCommonDir); err == nil {
		t.Fatal("Load() error = nil, want different-workspace rejection")
	}
}

func TestLoadRejectsTrailingJSON(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := Store{ConfigDir: filepath.Join(root, "config"), DataDir: filepath.Join(root, "data")}
	state := New(
		filepath.Join(root, "public"),
		filepath.Join(root, "public", ".git"),
		testRepositoryRef(),
		"main",
		store,
	)
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	path := store.path(state.LinkID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte(`{"unexpected":true}`)...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(state.Public.Root, state.Public.GitCommonDir); err == nil || !strings.Contains(err.Error(), "trailing JSON") {
		t.Fatalf("Load() error = %v, want trailing JSON rejection", err)
	}
}

func TestSaveValidatesEveryActiveMergePath(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := Store{ConfigDir: filepath.Join(root, "config"), DataDir: filepath.Join(root, "data")}
	base := New(
		filepath.Join(root, "public"),
		filepath.Join(root, "public", ".git"),
		testRepositoryRef(),
		"main",
		store,
	)
	base.Private.Initialized = true
	base.Private.ExpectedHead = strings.Repeat("a", 40)
	tests := []struct {
		name   string
		mutate func(*ActiveMerge)
		want   string
	}{
		{"conflict path", func(active *ActiveMerge) {
			active.ConflictPaths = []string{"../outside"}
			active.WorkspaceSnapshots[0].Path = "../outside"
		}, "invalid active-merge snapshot path"},
		{"deferred path", func(active *ActiveMerge) { active.DeferredPaths = []string{"../outside"} }, "invalid managed path"},
		{"recovery path", func(active *ActiveMerge) {
			active.RecoveryPaths = []string{"../outside"}
			active.WorkspaceSnapshots[1].Path = "../outside"
		}, "invalid active-merge snapshot path"},
		{"materialization path", func(active *ActiveMerge) {
			active.MaterializationPaths = append(active.MaterializationPaths, "../outside")
			active.MaterializationSnapshots = append(
				active.MaterializationSnapshots,
				WorkspaceSnapshot{Path: "../outside"},
			)
		}, "invalid active-merge materialization snapshot path"},
		{"remaining add", func(active *ActiveMerge) { active.RemainingPendingAdds = []string{"../outside"} }, "invalid managed path"},
		{"remaining removal", func(active *ActiveMerge) {
			active.RemainingPendingRemoves = []PendingRemoval{{Path: "../outside"}}
		}, "invalid managed path"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			state := base
			active := validActiveMerge([]string{"conflict.txt"}, []string{"recovery.txt"}, nil)
			state.ActiveMerge = &active
			test.mutate(state.ActiveMerge)
			if err := store.Save(state); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Save() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSaveRejectsInvalidActiveMergeRecoveryContract(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := Store{ConfigDir: filepath.Join(root, "config"), DataDir: filepath.Join(root, "data")}
	base := New(
		filepath.Join(root, "public"),
		filepath.Join(root, "public", ".git"),
		testRepositoryRef(),
		"main",
		store,
	)
	base.Private.Initialized = true
	base.Private.ExpectedHead = strings.Repeat("a", 40)
	tests := []struct {
		name   string
		active ActiveMerge
		want   string
	}{
		{
			name: "duplicate recovery path",
			active: func() ActiveMerge {
				active := validActiveMerge([]string{"conflict.txt"}, []string{".env", ".env"}, nil)
				return active
			}(),
			want: "duplicate active-merge recovery path",
		},
		{
			name: "override without recovery path",
			active: func() ActiveMerge {
				active := validActiveMerge([]string{"conflict.txt"}, nil, []string{".env"})
				return active
			}(),
			want: "missing its recovery path",
		},
		{
			name: "override without original status",
			active: func() ActiveMerge {
				active := validActiveMerge([]string{"conflict.txt"}, []string{".env"}, []string{".env"})
				active.OverrideStatuses = nil
				return active
			}(),
			want: "missing its original public status",
		},
		{
			name: "duplicate override status",
			active: func() ActiveMerge {
				active := validActiveMerge([]string{"conflict.txt"}, []string{".env"}, []string{".env"})
				active.OverrideStatuses = append(active.OverrideStatuses, active.OverrideStatuses[0])
				return active
			}(),
			want: "duplicate active-merge override status",
		},
		{
			name: "missing materialization plan",
			active: func() ActiveMerge {
				active := validActiveMerge([]string{"conflict.txt"}, nil, nil)
				active.MaterializationPaths = nil
				active.MaterializationSnapshots = nil
				return active
			}(),
			want: "without materialization paths",
		},
		{
			name: "duplicate materialization path",
			active: func() ActiveMerge {
				active := validActiveMerge([]string{"conflict.txt"}, nil, nil)
				active.MaterializationPaths = append(active.MaterializationPaths, active.MaterializationPaths[0])
				return active
			}(),
			want: "duplicate active-merge materialization path",
		},
		{
			name: "missing materialization snapshot",
			active: func() ActiveMerge {
				active := validActiveMerge([]string{"conflict.txt"}, nil, nil)
				active.MaterializationSnapshots = nil
				return active
			}(),
			want: "missing active-merge materialization snapshot",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			state := base
			state.ActiveMerge = &test.active
			if err := store.Save(state); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Save() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSaveAcceptsVerifiedMergeStagingState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := Store{ConfigDir: filepath.Join(root, "config"), DataDir: filepath.Join(root, "data")}
	state := New(
		filepath.Join(root, "public"),
		filepath.Join(root, "public", ".git"),
		testRepositoryRef(),
		"main",
		store,
	)
	state.Materializing = &Materialization{
		Phase:             MaterializationMergeStaged,
		ResultPrivateHead: strings.Repeat("a", 40),
		MergeHead:         strings.Repeat("b", 40),
		StagedTree:        strings.Repeat("c", 40),
	}
	if err := store.Save(state); err != nil {
		t.Fatalf("Save() error = %v, want merge-staged state accepted", err)
	}
}

func TestSaveRejectsIncompleteMergeStagingBinding(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := Store{ConfigDir: filepath.Join(root, "config"), DataDir: filepath.Join(root, "data")}
	state := New(
		filepath.Join(root, "public"),
		filepath.Join(root, "public", ".git"),
		testRepositoryRef(),
		"main",
		store,
	)
	tests := []struct {
		name          string
		materializing Materialization
		want          string
	}{
		{
			name: "one identifier missing",
			materializing: Materialization{
				Phase:             MaterializationMergeStaged,
				ResultPrivateHead: strings.Repeat("a", 40),
				MergeHead:         strings.Repeat("b", 40),
			},
			want: "incomplete verified merge-resolution binding",
		},
		{
			name: "both identifiers missing",
			materializing: Materialization{
				Phase:             MaterializationMergeStaged,
				ResultPrivateHead: strings.Repeat("a", 40),
			},
			want: "without a verified merge-resolution binding",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			state := state
			state.Materializing = &test.materializing
			if err := store.Save(state); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Save() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSaveRejectsMalformedRecoveryIdentifiers(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := Store{ConfigDir: filepath.Join(root, "config"), DataDir: filepath.Join(root, "data")}
	newState := func() State {
		return New(
			filepath.Join(root, "public"),
			filepath.Join(root, "public", ".git"),
			testRepositoryRef(),
			"main",
			store,
		)
	}
	tests := []struct {
		name   string
		mutate func(*State)
	}{
		{
			name: "active merge revision expression",
			mutate: func(state *State) {
				active := validActiveMerge([]string{"conflict.txt"}, nil, nil)
				active.PreMergeHead = "HEAD~1"
				state.ActiveMerge = &active
			},
		},
		{
			name: "materialization option-shaped commit",
			mutate: func(state *State) {
				state.Materializing = &Materialization{
					Phase:             MaterializationPushPending,
					ResultPrivateHead: "--hard",
				}
			},
		},
		{
			name: "materialization invalid merge head",
			mutate: func(state *State) {
				state.Materializing = &Materialization{
					Phase:             MaterializationMergeStaged,
					ResultPrivateHead: strings.Repeat("a", 40),
					MergeHead:         strings.Repeat("z", 40),
					StagedTree:        strings.Repeat("c", 40),
				}
			},
		},
		{
			name: "last sync abbreviated object",
			mutate: func(state *State) {
				state.LastSync.PrivateHead = "deadbeef"
			},
		},
		{
			name: "pending removal invalid digest",
			mutate: func(state *State) {
				state.PendingRemoves = []PendingRemoval{{Path: ".env", Digest: strings.Repeat("g", 64)}}
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			state := newState()
			test.mutate(&state)
			if err := store.Save(state); err == nil {
				t.Fatal("Save() error = nil, want malformed recovery identifier rejection")
			}
		})
	}
}

func TestSaveAcceptsSHA256ObjectIdentifiers(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := Store{ConfigDir: filepath.Join(root, "config"), DataDir: filepath.Join(root, "data")}
	state := New(
		filepath.Join(root, "public"),
		filepath.Join(root, "public", ".git"),
		testRepositoryRef(),
		"main",
		store,
	)
	state.Private.Initialized = true
	state.Private.ExpectedHead = strings.Repeat("a", 64)
	active := validActiveMerge([]string{"conflict.txt"}, nil, nil)
	active.PreMergeHead = strings.Repeat("a", 64)
	state.ActiveMerge = &active
	state.LastSync = LastSync{
		PublicHead:  strings.Repeat("b", 64),
		PrivateHead: strings.Repeat("c", 64),
		RemoteHead:  strings.Repeat("d", 64),
	}
	if err := store.Save(state); err != nil {
		t.Fatalf("Save() error = %v, want SHA-256 object IDs accepted", err)
	}
}

func TestSaveLoadPreservesMaterializationWorkspaceContract(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := Store{ConfigDir: filepath.Join(root, "config"), DataDir: filepath.Join(root, "data")}
	state := New(
		filepath.Join(root, "public"),
		filepath.Join(root, "public", ".git"),
		testRepositoryRef(),
		"main",
		store,
	)
	state.Materializing = &Materialization{
		Phase:             MaterializationPushed,
		ResultPrivateHead: strings.Repeat("a", 40),
		PreviousPaths:     []string{".env"},
		FinalPaths:        []string{".env"},
		ExcludedPaths:     []string{".env"},
		OverridePaths:     []string{".env"},
		RecoveryPaths:     []string{".env"},
		WorkspaceSnapshots: []WorkspaceSnapshot{{
			Path:       ".env",
			Digest:     strings.Repeat("b", 64),
			Existed:    true,
			Executable: true,
		}},
	}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(state.Public.Root, state.Public.GitCommonDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Materializing.WorkspaceSnapshots) != 1 ||
		!loaded.Materializing.WorkspaceSnapshots[0].Executable ||
		loaded.Materializing.WorkspaceSnapshots[0].Digest != strings.Repeat("b", 64) {
		t.Fatalf("loaded workspace snapshots = %+v", loaded.Materializing.WorkspaceSnapshots)
	}
	if !slices.Equal(loaded.Materializing.RecoveryPaths, []string{".env"}) {
		t.Fatalf("loaded recovery paths = %v", loaded.Materializing.RecoveryPaths)
	}
}

func TestSaveRejectsInvalidMaterializationWorkspaceContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Materialization)
	}{
		{
			name: "missing snapshot",
			mutate: func(materialization *Materialization) {
				materialization.WorkspaceSnapshots = nil
			},
		},
		{
			name: "invalid digest",
			mutate: func(materialization *Materialization) {
				materialization.WorkspaceSnapshots[0].Digest = strings.Repeat("g", 64)
			},
		},
		{
			name: "duplicate snapshot",
			mutate: func(materialization *Materialization) {
				materialization.WorkspaceSnapshots = append(
					materialization.WorkspaceSnapshots,
					materialization.WorkspaceSnapshots[0],
				)
			},
		},
		{
			name: "override without recovery path",
			mutate: func(materialization *Materialization) {
				materialization.RecoveryPaths = nil
			},
		},
		{
			name: "snapshot for skipped path",
			mutate: func(materialization *Materialization) {
				materialization.SkippedPaths = []string{".env"}
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			store := Store{ConfigDir: filepath.Join(root, "config"), DataDir: filepath.Join(root, "data")}
			state := New(
				filepath.Join(root, "public"),
				filepath.Join(root, "public", ".git"),
				testRepositoryRef(),
				"main",
				store,
			)
			state.Materializing = &Materialization{
				Phase:             MaterializationPushed,
				ResultPrivateHead: strings.Repeat("a", 40),
				PreviousPaths:     []string{".env"},
				FinalPaths:        []string{".env"},
				ExcludedPaths:     []string{".env"},
				OverridePaths:     []string{".env"},
				RecoveryPaths:     []string{".env"},
				WorkspaceSnapshots: []WorkspaceSnapshot{{
					Path:    ".env",
					Digest:  strings.Repeat("b", 64),
					Existed: true,
				}},
			}
			test.mutate(state.Materializing)
			if err := store.Save(state); err == nil {
				t.Fatal("Save() error = nil, want invalid materialization workspace contract")
			}
		})
	}
}
