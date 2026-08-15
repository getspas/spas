package collision

import (
	"testing"

	"github.com/getspas/spas/internal/pathmodel"
)

func TestDetect(t *testing.T) {
	t.Parallel()

	public := paths(t, ".env", "config", "docs/Architecture.md", ".secret/public.json")
	private := paths(t, ".env", "config/private.json", "docs/ARCHITECTURE.md", ".secret/private.json")

	got := Detect(public, private, true)
	if len(got) != 3 {
		t.Fatalf("Detect() returned %d collisions: %#v", len(got), got)
	}
	if got[0].Kind != TrackedPath || got[1].Kind != FileDirectory || got[2].Kind != CaseInsensitive {
		t.Fatalf("Detect() kinds = %v, %v, %v", got[0].Kind, got[1].Kind, got[2].Kind)
	}
}

func TestSharedDirectoryIsNotConflict(t *testing.T) {
	t.Parallel()

	got := Detect(paths(t, ".secret/public.json"), paths(t, ".secret/private.json"), true)
	if len(got) != 0 {
		t.Fatalf("Detect() = %#v, want no conflicts", got)
	}
}

func TestParseCollapsesUnicodeNormalizationVariants(t *testing.T) {
	t.Parallel()

	// pathmodel.Parse normalizes to NFC, so two spellings that differ only in
	// Unicode normalization are the same managed path and can never diverge.
	composed := paths(t, "docs/café.md")
	decomposed := paths(t, "docs/cafe\u0301.md")
	if composed[0] != decomposed[0] {
		t.Fatalf("Parse() produced distinct paths %q and %q for normalization variants", composed[0], decomposed[0])
	}
}

func TestPrivatePortabilityConflictsRejectsUnicodeNormalizationCollision(t *testing.T) {
	t.Parallel()

	// Defense in depth: raw paths that bypassed Parse (for example read
	// directly from a Git tree) must still collide on normalization.
	raw := []pathmodel.Path{pathmodel.Path("docs/café.md"), pathmodel.Path("docs/cafe\u0301.md")}
	err := PrivatePortabilityConflicts(raw, false)
	if err == nil {
		t.Fatal("PrivatePortabilityConflicts() error = nil, want Unicode normalization conflict")
	}
}

func TestPrivatePortabilityConflictsRejectsCaseFoldedFileDirectoryCollision(t *testing.T) {
	t.Parallel()

	err := PrivatePortabilityConflicts(paths(t, "Foo", "foo/child.txt"), true)
	if err == nil {
		t.Fatal("PrivatePortabilityConflicts() error = nil, want file/directory conflict")
	}
}

func TestPrivateRevisionCompatibilityAllowsCaseOnlyRename(t *testing.T) {
	t.Parallel()

	if err := PrivateRevisionCompatibility(paths(t, "docs/Foo.md"), paths(t, "docs/foo.md"), true); err != nil {
		t.Fatalf("PrivateRevisionCompatibility() error = %v, want case-only rename to be allowed", err)
	}
}

func TestPrivateRevisionCompatibilityRejectsFileDirectoryTransition(t *testing.T) {
	t.Parallel()

	if err := PrivateRevisionCompatibility(paths(t, "Foo"), paths(t, "foo/child.txt"), true); err == nil {
		t.Fatal("PrivateRevisionCompatibility() error = nil, want file/directory transition rejection")
	}
	if err := PrivateRevisionCompatibility(paths(t, "foo/child.txt"), paths(t, "Foo"), true); err == nil {
		t.Fatal("PrivateRevisionCompatibility() inverse error = nil, want file/directory transition rejection")
	}
}

func TestPrivateRevisionCompatibilityAllowsSharedDirectoryAcrossRevisions(t *testing.T) {
	t.Parallel()

	if err := PrivateRevisionCompatibility(paths(t, "Folder/one.txt"), paths(t, "folder/two.txt"), true); err != nil {
		t.Fatalf("PrivateRevisionCompatibility() error = %v, want shared directory to be allowed", err)
	}
}

func TestPrivatePortabilityConflictsAllowsSharedDirectories(t *testing.T) {
	t.Parallel()

	if err := PrivatePortabilityConflicts(paths(t, "folder/one.txt", "Folder/two.txt"), true); err != nil {
		t.Fatalf("PrivatePortabilityConflicts() error = %v, want shared directory to be allowed", err)
	}
}

func TestDetectRejectsUnicodeNormalizationCollisionAcrossRepositories(t *testing.T) {
	t.Parallel()

	public := []pathmodel.Path{pathmodel.Path("docs/café.md")}
	private := []pathmodel.Path{pathmodel.Path("docs/cafe\u0301.md")}
	got := Detect(public, private, false)
	if len(got) != 1 || got[0].Kind != PortablePath {
		t.Fatalf("Detect() = %#v, want one cross-platform filesystem conflict", got)
	}
}

func paths(t *testing.T, values ...string) []pathmodel.Path {
	t.Helper()
	result := make([]pathmodel.Path, 0, len(values))
	for _, value := range values {
		path, err := pathmodel.Parse(value)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, path)
	}
	return result
}
