package gitexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv("SPAS_AUTH_HELPER"); mode != "" {
		var input []byte
		if mode == "credential" {
			input, _ = io.ReadAll(os.Stdin)
		}
		record := struct {
			Args                  []string
			Input                 string
			Environment           map[string]string
			CredentialInteractive string
		}{Args: os.Args[1:], Input: string(input), Environment: map[string]string{}}
		if mode == "credential" {
			value, err := exec.Command("git", "config", "--get", "credential.interactive").Output()
			if err == nil {
				record.CredentialInteractive = strings.TrimSpace(string(value))
			}
		}
		for _, name := range []string{"GIT_TERMINAL_PROMPT", "GIT_ASKPASS", "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE", "GCM_INTERACTIVE"} {
			record.Environment[name] = os.Getenv(name)
		}
		data, _ := json.Marshal(record)
		if err := os.WriteFile(os.Getenv("SPAS_AUTH_RECORD"), data, 0o600); err != nil {
			os.Exit(2)
		}
		if mode == "ssh" {
			fmt.Fprintln(os.Stderr, "controlled SSH authentication failure")
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func isolatedAuthentication(t *testing.T, mode string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"GIT_SSH", "GIT_SSH_COMMAND", "GIT_SSH_VARIANT", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "gitconfig"))
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	t.Setenv("SPAS_AUTH_HELPER", mode)
	record := filepath.Join(dir, "record.json")
	t.Setenv("SPAS_AUTH_RECORD", record)
	name := "ssh"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	helper := filepath.Join(dir, name)
	data, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, data, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir, record
}

func TestUnattendedDefaultSSHUsesBatchMode(t *testing.T) {
	dir, record := isolatedAuthentication(t, "ssh")
	for _, name := range []string{"GIT_TERMINAL_PROMPT", "GIT_ASKPASS", "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE", "GCM_INTERACTIVE"} {
		t.Setenv(name, "force-interaction")
	}
	// Git for Windows prepends bundled tools to PATH. Its explicit exec path
	// places the controlled SSH helper first without selecting a custom transport.
	_, err := (Runner{NonInteractive: true, Stdin: strings.NewReader("must not inherit")}).RunStreaming(t.Context(), dir, "--exec-path="+dir, "ls-remote", "ssh://git@example.invalid/assets")
	var exit *ExitError
	if !errors.As(err, &exit) || !strings.Contains(exit.Stderr, "controlled SSH authentication failure") {
		t.Fatalf("SSH failure = %v", err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Args        []string
		Input       string
		Environment map[string]string
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(got.Args, " "), "-oBatchMode=yes") || got.Input != "" {
		t.Fatalf("SSH invocation = %s", data)
	}
	for name, want := range map[string]string{"GIT_TERMINAL_PROMPT": "0", "GIT_ASKPASS": "", "SSH_ASKPASS": "", "SSH_ASKPASS_REQUIRE": "never", "GCM_INTERACTIVE": "never"} {
		if got.Environment[name] != want {
			t.Errorf("%s = %q, want %q", name, got.Environment[name], want)
		}
	}
}

func TestCustomSSHSelectionIsPreserved(t *testing.T) {
	for _, selection := range []string{"GIT_SSH_COMMAND", "GIT_SSH", "core.sshCommand", "ssh.variant", "interactive"} {
		t.Run(selection, func(t *testing.T) {
			dir, record := isolatedAuthentication(t, "ssh")
			helper := filepath.Join(dir, "ssh")
			if runtime.GOOS == "windows" {
				helper += ".exe"
			}
			args := []string{"--exec-path=" + dir, "ls-remote", "ssh://git@example.invalid/assets"}
			switch selection {
			case "GIT_SSH_COMMAND":
				t.Setenv(selection, "\""+filepath.ToSlash(helper)+"\" -oBatchMode=yes -p 2222")
			case "GIT_SSH":
				t.Setenv(selection, helper)
			case "core.sshCommand":
				args = append([]string{"-c", "core.sshCommand=\"" + filepath.ToSlash(helper) + "\" -oBatchMode=yes -p 2222"}, args...)
			case "ssh.variant":
				args = append([]string{"-c", "ssh.variant=ssh"}, args...)
			}
			_, err := (Runner{NonInteractive: selection != "interactive"}).Run(t.Context(), dir, args...)
			if err == nil {
				t.Fatal("expected controlled authentication failure")
			}
			data, err := os.ReadFile(record)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Args        []string
				Environment map[string]string
			}
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(got.Args, " ")
			if selection == "core.sshCommand" || selection == "GIT_SSH_COMMAND" {
				if !strings.Contains(joined, "-p 2222") {
					t.Fatalf("custom command lost arguments: %s", data)
				}
			} else if strings.Contains(joined, "BatchMode") {
				t.Fatalf("custom/interactive selection changed: %s", data)
			}
			if selection == "interactive" && got.Environment["GCM_INTERACTIVE"] == "never" {
				t.Fatalf("interactive authentication suppressed: %s", data)
			}
		})
	}
}

func TestUnattendedDiffControlsPartialCloneAuthentication(t *testing.T) {
	dir, record := isolatedAuthentication(t, "ssh")
	git := func(workdir string, args ...string) Result {
		t.Helper()
		result, err := (Runner{}).Run(t.Context(), workdir, args...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return result
	}
	source := filepath.Join(dir, "source")
	git(dir, "init", source)
	git(source, "config", "user.name", "SPAS Test")
	git(source, "config", "user.email", "spas@example.invalid")
	git(source, "config", "uploadpack.allowFilter", "true")
	if err := os.WriteFile(filepath.Join(source, "tracked.txt"), []byte("committed contents\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(source, "add", "tracked.txt")
	git(source, "commit", "-m", "initial")
	blob := strings.TrimSpace(string(git(source, "rev-parse", "HEAD:tracked.txt").Stdout))
	partial := filepath.Join(dir, "partial")
	git(dir, "clone", "--no-local", "--filter=blob:none", "--no-checkout", source, partial)
	missing := git(partial, "rev-list", "--objects", "--all", "--missing=print")
	if !strings.Contains(string(missing.Stdout), "?"+blob) {
		t.Fatalf("partial clone has no missing tracked blob: %s", missing.Stdout)
	}
	index, err := os.ReadFile(filepath.Join(source, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partial, ".git", "index"), index, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partial, "tracked.txt"), []byte("local edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(partial, "remote", "set-url", "origin", "ssh://git@example.invalid/assets")
	// Diff must fetch the missing committed blob. The default SSH executable is
	// controlled through the same exec-path seam as explicit remote operations.
	_, err = (Runner{NonInteractive: true}).Run(t.Context(), partial, "--exec-path="+dir, "diff", "HEAD", "--", "tracked.txt")
	var exit *ExitError
	if !errors.As(err, &exit) || !strings.Contains(exit.Stderr, "controlled SSH authentication failure") {
		t.Fatalf("partial-clone diff failure = %v", err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Args                  []string
		Input                 string
		CredentialInteractive string
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(got.Args, " "), "-oBatchMode=yes") {
		t.Fatalf("implicit-fetch SSH invocation = %s", data)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="SPAS test"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	t.Setenv("SPAS_AUTH_HELPER", "credential")
	git(partial, "remote", "set-url", "origin", server.URL+"/assets")
	git(partial, "config", "credential.helper", "!\""+filepath.ToSlash(os.Args[0])+"\"")
	_, err = (Runner{NonInteractive: true}).Run(t.Context(), partial, "diff", "HEAD", "--", "tracked.txt")
	if !errors.As(err, &exit) {
		t.Fatalf("partial-clone HTTP authentication failure = %v", err)
	}
	data, err = os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.CredentialInteractive != "false" || !strings.Contains(got.Input, "protocol=http\n") {
		t.Fatalf("implicit-fetch credential-helper invocation = %s", data)
	}
}

func TestUnattendedCredentialHelperRetainsProtocolInput(t *testing.T) {
	dir, record := isolatedAuthentication(t, "credential")
	// This helper returns no credentials. Git must fail rather than ask for them.
	helper := filepath.ToSlash(os.Args[0])
	result, err := (Runner{NonInteractive: true}).RunInput(context.Background(), dir, strings.NewReader("protocol=https\nhost=example.invalid\n\n"), "-c", "credential.helper=", "-c", "credential.helper=!\""+helper+"\"", "credential", "fill")
	if err == nil || (!strings.Contains(string(result.Stderr), "terminal prompts disabled") && !strings.Contains(string(result.Stderr), "unable to get password from user")) {
		t.Fatalf("credential fill = %v, stderr=%s", err, result.Stderr)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Input       string
		Environment map[string]string
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Input, "host=example.invalid\n") || got.Environment["GCM_INTERACTIVE"] != "never" {
		t.Fatalf("helper invocation = %s", data)
	}
}
