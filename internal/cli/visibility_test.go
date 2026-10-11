package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicApprovalOptionRemoved(t *testing.T) {
	for _, command := range []string{"link", "sync"} {
		code, out, stderr := executeCaptured(t, []string{command, "--allow-public", "--json"})
		if code != 2 || len(out) != 0 {
			t.Fatalf("%s obsolete option: code %d, stdout %s, stderr %s", command, code, out, stderr)
		}
	}
}

func TestPublicRepositoryCLIWarningStreams(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"HOME", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_DATA_HOME"} {
		t.Setenv(name, root)
	}
	workspace := filepath.Join(root, "workspace")
	remote := filepath.Join(root, "assets.git")
	git := func(args ...string) {
		t.Helper()
		if output, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v, %s", args, err, output)
		}
	}
	git("init", "-q", "-b", "main", workspace)
	git("-C", workspace, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-q", "-m", "initial")
	git("init", "--bare", "-q", "-b", "main", remote)
	source := filepath.Join(root, "source")
	git("init", "-q", "-b", "main", source)
	if err := os.WriteFile(filepath.Join(source, "asset.txt"), []byte("remote asset\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("-C", source, "add", "asset.txt")
	git("-C", source, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "initial asset")
	git("-C", source, "push", "-q", remote, "main")
	config := filepath.Join(root, "gitconfig")
	// Redirect this test's GitHub URL to a real local repository. Git retains
	// the original origin URL, so normal SPAS origin checks still run.
	git("config", "--file", config, "url.file://"+filepath.ToSlash(remote)+".insteadOf", "https://github.com/getspas/assets.git")
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, jsonMode := range []bool{false, true} {
		args := []string{"link", "getspas/assets", "--branch", "main", "--non-interactive", "--repo", workspace}
		if jsonMode {
			args = append(args, "--json")
		}
		// Unlink between cases; no assets or checkout are removed.
		if jsonMode {
			code, _, stderr := executeCaptured(t, []string{"unlink", "--force", "--repo", workspace})
			if code != 0 {
				t.Fatalf("unlink: code %d, stderr %s", code, stderr)
			}
		}
		for _, wantFailure := range []bool{false, true} {
			code, out, stderr := executeCaptured(t, args)
			wantCode := 0
			if wantFailure {
				wantCode = 1
			}
			if code != wantCode || bytes.Contains(out, []byte("publicly readable")) || bytes.Contains(stderr, []byte("\x1b")) {
				t.Fatalf("link: code %d, stdout %s, stderr %s", code, out, stderr)
			}
			if jsonMode {
				assertVisibilityJSONStreams(t, out, stderr, wantFailure, "operation_failed")
			} else if !bytes.Contains(stderr, []byte("warning:")) || !bytes.Contains(stderr, []byte("publicly readable")) || bytes.Contains(stderr, []byte("Continue?")) {
				t.Fatalf("human stderr: %s", stderr)
			}
		}
	}
	// A sync without enrolled files needs no commit approval, even for public
	// storage. The second invocation verifies that visibility is checked again.
	for i := 0; i < 2; i++ {
		code, out, stderr := executeCaptured(t, []string{"sync", "--repo", workspace, "--json", "--existing-exclude", "preserve", "--merge-protection", "skip"})
		if code != 0 {
			t.Fatalf("sync: code %d, stdout %s, stderr %s", code, out, stderr)
		}
		assertVisibilityJSONStreams(t, out, stderr, false, "")
	}
	if err := os.WriteFile(filepath.Join(workspace, "new.txt"), []byte("new work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := executeCaptured(t, []string{"add", "new.txt", "--repo", workspace, "--json", "--existing-exclude", "preserve", "--merge-protection", "skip"})
	if code != 0 {
		t.Fatalf("add: code %d, stdout %s, stderr %s", code, out, stderr)
	}
	code, out, stderr = executeCaptured(t, []string{"sync", "--repo", workspace, "--json", "--existing-exclude", "preserve", "--merge-protection", "skip"})
	if code != 4 {
		t.Fatalf("sync missing commit approval: code %d, stdout %s, stderr %s", code, out, stderr)
	}
	assertVisibilityJSONStreams(t, out, stderr, true, "decision_required")
}

func assertVisibilityJSONStreams(t *testing.T, out, stderr []byte, failed bool, errorCode string) {
	t.Helper()
	if bytes.Contains(stderr, []byte("\x1b")) || bytes.Contains(out, []byte("\x1b")) {
		t.Fatal("ANSI in machine streams")
	}
	decoder := json.NewDecoder(bytes.NewReader(stderr))
	var warning map[string]json.RawMessage
	if err := decoder.Decode(&warning); err != nil || len(warning) != 2 || string(warning["schemaVersion"]) != "2" {
		t.Fatalf("warning envelope %s: %v", stderr, err)
	}
	var diagnostic map[string]string
	if err := json.Unmarshal(warning["warning"], &diagnostic); err != nil || len(diagnostic) != 2 || diagnostic["code"] != "public_repository" || !strings.Contains(diagnostic["message"], "publicly readable") {
		t.Fatalf("warning = %s", stderr)
	}
	if failed {
		var envelope struct {
			SchemaVersion int  `json:"schemaVersion"`
			OK            bool `json:"ok"`
			Error         struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := decoder.Decode(&envelope); err != nil || envelope.SchemaVersion != 2 || envelope.OK || envelope.Error.Code != errorCode || envelope.Error.Message == "" || len(out) != 0 {
			t.Fatalf("failure streams stdout %s, stderr %s, decode %v", out, stderr, err)
		}
	} else {
		var result map[string]any
		if err := json.Unmarshal(out, &result); err != nil || result["schemaVersion"] != float64(2) {
			t.Fatalf("result %s: %v", out, err)
		}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("unexpected trailing stderr: %s, %v", stderr, err)
	}
}
