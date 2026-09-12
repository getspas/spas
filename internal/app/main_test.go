package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("SPAS_APP_GIT_PROXY") != "" {
		os.Exit(runGitProxy())
	}
	os.Exit(m.Run())
}

func enableGitProxy(t *testing.T, mode string) {
	t.Helper()
	// The parent race runtime is already initialized. Only subsequently started
	// helpers use this exit policy; their Git command finishes before they exit.
	t.Setenv("GORACE", strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	t.Setenv("SPAS_APP_GIT_PROXY", mode)
}

func TestGitProxyPreservesOptionsAndExitStatus(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GORACE", "history_size=2 exitcode=77 atexit_sleep_ms=1000")
	enableGitProxy(t, "passthrough")
	t.Setenv("SPAS_APP_REAL_GIT", realGit)
	if got := os.Getenv("GORACE"); got != "history_size=2 exitcode=77 atexit_sleep_ms=1000 atexit_sleep_ms=0" {
		t.Fatalf("helper race options = %q", got)
	}
	for _, args := range [][]string{{"--version"}, {"spas-nonexistent-command"}} {
		directOutput, directErr := exec.CommandContext(t.Context(), realGit, args...).CombinedOutput()
		proxyOutput, proxyErr := exec.CommandContext(t.Context(), os.Args[0], args...).CombinedOutput()
		if string(proxyOutput) != string(directOutput) {
			t.Fatalf("proxy output = %q, want %q", proxyOutput, directOutput)
		}
		if directErr == nil {
			if proxyErr != nil {
				t.Fatalf("proxy failed: %v", proxyErr)
			}
		} else {
			directExit, directOK := errors.AsType[*exec.ExitError](directErr)
			proxyExit, proxyOK := errors.AsType[*exec.ExitError](proxyErr)
			if !directOK || !proxyOK || directExit.ExitCode() != proxyExit.ExitCode() {
				t.Fatalf("proxy error = %v, want %v", proxyErr, directErr)
			}
		}
	}
}

func runGitProxy() int {
	args := os.Args[1:]
	mode := os.Getenv("SPAS_APP_GIT_PROXY")
	if mode == "remove-before-diff" && containsArgument(args, "diff") && containsArgument(args, "--no-index") {
		if err := os.Remove(os.Getenv("SPAS_APP_EDIT_PATH")); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if mode == "edit-after-private-abort" {
		marker := os.Getenv("SPAS_APP_ABORT_MARKER")
		if _, err := os.Stat(marker); err == nil {
			if err := os.WriteFile(
				os.Getenv("SPAS_APP_EDIT_PATH"),
				[]byte(os.Getenv("SPAS_APP_EDIT_CONTENT")),
				0o600,
			); err != nil {
				_, _ = os.Stderr.WriteString(err.Error() + "\n")
				return 1
			}
			if err := os.Remove(marker); err != nil {
				_, _ = os.Stderr.WriteString(err.Error() + "\n")
				return 1
			}
		}
	}
	if mode == "fail-write-tree-and-abort" {
		if containsArgument(args, "write-tree") ||
			(containsArgument(args, "merge") && containsArgument(args, "--abort")) {
			_, _ = os.Stderr.WriteString("injected Git failure\n")
			return 1
		}
	}
	if mode == "fail-write-tree-and-recreate-marker" && containsArgument(args, "write-tree") {
		_, _ = os.Stderr.WriteString("injected Git failure\n")
		return 1
	}
	if mode == "fail-commit" && containsArgument(args, "commit") {
		_, _ = os.Stderr.WriteString("injected Git commit failure\n")
		return 1
	}
	if mode == "edit-private-on-abort-tracked-paths" &&
		containsArgument(args, "ls-files") &&
		containsArgument(args, "--cached") {
		if err := os.WriteFile(
			os.Getenv("SPAS_APP_EDIT_PATH"),
			[]byte(os.Getenv("SPAS_APP_EDIT_CONTENT")),
			0o600,
		); err != nil {
			_, _ = os.Stderr.WriteString(err.Error() + "\n")
			return 1
		}
	}
	if mode == "edit-on-public-rm" &&
		containsArgument(args, "rm") &&
		containsArgument(args, "--cached") {
		if err := os.WriteFile(
			os.Getenv("SPAS_APP_EDIT_PATH"),
			[]byte(os.Getenv("SPAS_APP_EDIT_CONTENT")),
			0o600,
		); err != nil {
			_, _ = os.Stderr.WriteString(err.Error() + "\n")
			return 1
		}
	}
	if mode == "edit-on-private-abort" &&
		containsArgument(args, "merge") &&
		containsArgument(args, "--abort") {
		if err := os.WriteFile(
			os.Getenv("SPAS_APP_EDIT_PATH"),
			[]byte(os.Getenv("SPAS_APP_EDIT_CONTENT")),
			0o600,
		); err != nil {
			_, _ = os.Stderr.WriteString(err.Error() + "\n")
			return 1
		}
	}
	if mode == "fail-push-nff" && containsArgument(args, "push") {
		countPath := os.Getenv("SPAS_APP_PUSH_COUNT")
		count := 0
		if data, err := os.ReadFile(countPath); err == nil {
			count, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		}
		count++
		if err := os.WriteFile(countPath, []byte(strconv.Itoa(count)), 0o600); err != nil {
			_, _ = os.Stderr.WriteString(err.Error() + "\n")
			return 1
		}
		_, _ = fmt.Fprintln(os.Stdout, "!\trefs/heads/main:refs/heads/main\t[rejected] (non-fast-forward)")
		return 1
	}
	command := exec.Command(os.Getenv("SPAS_APP_REAL_GIT"), args...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Env = os.Environ()
	if err := command.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return 127
	}
	if (mode == "fail-write-tree-and-recreate-marker" || mode == "recreate-marker-after-abort") &&
		containsArgument(args, "merge") &&
		containsArgument(args, "--abort") {
		if err := os.Symlink("MERGE_HEAD", os.Getenv("SPAS_APP_MERGE_MARKER")); err != nil {
			_, _ = os.Stderr.WriteString(err.Error() + "\n")
			return 1
		}
	}
	if mode == "edit-after-private-abort" &&
		containsArgument(args, "merge") &&
		containsArgument(args, "--abort") {
		if err := os.WriteFile(os.Getenv("SPAS_APP_ABORT_MARKER"), []byte("ready"), 0o600); err != nil {
			_, _ = os.Stderr.WriteString(err.Error() + "\n")
			return 1
		}
	}
	return 0
}

func containsArgument(args []string, wanted string) bool {
	for _, arg := range args {
		if arg == wanted {
			return true
		}
	}
	return false
}
