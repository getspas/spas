package app

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getspas/spas/internal/limits"
	"github.com/getspas/spas/internal/linkstate"
	"github.com/getspas/spas/internal/pathmodel"
	"github.com/getspas/spas/internal/spaserr"
)

func TestVerifyActiveMergeMaterializationSnapshotsRejectsNewBaseline(t *testing.T) {
	t.Parallel()

	approvedDigest := [32]byte{1}
	active := &linkstate.ActiveMerge{
		MaterializationPaths: []string{"other.txt"},
		MaterializationSnapshots: []linkstate.WorkspaceSnapshot{{
			Path:    "other.txt",
			Existed: true,
			Digest:  hex.EncodeToString(approvedDigest[:]),
		}},
	}
	path := mustPath(t, "other.txt")
	err := verifyActiveMergeMaterializationSnapshots(
		active,
		[]pathmodel.Path{path},
		map[string]fileSnapshot{
			path.String(): {Digest: [32]byte{2}, Existed: true},
		},
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "changed before the final") {
		t.Fatalf("verifyActiveMergeMaterializationSnapshots() error = %v, want changed-baseline rejection", err)
	}
	if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindUnsafeGitState {
		t.Fatalf("error kind = %v, %v; want unsafe Git state", kind, ok)
	}
}

func TestValidateProspectivePrivateTreeSize(t *testing.T) {
	t.Parallel()

	if err := validateProspectivePrivateTreeSize(make([]pathmodel.Path, limits.MaxPrivateTreeEntries)); err != nil {
		t.Fatalf("exact supported size error = %v", err)
	}
	if err := validateProspectivePrivateTreeSize(make([]pathmodel.Path, limits.MaxPrivateTreeEntries+1)); err == nil {
		t.Fatal("over-limit prospective private tree error = nil")
	}
}

func TestResolveStoredOverridesFindsCaseOnlyPrivateReplacement(t *testing.T) {
	t.Parallel()

	public, private, replacements, err := resolveStoredOverrides(
		[]string{"docs/architecture.md"},
		[]pathmodel.Path{mustPath(t, "docs/ARCHITECTURE.md")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := replacements["docs/architecture.md"]; got != "docs/ARCHITECTURE.md" {
		t.Fatalf("replacement = %q, want private spelling", got)
	}
	if _, found := public["docs/architecture.md"]; !found {
		t.Fatal("public override path missing")
	}
	if _, found := private["docs/ARCHITECTURE.md"]; !found {
		t.Fatal("private override path missing")
	}
}

func TestResolveStoredOverridesRejectsMissingOrAmbiguousReplacement(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		private []pathmodel.Path
		want    string
	}{
		{name: "missing", want: "found 0"},
		{
			name: "ambiguous",
			private: []pathmodel.Path{
				mustPath(t, "docs/ARCHITECTURE.md"),
				mustPath(t, "docs/Architecture.md"),
			},
			want: "found 2",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, _, _, err := resolveStoredOverrides([]string{"docs/architecture.md"}, test.private)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("resolveStoredOverrides() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestVerifyApprovedSnapshotsChecksApprovedDeletion(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := mustPath(t, "docs/ARCHITECTURE.md")
	full := path.OSPath(root)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("approved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := snapshotFile(full)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("changed after approval\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = verifyApprovedSnapshots(
		root,
		[]plannedChange{{Path: path, Status: "D"}},
		map[string]fileSnapshot{path.String(): snapshot},
	)
	if err == nil || !strings.Contains(err.Error(), "changed after commit approval for the linked repository") {
		t.Fatalf("verifyApprovedSnapshots() error = %v, want deletion snapshot rejection", err)
	}
}

func TestCaseRenamePreRemovals(t *testing.T) {
	t.Parallel()

	oldPath := mustPath(t, "docs/Foo.md")
	newPath := mustPath(t, "docs/foo.md")

	removals, targets, err := caseRenamePreRemovals(
		[]pathmodel.Path{newPath},
		[]string{oldPath.String()},
		map[string]struct{}{},
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(removals) != 1 || removals[0] != oldPath {
		t.Fatalf("caseRenamePreRemovals() removals = %#v, want %q", removals, oldPath)
	}
	if _, found := targets[pathmodel.Canonical(newPath, true)]; !found {
		t.Fatal("caseRenamePreRemovals() did not mark final canonical destination")
	}
}

func TestCaseRenamePreRemovalsDisabledOnCaseSensitiveFilesystem(t *testing.T) {
	t.Parallel()

	removals, targets, err := caseRenamePreRemovals(
		[]pathmodel.Path{mustPath(t, "docs/foo.md")},
		[]string{"docs/Foo.md"},
		map[string]struct{}{},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(removals) != 0 || len(targets) != 0 {
		t.Fatalf("caseRenamePreRemovals() = %#v, %#v, want no pre-removal", removals, targets)
	}
}

func TestCaseRenamePreRemovalsHonorsSkippedFinalPath(t *testing.T) {
	t.Parallel()

	newPath := mustPath(t, "docs/foo.md")
	removals, _, err := caseRenamePreRemovals(
		[]pathmodel.Path{newPath},
		[]string{"docs/Foo.md"},
		map[string]struct{}{newPath.String(): {}},
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(removals) != 0 {
		t.Fatalf("caseRenamePreRemovals() removals = %#v, want none for skipped final path", removals)
	}
}
