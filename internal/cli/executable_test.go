package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/spf13/cobra"
)

func TestGitExecutableSelectionSurvivesDirectoryChanges(t *testing.T) {
	invocation := t.TempDir()
	t.Chdir(invocation)
	t.Setenv("SPAS_CLI_EXECUTABLE_PROBE", "1")
	toolsDir := filepath.Join(invocation, "tools with spaces")
	if err := os.Mkdir(toolsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	name := "chosen-git"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable := filepath.Join(toolsDir, name)
	program, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, program, 0o755); err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, name := range []string{"public workspace", "probe directory", "private checkout"} {
		dir := filepath.Join(invocation, name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, dir)
	}
	relative := "./tools with spaces/" + name
	selections := []string{relative, executable, "git"}
	if runtime.GOOS == "windows" {
		selections = append(selections, ".\\tools with spaces\\"+name, filepath.VolumeName(executable)+relative[2:])
	}
	for _, selection := range selections {
		t.Run(selection, func(t *testing.T) {
			var output bytes.Buffer
			command := &cobra.Command{}
			command.SetIn(bytes.NewReader(nil))
			command.SetOut(&output)
			command.SetErr(&output)
			instance, err := buildApp(command, &rootOptions{repo: dirs[0], gitPath: selection, nonInteractive: true})
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"-test.run=^TestGitExecutableProbe$"}
			if selection == "git" {
				args = []string{"--version"}
			}
			var first []byte
			for _, dir := range dirs {
				result, err := instance.Git.Run(context.Background(), dir, args...)
				if err != nil {
					t.Errorf("selected %q in %q: %v", selection, dir, err)
					continue
				}
				if selection != "git" && string(result.Stdout) != "selected executable\n" {
					t.Errorf("unexpected executable output: %q", result.Stdout)
				}
				if first == nil {
					first = result.Stdout
				} else if !bytes.Equal(first, result.Stdout) {
					t.Errorf("executable selection changed between directories: %q and %q", first, result.Stdout)
				}
			}
		})
	}
}

func TestGitExecutableProbe(t *testing.T) {
	if os.Getenv("SPAS_CLI_EXECUTABLE_PROBE") != "1" {
		return
	}
	fmt.Fprintln(os.Stdout, "selected executable")
	os.Exit(0)
}
