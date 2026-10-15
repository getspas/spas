package exclude

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyReportsDriftFromLockedRead(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "exclude")
	original := "outside/\n# BEGIN SPAS link=lnk_test\n/.spas-tmp/\n/old.env\n# END SPAS link=lnk_test\nafter/\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := Build(path, "lnk_test", mustPaths(t, "old.env", "new.env"))
	if err != nil {
		t.Fatal(err)
	}
	// This editor finishes before Apply takes its locked read. It is not a
	// noncooperating write racing the atomic replacement.
	edited := strings.Replace(original, "/old.env", "/unexpected.env", 1) + "later/\n"
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	repaired, err := Apply(plan, mustPaths(t, "old.env"))
	if err != nil || !repaired {
		t.Fatalf("Apply() = %t, %v; want repaired drift", repaired, err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "outside/\n# BEGIN SPAS link=lnk_test\n/.spas-tmp/\n/new.env\n/old.env\n# END SPAS link=lnk_test\nafter/\nlater/\n"
	if string(content) != want {
		t.Fatalf("exclude = %q, want %q", content, want)
	}
	repaired, err = Apply(plan, mustPaths(t, "old.env", "new.env"))
	if err != nil || repaired {
		t.Fatalf("repeated Apply() = %t, %v", repaired, err)
	}
	if err := Restore(plan); err != nil {
		t.Fatal(err)
	}
	content, err = os.ReadFile(path)
	if err != nil || string(content) != original+"later/\n" {
		t.Fatalf("restored exclude = %q, %v", content, err)
	}
}

func TestApplyDistinguishesEnrollmentChangesFromDrift(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, content string
		prior, next   []string
		repaired      bool
	}{
		{name: "first enrollment", next: []string{"asset.env"}},
		{name: "ordinary addition", content: "# BEGIN SPAS link=lnk_test\n/.spas-tmp/\n/asset.env\n# END SPAS link=lnk_test\n", prior: []string{"asset.env"}, next: []string{"asset.env", "new.env"}},
		{name: "ordinary removal", content: "# BEGIN SPAS link=lnk_test\r\n/.spas-tmp/\r\n/asset.env\r\n# END SPAS link=lnk_test\r\n", prior: []string{"asset.env"}},
		{name: "missing block", prior: []string{"asset.env"}, next: []string{"asset.env"}, repaired: true},
		{name: "missing temporary rule", content: "# BEGIN SPAS link=lnk_test\n/asset.env\n# END SPAS link=lnk_test\n", prior: []string{"asset.env"}, next: []string{"asset.env"}, repaired: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "exclude")
			if tc.content != "" {
				if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			plan, err := Build(path, "lnk_test", mustPaths(t, tc.next...))
			if err != nil {
				t.Fatal(err)
			}
			repaired, err := Apply(plan, mustPaths(t, tc.prior...))
			if err != nil || repaired != tc.repaired {
				t.Fatalf("Apply() = %t, %v; want repaired %t", repaired, err, tc.repaired)
			}
		})
	}
}

func TestApplyDoesNotWarnForDriftAlreadyRepairedBeforeLockedRead(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "exclude")
	plan, err := Build(path, "lnk_test", mustPaths(t, "asset.env", "new.env"))
	if err != nil {
		t.Fatal(err)
	}
	// Another completed repair makes the initial missing-block observation stale.
	prior := "# BEGIN SPAS link=lnk_test\n/.spas-tmp/\n/asset.env\n# END SPAS link=lnk_test\n"
	if err := os.WriteFile(path, []byte(prior), 0o600); err != nil {
		t.Fatal(err)
	}
	repaired, err := Apply(plan, mustPaths(t, "asset.env"))
	if err != nil || repaired {
		t.Fatalf("Apply() = %t, %v", repaired, err)
	}
}

func TestApplyRejectsMalformedMarkersIntroducedAfterPlanning(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "exclude")
	plan, err := Build(path, "lnk_test", mustPaths(t, "asset.env"))
	if err != nil {
		t.Fatal(err)
	}
	malformed := "# outside\n# BEGIN SPAS link=lnk_test\n/asset.env\n"
	if err := os.WriteFile(path, []byte(malformed), 0o600); err != nil {
		t.Fatal(err)
	}
	repaired, err := Apply(plan, nil)
	if err == nil || repaired || !strings.Contains(err.Error(), "manually") || !strings.Contains(err.Error(), fmt.Sprintf("%q", path)) {
		t.Fatalf("Apply() = %t, %v", repaired, err)
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil || string(content) != malformed {
		t.Fatalf("exclude changed: %q, %v", content, readErr)
	}
}
