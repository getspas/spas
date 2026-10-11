package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestExclusionCLIWarningStreams(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"HOME", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_DATA_HOME"} {
		t.Setenv(name, root)
	}
	workspace := filepath.Join(root, "workspace")
	remote := filepath.Join(root, "assets.git")
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v, %s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main", workspace)
	git("-C", workspace, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-q", "-m", "initial")
	git("init", "--bare", "-q", "-b", "main", remote)
	config := filepath.Join(root, "gitconfig")
	git("config", "--file", config, "url.file://"+filepath.ToSlash(remote)+".insteadOf", "https://github.com/getspas/assets.git")
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if code, _, errOut := executeCaptured(t, []string{"link", "getspas/assets", "--branch", "main", "--non-interactive", "--repo", workspace}); code != 0 {
		t.Fatalf("link %d: %s", code, errOut)
	}
	if err := os.WriteFile(filepath.Join(workspace, "asset.env"), []byte("asset\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"add", "asset.env", "--existing-exclude", "preserve", "--merge-protection", "enable", "--non-interactive", "--repo", workspace}
	if code, _, errOut := executeCaptured(t, args); code != 0 || len(errOut) != 0 {
		t.Fatalf("enrollment %d: %s", code, errOut)
	}
	for _, jsonMode := range []bool{false, true} {
		for _, failure := range []bool{false, true} {
			path := filepath.Join(workspace, ".git", "info", "exclude")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			ignore := filepath.Join(workspace, ".gitignore")
			if failure {
				if err := os.WriteFile(ignore, []byte("!asset.env\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Remove(ignore); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
			currentArgs := append([]string{}, args...)
			if jsonMode {
				currentArgs = append(currentArgs, "--json")
			}
			code, out, stderr := executeCaptured(t, currentArgs)
			want := 0
			if failure {
				want = 9
			}
			if code != want || bytes.Contains(out, []byte("Reconciled unexpected")) || bytes.Contains(stderr, []byte("\x1b")) {
				t.Fatalf("code %d stdout %s stderr %s", code, out, stderr)
			}
			if jsonMode {
				decoder := json.NewDecoder(bytes.NewReader(stderr))
				var warning map[string]json.RawMessage
				if err := decoder.Decode(&warning); err != nil || len(warning) != 2 || string(warning["schemaVersion"]) != "2" {
					t.Fatalf("warning %s: %v", stderr, err)
				}
				var fields map[string]string
				if err := json.Unmarshal(warning["warning"], &fields); err != nil || len(fields) != 2 || fields["code"] != "exclusion_drift_repaired" || fields["message"] == "" {
					t.Fatalf("warning %s: %v", stderr, err)
				}
				if failure {
					var envelope struct {
						SchemaVersion int `json:"schemaVersion"`
						Error         struct {
							Code string `json:"code"`
						} `json:"error"`
					}
					if err := decoder.Decode(&envelope); err != nil || envelope.SchemaVersion != 2 || envelope.Error.Code != "exclusion_validation_failed" || len(out) != 0 {
						t.Fatalf("error %s: %v", stderr, err)
					}
				} else if !json.Valid(out) {
					t.Fatalf("stdout %s", out)
				}
				var extra any
				if err := decoder.Decode(&extra); err != io.EOF {
					t.Fatalf("trailing stderr %s: %v", stderr, err)
				}
			} else if !bytes.Contains(stderr, []byte("warning: Reconciled unexpected changes")) {
				t.Fatalf("warning %s", stderr)
			}
			// Failed verification restores the missing file. Prepare a regular file
			// before the next case's simulated external deletion.
			if failure {
				if err := os.WriteFile(path, []byte("# external\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
