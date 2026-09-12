package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/getspas/spas/internal/spaserr"
)

func TestFlagGroupErrorsAreInvalidUsage(t *testing.T) {
	t.Parallel()
	var out, stderr bytes.Buffer
	root := NewRootContext(context.Background(), bytes.NewReader(nil), &out, &stderr)
	root.SetArgs([]string{"diff", "--name-only", "--stat"})
	err := root.Execute()
	if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindInvalidUsage {
		t.Fatalf("error = %v, want typed invalid_usage", err)
	}
}

func TestExecuteRejectsConflictingDiffFlags(t *testing.T) {
	for _, jsonMode := range []bool{false, true} {
		args := []string{"diff", "--name-only", "--stat"}
		if jsonMode {
			args = append(args, "--json", "--verbose")
		}
		code, out, stderr := executeCaptured(t, args)
		if code != 2 || len(out) != 0 {
			t.Errorf("Execute(%v) = %d, stdout %q, stderr %q", args, code, out, stderr)
		}
		if jsonMode {
			var envelope struct {
				OK    bool `json:"ok"`
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(stderr, &envelope); err != nil || envelope.OK || envelope.Error.Code != "invalid_usage" {
				t.Errorf("JSON error = %s, decode error %v", stderr, err)
			}
		} else if !bytes.Contains(stderr, []byte("error:")) {
			t.Errorf("missing text diagnostic: %q", stderr)
		}
	}
}

func TestExecutePreservesValidFlagsAndRuntimeErrors(t *testing.T) {
	code, out, stderr := executeCaptured(t, []string{"version", "--json"})
	if code != 0 || len(out) == 0 || len(stderr) != 0 {
		t.Fatalf("valid command = %d, %q, %q", code, out, stderr)
	}
	code, out, stderr = executeCaptured(t, []string{"diff", "--name-only", "--json", "--git", filepath.Join(t.TempDir(), "missing-git")})
	if code != 1 || len(out) != 0 || !bytes.Contains(stderr, []byte(`"code":"operation_failed"`)) {
		t.Fatalf("runtime failure = %d, %q, %q", code, out, stderr)
	}
}

func executeCaptured(t *testing.T, args []string) (int, []byte, []byte) {
	t.Helper()
	root := t.TempDir()
	out, err := os.Create(filepath.Join(root, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	stderr, err := os.Create(filepath.Join(root, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	originalArgs, originalOut, originalErr := os.Args, os.Stdout, os.Stderr
	defer func() { os.Args, os.Stdout, os.Stderr = originalArgs, originalOut, originalErr }()
	os.Args, os.Stdout, os.Stderr = append([]string{"spas"}, args...), out, stderr
	code := Execute()
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stderr.Close(); err != nil {
		t.Fatal(err)
	}
	stdoutBytes, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	stderrBytes, err := os.ReadFile(stderr.Name())
	if err != nil {
		t.Fatal(err)
	}
	return code, stdoutBytes, stderrBytes
}
