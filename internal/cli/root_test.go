package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getspas/spas/internal/app"
	"github.com/getspas/spas/internal/interaction"
	"github.com/getspas/spas/internal/linkstate"
	"github.com/getspas/spas/internal/spaserr"
	"github.com/getspas/spas/internal/version"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func TestRootHelp(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	root := NewRootContext(context.Background(), strings.NewReader(""), &output, &output)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, expected := range []string{"link", "add", "sync", "doctor", "--non-interactive", "--json", "--timeout"} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("help does not contain %q", expected)
		}
	}
}

func TestSyncHelpHasInteractiveAndOneLineExamples(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	root := NewRootContext(context.Background(), strings.NewReader(""), &output, &output)
	root.SetArgs([]string{"sync", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, expected := range []string{"Interactive", "One line", "--message", "--non-interactive"} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("sync help does not contain %q", expected)
		}
	}
}

func TestSyncRejectsAmbiguousConflictFlags(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"sync", "--force", "--conflict", "override"},
		{"sync", "--skip-conflicts", "--conflict", "skip"},
		{"sync", "--discard-public-changes"},
		{"sync", "--discard-public-changes", "--conflict", "skip"},
		{"sync", "--skip-conflicts", "--discard-public-changes"},
	} {
		var output bytes.Buffer
		root := NewRootContext(context.Background(), strings.NewReader(""), &output, &output)
		root.SetArgs(args)
		if err := root.Execute(); err == nil {
			t.Errorf("Execute(%v) error = nil, want invalid flag combination", args)
		}
	}
}

func TestSyncAllowsExplicitOverrideDiscardAndContinueDiscard(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"sync", "--discard-public-changes", "--conflict", "override"},
		{"sync", "--continue", "--discard-public-changes"},
	} {
		args := args
		t.Run(strings.Join(args[1:], "-"), func(t *testing.T) {
			var output bytes.Buffer
			root := NewRootContext(context.Background(), strings.NewReader(""), &output, &output)
			root.SetArgs(args)
			err := root.Execute()
			if err == nil {
				t.Fatal("Execute() error = nil, want workspace lookup failure after flag validation")
			}
			if strings.Contains(err.Error(), "discard-public-changes requires") || strings.Contains(err.Error(), "not valid") {
				t.Fatalf("Execute(%v) rejected valid flag combination: %v", args, err)
			}
		})
	}
}

func TestEveryCommandAndFlagHasHelpText(t *testing.T) {
	t.Parallel()

	root := NewRootContext(context.Background(), strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	expectedCommands := map[string]bool{
		"add": false, "completion": false, "diff": false, "doctor": false,
		"link": false, "remove": false, "status": false, "sync": false,
		"unlink": false, "version": false,
	}
	for _, command := range root.Commands() {
		if _, expected := expectedCommands[command.Name()]; !expected {
			continue
		}
		expectedCommands[command.Name()] = true
		if strings.TrimSpace(command.Short) == "" {
			t.Errorf("%s has no short help", command.Name())
		}
		command.Flags().VisitAll(func(flag *pflag.Flag) {
			if strings.TrimSpace(flag.Usage) == "" {
				t.Errorf("%s --%s has no help text", command.Name(), flag.Name)
			}
		})
	}
	for name, found := range expectedCommands {
		if !found {
			t.Errorf("root command does not include %q", name)
		}
	}
}

func TestMutatingCommandHelpExplainsBehaviorAndOneLineUse(t *testing.T) {
	t.Parallel()

	tests := map[string][]string{
		"link":   {"no network request", "One line", "--non-interactive"},
		"add":    {"local exclude file", "One line", "--non-interactive"},
		"remove": {"does not", "--non-interactive"},
		"sync":   {"never creates a commit in the project repository", "One line", "--non-interactive"},
		"unlink": {"kept unless removal is explicitly requested", "--remove-files", "--non-interactive"},
	}
	for command, expected := range tests {
		command := command
		expected := expected
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			root := NewRootContext(context.Background(), strings.NewReader(""), &output, &output)
			root.SetArgs([]string{command, "--help"})
			if err := root.Execute(); err != nil {
				t.Fatalf("%s --help: %v", command, err)
			}
			for _, value := range expected {
				if !strings.Contains(output.String(), value) {
					t.Errorf("%s help does not contain %q:\n%s", command, value, output.String())
				}
			}
		})
	}
}

func TestCommandValidationRejectsInvalidOptionsAndModes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "add existing exclude", args: []string{"add", "file", "--existing-exclude", "replace"}},
		{name: "add merge protection", args: []string{"add", "file", "--merge-protection", "always"}},
		{name: "sync conflict", args: []string{"sync", "--conflict", "merge"}},
		{name: "sync existing exclude", args: []string{"sync", "--existing-exclude", "replace"}},
		{name: "sync merge protection", args: []string{"sync", "--merge-protection", "always"}},
		{name: "sync message sources", args: []string{"sync", "--message", "reason", "--message-file", "reason.txt"}},
		{name: "sync modes", args: []string{"sync", "--continue", "--abort"}},
		{name: "sync continue conflict", args: []string{"sync", "--continue", "--conflict", "override"}},
		{name: "sync continue force", args: []string{"sync", "--continue", "--force"}},
		{name: "sync continue branch", args: []string{"sync", "--continue", "--branch", "other"}},
		{name: "sync abort message", args: []string{"sync", "--abort", "--message", "reason"}},
		{name: "sync abort conflict", args: []string{"sync", "--abort", "--conflict", "skip"}},
		{name: "sync force and skip", args: []string{"sync", "--force", "--skip-conflicts"}},
		{name: "unlink approval", args: []string{"unlink", "--approve-remove-files"}},
		{name: "completion shell", args: []string{"completion", "cmd"}},
		{name: "link too many args", args: []string{"link", "one/repo", "two/repo"}},
		{name: "status args", args: []string{"status", "unexpected"}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			root := NewRootContext(context.Background(), strings.NewReader(""), &output, &output)
			root.SetArgs(test.args)
			if err := root.Execute(); err == nil {
				t.Fatalf("Execute(%v) error = nil, want validation error", test.args)
			}
		})
	}
}

func TestCompletionSupportsAllDocumentedShells(t *testing.T) {
	t.Parallel()

	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		shell := shell
		t.Run(shell, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			root := NewRootContext(context.Background(), strings.NewReader(""), &output, &output)
			root.SetArgs([]string{"completion", shell})
			if err := root.Execute(); err != nil {
				t.Fatalf("completion %s: %v", shell, err)
			}
			if output.Len() < 100 || !strings.Contains(strings.ToLower(output.String()), "spas") {
				t.Fatalf("completion %s output looks incomplete:\n%s", shell, output.String())
			}
		})
	}
}

func TestVersionCommands(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"version"}, {"--version"}} {
		var output bytes.Buffer
		root := NewRootContext(context.Background(), strings.NewReader(""), &output, &output)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatalf("Execute(%v) error = %v", args, err)
		}
		if !strings.Contains(output.String(), "spas") && !strings.Contains(output.String(), "version") {
			t.Fatalf("Execute(%v) output = %q", args, output.String())
		}
	}

	for _, args := range [][]string{{"version", "--json"}, {"--json", "version"}} {
		var output bytes.Buffer
		root := NewRootContext(context.Background(), strings.NewReader(""), &output, &output)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatalf("Execute(%v) error = %v", args, err)
		}
		var payload struct {
			SchemaVersion int    `json:"schemaVersion"`
			Version       string `json:"version"`
			Commit        string `json:"commit"`
			Date          string `json:"date"`
		}
		if err := json.Unmarshal(output.Bytes(), &payload); err != nil {
			t.Fatalf("json.Unmarshal(%q) error = %v", output.String(), err)
		}
		if payload.SchemaVersion != app.JSONSchemaVersion {
			t.Fatalf("payload.SchemaVersion = %d, want %d", payload.SchemaVersion, app.JSONSchemaVersion)
		}
		if payload.Version != version.Version || payload.Commit != version.Commit || payload.Date != version.Date {
			t.Fatalf("payload = %+v, want version=%q commit=%q date=%q", payload, version.Version, version.Commit, version.Date)
		}
	}
}

func TestVerboseEmitsSafeDiagnosticsAndJSONSuppressesThem(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	var errOut bytes.Buffer
	root := NewRootContext(context.Background(), strings.NewReader(""), &out, &errOut)
	root.SetArgs([]string{"--verbose", "--repo", "workspace", "--git", "custom-git", "version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("verbose version: %v", err)
	}
	for _, value := range []string{`command=version`, `repo="workspace"`, `git="custom-git"`} {
		if !strings.Contains(errOut.String(), value) {
			t.Errorf("verbose diagnostics do not contain %q:\n%s", value, errOut.String())
		}
	}

	out.Reset()
	errOut.Reset()
	root = NewRootContext(context.Background(), strings.NewReader(""), &out, &errOut)
	root.SetArgs([]string{"--verbose", "--json", "version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("JSON verbose version: %v", err)
	}
	if errOut.Len() != 0 {
		t.Fatalf("JSON mode emitted verbose diagnostics: %q", errOut.String())
	}
}

func TestLinkDryRunValidatesTransportWithoutSaving(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	repository := filepath.Join(rootDir, "public")
	if err := os.MkdirAll(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "init", "-q", "-b", "main")

	var output bytes.Buffer
	root := NewRootContext(context.Background(), strings.NewReader(""), &output, &output)
	root.SetArgs([]string{
		"--repo", repository,
		"link", "getspas/private-files",
		"--transport", "ftp",
		"--dry-run",
		"--non-interactive",
	})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "transport") {
		t.Fatalf("link invalid transport error = %v", err)
	} else if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindInvalidUsage {
		t.Fatalf("link invalid transport kind = %v, %t; want invalid usage", kind, ok)
	}
}

func TestUnknownCommandClassifiesAsInvalidUsage(t *testing.T) {
	t.Parallel()

	err := classifyExecutionError(errors.New(`unknown command "wat" for "spas"`))
	if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindInvalidUsage || exitCode(err) != 2 {
		t.Fatalf("unknown command classification = %v, %t, exit %d", kind, ok, exitCode(err))
	}
}

func TestJSONModeIsRecognizedBeforeCommandResolution(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		args []string
		want bool
	}{
		{args: []string{"--json", "unknown"}, want: true},
		{args: []string{"unknown", "--json=true"}, want: true},
		{args: []string{"unknown", "--json=1"}, want: true},
		{args: []string{"--", "--json"}, want: false},
		{args: []string{"--json=false", "unknown"}, want: false},
		{args: []string{"--json=0", "unknown"}, want: false},
		{args: []string{"--json", "--json=false"}, want: false},
		{args: []string{"--json=false", "--json"}, want: true},
		{args: []string{"--json", "--", "--json=false"}, want: true},
	} {
		if got := jsonRequested(test.args); got != test.want {
			t.Errorf("jsonRequested(%v) = %t, want %t", test.args, got, test.want)
		}
	}
}

func TestVersionFlagIsRootOnly(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	root := NewRootContext(context.Background(), strings.NewReader(""), &output, &output)
	root.SetArgs([]string{"status", "--version"})
	err := root.Execute()
	if err == nil {
		t.Fatal("status --version error = nil, want root-only flag rejection")
	}
	if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindInvalidUsage {
		t.Fatalf("status --version kind = %v, %t; want invalid usage", kind, ok)
	}
}

func TestExitAndErrorCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err      error
		exit     int
		errorKey string
	}{
		{err: errors.New("operation"), exit: 1, errorKey: "operation_failed"},
		{err: context.DeadlineExceeded, exit: 1, errorKey: "operation_failed"},
		{err: context.Canceled, exit: 1, errorKey: "operation_failed"},
		{err: spaserr.Wrap(spaserr.KindInvalidUsage, errors.New("usage")), exit: 2, errorKey: "invalid_usage"},
		{err: linkstate.ErrNotLinked, exit: 3, errorKey: "not_linked"},
		{err: interaction.ErrDecisionRequired, exit: 4, errorKey: "decision_required"},
		{err: spaserr.Wrap(spaserr.KindPathConflict, errors.New("conflict")), exit: 5, errorKey: "path_conflict"},
		{err: app.ErrPrivateMergeConflict, exit: 6, errorKey: "private_merge_conflict"},
		{err: spaserr.Wrap(spaserr.KindAuthNetwork, errors.New("auth")), exit: 7, errorKey: "auth_or_network"},
		{err: spaserr.Wrap(spaserr.KindUnsafeGitState, errors.New("unsafe")), exit: 8, errorKey: "unsafe_git_state"},
		{err: spaserr.Wrap(spaserr.KindExclusionValidation, errors.New("exclusion")), exit: 9, errorKey: "exclusion_validation_failed"},
		{err: spaserr.Wrap(spaserr.KindLockHeld, errors.New("lock")), exit: 10, errorKey: "lock_held"},
		{err: spaserr.Wrap(spaserr.KindUnsupportedPath, errors.New("path")), exit: 11, errorKey: "unsupported_path"},
		{err: spaserr.Wrap(spaserr.KindInterrupted, errors.New("interrupted")), exit: 130, errorKey: "interrupted"},
	}
	for _, test := range tests {
		if got := exitCode(test.err); got != test.exit {
			t.Errorf("exitCode(%v) = %d, want %d", test.err, got, test.exit)
		}
		if got := errorCode(test.err); got != test.errorKey {
			t.Errorf("errorCode(%v) = %q, want %q", test.err, got, test.errorKey)
		}
	}
	// A wrapped typed error keeps its kind through further wrapping.
	wrapped := fmt.Errorf("outer context: %w", spaserr.Wrap(spaserr.KindLockHeld, errors.New("inner")))
	if got := exitCode(wrapped); got != 10 {
		t.Errorf("exitCode(wrapped) = %d, want 10", got)
	}
}

func TestResolveCommitMessage(t *testing.T) {
	t.Parallel()

	syncCommand := func(t *testing.T, args ...string) *cobra.Command {
		t.Helper()
		command := &cobra.Command{Use: "sync"}
		command.Flags().StringP("message", "m", "", "")
		command.Flags().String("message-file", "", "")
		if err := command.Flags().Parse(args); err != nil {
			t.Fatal(err)
		}
		return command
	}

	path := filepath.Join(t.TempDir(), "message.txt")
	if err := os.WriteFile(path, []byte("  Update managed architecture notes  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := syncCommand(t, "--message-file", path)
	if got, err := resolveCommitMessage(command, "", path); err != nil || got != "Update managed architecture notes" {
		t.Fatalf("resolveCommitMessage(file) = %q, %v", got, err)
	}
	command = syncCommand(t, "--message", "Inline reason")
	if got, err := resolveCommitMessage(command, "Inline reason", ""); err != nil || got != "Inline reason" {
		t.Fatalf("resolveCommitMessage(inline) = %q, %v", got, err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	command = syncCommand(t, "--message-file", missing)
	if _, err := resolveCommitMessage(command, "", missing); err == nil ||
		!strings.Contains(err.Error(), "read commit message file for linked repository") {
		t.Fatalf("resolveCommitMessage(missing) error = %v", err)
	}

	// A supplied-but-blank message from either source is invalid usage.
	blank := filepath.Join(t.TempDir(), "blank.txt")
	if err := os.WriteFile(blank, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command = syncCommand(t, "--message-file", blank)
	if _, err := resolveCommitMessage(command, "", blank); err == nil ||
		!strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("resolveCommitMessage(blank file) error = %v", err)
	}
	command = syncCommand(t, "--message", "   ")
	if _, err := resolveCommitMessage(command, "   ", ""); err == nil ||
		!strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("resolveCommitMessage(blank inline) error = %v", err)
	}
}

func TestTimeoutFlagRejectsNegativeDuration(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	root := NewRootContext(context.Background(), strings.NewReader(""), &output, &output)
	root.SetArgs([]string{"--timeout", "-5s", "version"})
	err := root.Execute()
	if err == nil {
		t.Fatal("Execute() with negative timeout error = nil, want error")
	}
	if kind, ok := spaserr.KindOf(err); !ok || kind != spaserr.KindInvalidUsage {
		t.Fatalf("Execute() with negative timeout error kind = %v, want KindInvalidUsage", kind)
	}
	if !strings.Contains(err.Error(), "--timeout cannot be negative") {
		t.Fatalf("Execute() error = %v, want negative timeout message", err)
	}
}

func TestTimeoutFlagSetsContextDeadline(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	root := NewRootContext(context.Background(), strings.NewReader(""), &output, &output)
	var observedDeadline time.Time
	var deadlineSet bool
	testCmd := &cobra.Command{
		Use: "test-timeout",
		RunE: func(cmd *cobra.Command, _ []string) error {
			observedDeadline, deadlineSet = cmd.Context().Deadline()
			return nil
		},
	}
	root.AddCommand(testCmd)
	root.SetArgs([]string{"--timeout", "5s", "test-timeout"})
	before := time.Now()
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !deadlineSet {
		t.Fatal("command context has no deadline set")
	}
	if observedDeadline.Before(before) || observedDeadline.After(before.Add(6*time.Second)) {
		t.Fatalf("observed deadline = %v, want within [now, now+5s]", observedDeadline)
	}
}

func TestTimeoutFlagExpiredDeadlineSurfacesOperationFailed(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	root := NewRootContext(context.Background(), strings.NewReader(""), &output, &output)
	testCmd := &cobra.Command{
		Use: "test-timeout-expire",
		RunE: func(cmd *cobra.Command, _ []string) error {
			<-cmd.Context().Done()
			return cmd.Context().Err()
		},
	}
	root.AddCommand(testCmd)
	root.SetArgs([]string{"--timeout", "10ms", "test-timeout-expire"})
	err := root.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want context deadline exceeded")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute() error = %v, want context.DeadlineExceeded", err)
	}
	if got := exitCode(err); got != 1 {
		t.Fatalf("exitCode(err) = %d, want 1", got)
	}
	if got := errorCode(err); got != "operation_failed" {
		t.Fatalf("errorCode(err) = %q, want operation_failed", got)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func TestDoctorCommandUnlinked(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	var output bytes.Buffer
	root := NewRootContext(context.Background(), strings.NewReader(""), &output, &output)
	root.SetArgs([]string{"doctor", "--repo", dir})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute(doctor) error = %v\n%s", err, output.String())
	}
	text := output.String()
	for _, expected := range []string{"git", "data-dirs", "lock", "ok", "workspace", "warning", "not a Git repository — link checks skipped:"} {
		if !strings.Contains(text, expected) {
			t.Errorf("output missing %q: %s", expected, text)
		}
	}

	output.Reset()
	root = NewRootContext(context.Background(), strings.NewReader(""), &output, &output)
	root.SetArgs([]string{"doctor", "--repo", dir, "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute(doctor --json) error = %v\n%s", err, output.String())
	}
	var result app.DoctorResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("decode doctor json: %v\n%s", err, output.String())
	}
	if result.SchemaVersion != app.JSONSchemaVersion || !result.Healthy || result.Errors != 0 || result.Warnings != 1 {
		t.Fatalf("doctor result = %#v, want healthy with 1 warning and schemaVersion %d", result, app.JSONSchemaVersion)
	}
	foundWorkspaceWarning := false
	for _, check := range result.Checks {
		if check.Name == "workspace" && check.Status == "warning" && strings.Contains(check.Message, "not a Git repository — link checks skipped:") {
			foundWorkspaceWarning = true
			break
		}
	}
	if !foundWorkspaceWarning {
		t.Fatalf("doctor result checks = %#v, want workspace warning", result.Checks)
	}
}
