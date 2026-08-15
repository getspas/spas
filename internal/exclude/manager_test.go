package exclude

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getspas/spas/internal/lock"
	"github.com/getspas/spas/internal/pathmodel"
	"github.com/getspas/spas/internal/spaserr"
)

func TestBuildPreservesUserContent(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "exclude")
	before := []byte("# user comment\r\nscratch/\r\n")
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := Build(path, "lnk_test", mustPaths(t, ".env", "docs/A B[1].md"))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.ExistingUserRules {
		t.Fatal("ExistingUserRules = false, want true")
	}
	if !bytes.HasPrefix(plan.After, before) {
		t.Fatalf("Build() did not preserve prefix:\n%q", plan.After)
	}
	want := []byte("/docs/A\\ B\\[1\\].md\r\n")
	if !bytes.Contains(plan.After, want) {
		t.Fatalf("Build() result %q does not contain %q", plan.After, want)
	}
	if !bytes.Contains(plan.After, []byte("/.spas-tmp/\r\n")) {
		t.Fatalf("Build() result does not reserve managed temporary directory: %q", plan.After)
	}
}

func TestBuildReplacesOnlyManagedBlock(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "exclude")
	before := []byte("user/\n\n# BEGIN SPAS link=lnk_test\n/old\n# END SPAS link=lnk_test\nother/\n")
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := Build(path, "lnk_test", mustPaths(t, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(plan.After, []byte("user/\n")) || !bytes.Contains(plan.After, []byte("other/\n")) {
		t.Fatalf("Build() removed user rules: %q", plan.After)
	}
	if bytes.Contains(plan.After, []byte("/old\n")) {
		t.Fatalf("Build() retained old managed path: %q", plan.After)
	}
}

func TestBuildRejectsMalformedBlock(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "exclude")
	if err := os.WriteFile(path, []byte("# BEGIN SPAS link=lnk_test\n/.env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(path, "lnk_test", mustPaths(t, ".env")); err == nil {
		t.Fatal("Build() error = nil, want non-nil")
	} else if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindExclusionValidation {
		t.Fatalf("Build() error = %v, kind = %v, want KindExclusionValidation", err, kind)
	}
}

func TestBuildEscapesEverySupportedGitIgnoreMetacharacter(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "exclude")
	plan, err := Build(path, "lnk_test", mustPaths(t,
		"docs/space name.md",
		"docs/#draft.md",
		"docs/!private.md",
		"docs/[internal].md",
	))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"/docs/space\\ name.md\n",
		"/docs/\\#draft.md\n",
		"/docs/\\!private.md\n",
		"/docs/\\[internal\\].md\n",
	} {
		if !bytes.Contains(plan.After, []byte(want)) {
			t.Errorf("Build() output does not contain %q:\n%s", want, plan.After)
		}
	}
}

func TestBuildRejectsDuplicateStartsAndEndings(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"duplicate starts": "# BEGIN SPAS link=lnk_test\n/.env\n# BEGIN SPAS link=lnk_test\n# END SPAS link=lnk_test\n",
		"duplicate ends":   "# BEGIN SPAS link=lnk_test\n/.env\n# END SPAS link=lnk_test\n# END SPAS link=lnk_test\n",
		"orphan ending":    "# END SPAS link=lnk_test\n",
	}
	for name, content := range tests {
		name := name
		content := content
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "exclude")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Build(path, "lnk_test", mustPaths(t, ".env")); err == nil {
				t.Fatal("Build() error = nil, want malformed-block rejection")
			}
		})
	}
}

func TestBuildRemovesOnlyManagedBlockWhenNoPathsRemain(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "exclude")
	before := []byte("user-rule\n\n# BEGIN SPAS link=lnk_test\n/.env\n# END SPAS link=lnk_test\ntrailing-rule\n")
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := Build(path, "lnk_test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(plan.After, []byte("BEGIN SPAS")) {
		t.Fatalf("Build() retained managed block:\n%s", plan.After)
	}
	if !bytes.Contains(plan.After, []byte("user-rule")) || !bytes.Contains(plan.After, []byte("trailing-rule")) {
		t.Fatalf("Build() removed user content:\n%s", plan.After)
	}
}

func mustPaths(t *testing.T, values ...string) []pathmodel.Path {
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

func TestRestoreRemovesExcludeFileCreatedByApply(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "info", "exclude")
	plan, err := Build(path, "lnk_test", mustPaths(t, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(plan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Apply() did not create exclude file: %v", err)
	}
	if err := Restore(plan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Restore() left newly created exclude file: %v", err)
	}
}

func TestApplyAndRestorePreserveConcurrentOutsideContent(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "exclude")
	if err := os.WriteFile(path, []byte("user/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := Build(path, "lnk_test", mustPaths(t, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("user/\nnew-rule/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Apply(plan); err != nil {
		t.Fatal(err)
	}
	afterApply, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(afterApply, []byte("new-rule/\n")) || !bytes.Contains(afterApply, []byte("/.env\n")) {
		t.Fatalf("Apply() failed to merge the managed block with latest content:\n%s", afterApply)
	}

	plan, err = Build(path, "lnk_test", mustPaths(t, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(plan); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("later/\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Restore(plan); err != nil {
		t.Fatal(err)
	}
	afterRestore, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(afterRestore, []byte("later/\n")) || !bytes.Contains(afterRestore, []byte("/.env\n")) {
		t.Fatalf("Restore() failed to preserve latest outside content:\n%s", afterRestore)
	}
}

func TestApplyFailsFastWhenAdjacentLockExistsAndDoesNotStealIt(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "exclude")
	if err := os.WriteFile(path, []byte("user/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := Build(path, "lnk_test", mustPaths(t, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	lockPath := path + ".spas.lock"
	holder, err := lock.AcquirePath(lockPath, "exclude test")
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	if err := Apply(plan); err == nil {
		t.Fatal("Apply() error = nil, want lock contention")
	} else {
		kind, ok := spaserr.KindOf(err)
		if !ok || kind != spaserr.KindLockHeld {
			t.Fatalf("Apply() error = %v, kind = %v, want KindLockHeld", err, kind)
		}
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("Apply() removed the persistent lock path: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "user/\n" {
		t.Fatalf("exclude changed during contention: %q, %v", got, err)
	}
}

func TestApplyAllowsPreexistingUnlockedLockFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "exclude")
	plan, err := Build(path, "lnk_test", mustPaths(t, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	lockPath := path + ".spas.lock"
	if err := os.WriteFile(lockPath, []byte("previous metadata"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Apply(plan); err != nil {
		t.Fatalf("Apply() error = %v, want unlocked persistent lock file to be reusable", err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("Apply() removed persistent lock file: %v", err)
	}
}

func TestApplyRetainsPersistentLockAfterHandledFailure(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "exclude")
	plan, err := Build(path, "lnk_test", mustPaths(t, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# BEGIN SPAS link=lnk_test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Apply(plan); err == nil || !strings.Contains(err.Error(), "unterminated") {
		t.Fatalf("Apply() error = %v, want malformed-block failure", err)
	}
	if _, err := os.Stat(path + ".spas.lock"); err != nil {
		t.Fatalf("handled failure removed persistent lock: %v", err)
	}
}

func TestCooperatingLinkPlansSerializeWithoutLosingEitherBlock(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "exclude")
	if err := os.WriteFile(path, []byte("user/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := Build(path, "lnk_first", mustPaths(t, "first.env"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(path, "lnk_second", mustPaths(t, "second.env"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(first); err != nil {
		t.Fatal(err)
	}
	if err := Apply(second); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"user/\n", "# BEGIN SPAS link=lnk_first\n", "/first.env\n", "# BEGIN SPAS link=lnk_second\n", "/second.env\n"} {
		if !bytes.Contains(content, []byte(want)) {
			t.Errorf("serialized exclude content omitted %q:\n%s", want, content)
		}
	}
}

func TestBuildTreatsMarkerPrefixesAsUserContent(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "exclude")
	before := []byte("# BEGIN SPAS link=lnk_test trailing text\n# END SPAS link=lnk_test trailing text\n")
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := Build(path, "lnk_test", mustPaths(t, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(plan.After, before) {
		t.Fatalf("Build() interpreted marker-like user text as a managed block:\n%s", plan.After)
	}
	if !bytes.Contains(plan.After, []byte("# BEGIN SPAS link=lnk_test\n/.spas-tmp/\n/.env\n# END SPAS link=lnk_test\n")) {
		t.Fatalf("Build() did not append the exact managed block:\n%s", plan.After)
	}
}

func TestBuildDoesNotConfuseLinkIDPrefix(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "exclude")
	before := []byte("# BEGIN SPAS link=lnk_test_other\n/other\n# END SPAS link=lnk_test_other\n")
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := Build(path, "lnk_test", mustPaths(t, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(plan.After, before) {
		t.Fatalf("Build() altered a different link's block:\n%s", plan.After)
	}
	if !bytes.Contains(plan.After, []byte("# BEGIN SPAS link=lnk_test\n/.spas-tmp/\n/.env\n# END SPAS link=lnk_test\n")) {
		t.Fatalf("Build() did not append the requested link block:\n%s", plan.After)
	}
}

func TestBuildRejectsSymlinkedExcludeFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "shared-ignore")
	if err := os.WriteFile(target, []byte("user/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "exclude")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Build(path, "lnk_test", mustPaths(t, ".env")); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("Build() error = %v, want symlink rejection", err)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("exclude symlink was altered: info=%v err=%v", info, err)
	}
}
