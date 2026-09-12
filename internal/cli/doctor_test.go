package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getspas/spas/internal/app"
)

func TestExecuteDoctorDistinguishesCorruptionFromAbsence(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"HOME", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_DATA_HOME"} {
		t.Setenv(name, root)
	}
	workspace := filepath.Join(root, "workspace")
	if output, err := exec.CommandContext(t.Context(), "git", "init", "-q", "-b", "main", workspace).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v, %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".git", "config"), []byte("[broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, directory string
		code            int
	}{
		{"corrupt", workspace, 1},
		{"outside", outside, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, jsonMode := range []bool{false, true} {
				args := []string{"doctor", "--repo", test.directory}
				if jsonMode {
					args = append(args, "--json")
				}
				code, output, stderr := executeCaptured(t, args)
				if code != test.code {
					t.Fatalf("Doctor exit = %d, output=%s stderr=%s", code, output, stderr)
				}
				if jsonMode {
					var result app.DoctorResult
					if err := json.Unmarshal(output, &result); err != nil {
						t.Fatal(err)
					}
					if result.Healthy != (test.code == 0) || (result.Errors == 0) != (test.code == 0) || len(stderr) != 0 {
						t.Fatalf("Doctor result = %s, stderr=%s", output, stderr)
					}
				}
				if test.code == 1 && !strings.Contains(string(output), "bad config") {
					t.Fatalf("Doctor hid configuration error: %s", output)
				}
			}
		})
	}
}
