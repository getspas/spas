package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnlinkDiscardRequiresFileRemoval(t *testing.T) {
	code, out, stderr := executeCaptured(t, []string{"unlink", "--discard-unsynced-changes", "--json"})
	if code != 2 || len(out) != 0 || !strings.Contains(string(stderr), "--discard-unsynced-changes requires --remove-files") {
		t.Fatalf("code %d, stdout %s, stderr %s", code, out, stderr)
	}
}

func TestUnlinkExplicitCLIApprovalRoutes(t *testing.T) {
	for _, test := range []struct {
		name    string
		change  bool
		pending bool
		args    []string
		code    int
		missing string
	}{
		{name: "synchronized", args: []string{"--remove-files", "--approve-remove-files"}},
		{name: "discard does not approve removal", change: true, args: []string{"--remove-files", "--discard-unsynced-changes", "--yes", "--force"}, code: 4, missing: "--approve-remove-files"},
		{name: "yes and force do not approve discard", change: true, args: []string{"--remove-files", "--approve-remove-files", "--yes", "--force"}, code: 4, missing: "--discard-unsynced-changes"},
		{name: "explicit discard", change: true, args: []string{"--remove-files", "--approve-remove-files", "--discard-unsynced-changes"}},
		{name: "pending addition needs discard", pending: true, args: []string{"--force", "--remove-files", "--approve-remove-files"}, code: 4, missing: "--discard-unsynced-changes"},
		{name: "pending addition approved", pending: true, args: []string{"--force", "--remove-files", "--approve-remove-files", "--discard-unsynced-changes"}},
		{name: "keep edited files by default", change: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := unlinkCLIWorkspace(t)
			asset := filepath.Join(workspace, "asset.env")
			if test.change {
				if err := os.WriteFile(asset, []byte("unsynchronized edit\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if test.pending {
				asset = filepath.Join(workspace, "new.env")
				if err := os.WriteFile(asset, []byte("new work\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if code, _, errOut := executeCaptured(t, []string{"add", "new.env", "--repo", workspace, "--existing-exclude", "preserve", "--merge-protection", "skip", "--json"}); code != 0 {
					t.Fatalf("add %d: %s", code, errOut)
				}
			}
			args := append([]string{"unlink", "--json", "--non-interactive", "--repo", workspace}, test.args...)
			code, out, stderr := executeCaptured(t, args)
			if code != test.code || bytes.Contains(stderr, []byte("\x1b")) || bytes.Contains(out, []byte("\x1b")) {
				t.Fatalf("code %d stdout %s stderr %s", code, out, stderr)
			}
			if code != 0 {
				var envelope struct {
					SchemaVersion int  `json:"schemaVersion"`
					OK            bool `json:"ok"`
					Error         struct {
						Code    string `json:"code"`
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal(stderr, &envelope); err != nil || envelope.SchemaVersion != 2 || envelope.OK || envelope.Error.Code != "decision_required" || !strings.Contains(envelope.Error.Message, test.missing) || len(out) != 0 {
					t.Fatalf("error envelope %s, %v", stderr, err)
				}
				if test.missing == "--discard-unsynced-changes" && !strings.Contains(envelope.Error.Message, filepath.Base(asset)) {
					t.Fatalf("missing affected path: %s", stderr)
				}
				if _, err := os.Stat(asset); err != nil {
					t.Fatalf("missing approval removed asset: %v", err)
				}
			} else {
				var result struct {
					Unlinked  bool     `json:"unlinked"`
					KeptFiles bool     `json:"keptFiles"`
					Visible   []string `json:"workspaceFilesNowVisibleToPublicGit"`
				}
				if err := json.Unmarshal(out, &result); err != nil || !result.Unlinked || len(stderr) != 0 {
					t.Fatalf("result %s stderr %s, %v", out, stderr, err)
				}
				if len(test.args) == 0 {
					if !result.KeptFiles || len(result.Visible) != 1 || result.Visible[0] != "asset.env" {
						t.Fatalf("default unlink result %s", out)
					}
					if content, err := os.ReadFile(asset); err != nil || string(content) != "unsynchronized edit\n" {
						t.Fatalf("kept file %q, %v", content, err)
					}
				} else if _, err := os.Stat(asset); !os.IsNotExist(err) {
					t.Fatalf("cleanup target remains: %v", err)
				}
			}
		})
	}
}

func TestUnlinkDiscardInvalidCombinations(t *testing.T) {
	for _, args := range [][]string{
		{"--discard-unsynced-changes"},
		{"--discard-unsynced-changes", "--keep-files"},
		{"--discard-unsynced-changes", "--remove-private-clone"},
		{"--discard-unsynced-changes", "--remove-files", "--keep-files"},
		{"--discard-unsynced-changes", "--remove-files", "--keep-files=false"},
	} {
		code, out, stderr := executeCaptured(t, append([]string{"unlink", "--json"}, args...))
		if code != 2 || len(out) != 0 || !json.Valid(stderr) {
			t.Fatalf("%v: code %d stdout %s stderr %s", args, code, out, stderr)
		}
	}
}

func unlinkCLIWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"HOME", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_DATA_HOME"} {
		t.Setenv(name, root)
	}
	workspace, remote := filepath.Join(root, "workspace"), filepath.Join(root, "assets.git")
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v, %s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main", workspace)
	git("-C", workspace, "config", "user.name", "Test")
	git("-C", workspace, "config", "user.email", "test@example.invalid")
	git("-C", workspace, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-q", "-m", "initial")
	git("init", "--bare", "-q", "-b", "main", remote)
	config := filepath.Join(root, "gitconfig")
	git("config", "--file", config, "url.file://"+filepath.ToSlash(remote)+".insteadOf", "https://github.com/getspas/assets.git")
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if err := os.WriteFile(filepath.Join(workspace, "asset.env"), []byte("asset\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"link", "getspas/assets", "--branch", "main"},
		{"add", "asset.env", "--existing-exclude", "preserve", "--merge-protection", "skip"},
		{"sync", "--message", "Add asset", "--existing-exclude", "preserve", "--merge-protection", "skip"},
	} {
		code, out, stderr := executeCaptured(t, append(args, "--repo", workspace, "--non-interactive"))
		if code != 0 {
			t.Fatalf("%v: code %d stdout %s stderr %s", args, code, out, stderr)
		}
	}
	return workspace
}
