package gitexec

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/getspas/spas/internal/limits"
)

func TestRunCapturesOutput(t *testing.T) {
	t.Parallel()

	result, err := (Runner{}).Run(context.Background(), t.TempDir(), "--version")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(result.Stdout) == 0 {
		t.Fatal("Run() returned empty stdout")
	}
}

func TestSanitizedEnvironmentRemovesGitRepositoryOverrides(t *testing.T) {
	t.Parallel()

	environment := []string{
		"PATH=/bin",
		"GIT_CONFIG_GLOBAL=/home/user/.gitconfig",
		"GIT_CONFIG_SYSTEM=/etc/gitconfig",
		"GIT_DIR=/tmp/other",
		"GIT_CONFIG_PARAMETERS='core.hooksPath=/tmp/hooks'",
		"GIT_CONFIG_KEY_4=core.fsmonitor",
		"GIT_CONFIG_VALUE_4=/tmp/monitor",
		"GIT_NO_LAZY_FETCH=0",
		"GIT_NO_REPLACE_OBJECTS=0",
		"git_work_tree=/tmp/worktree",
	}
	got := sanitizedEnvironment(environment)
	joined := strings.Join(got, "\n")
	if joined != "PATH=/bin\nGIT_CONFIG_GLOBAL=/home/user/.gitconfig\nGIT_CONFIG_SYSTEM=/etc/gitconfig" {
		t.Fatalf("sanitizedEnvironment() = %q", joined)
	}
}

func TestCommandEnvironmentScopesNoLazyFetchToManagedPrivateRunner(t *testing.T) {
	t.Parallel()

	base := []string{"PATH=/bin", "GIT_NO_LAZY_FETCH=0"}
	public := strings.Join((Runner{}).commandEnvironment(base), "\n")
	if strings.Contains(public, "GIT_NO_LAZY_FETCH=") {
		t.Fatalf("public runner environment contains lazy-fetch override: %q", public)
	}
	private := strings.Join((Runner{NoLazyFetch: true}).commandEnvironment(base), "\n")
	if !strings.Contains(private, "GIT_NO_LAZY_FETCH=1") {
		t.Fatalf("private runner environment = %q, want GIT_NO_LAZY_FETCH=1", private)
	}
}

func TestCommandEnvironmentDisablesOptionalLocksWhenRequested(t *testing.T) {
	t.Parallel()

	base := []string{"PATH=/bin", "GIT_OPTIONAL_LOCKS=1"}
	public := strings.Join((Runner{}).commandEnvironment(base), "\n")
	if strings.Contains(public, "GIT_OPTIONAL_LOCKS=") {
		t.Fatalf("public runner environment contains optional-lock override: %q", public)
	}
	readOnly := strings.Join((Runner{NoOptionalLocks: true}).commandEnvironment(base), "\n")
	if !strings.Contains(readOnly, "GIT_OPTIONAL_LOCKS=0") {
		t.Fatalf("read-only runner environment = %q, want GIT_OPTIONAL_LOCKS=0", readOnly)
	}
}

func TestLimitedBufferAcceptsLimitAndCancelsOnNextByte(t *testing.T) {
	t.Parallel()

	canceled := false
	state := &outputLimitState{
		cancel:    func() { canceled = true },
		operation: "test",
	}
	buffer := newLimitedBuffer("stdout", 4, state)
	if _, err := buffer.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if canceled || buffer.LimitError() != nil {
		t.Fatal("exact-limit write canceled the command")
	}
	if _, err := buffer.Write([]byte("5")); err != nil {
		t.Fatal(err)
	}
	var limitErr *OutputLimitError
	if !errors.As(buffer.LimitError(), &limitErr) {
		t.Fatalf("LimitError() = %v, want OutputLimitError", buffer.LimitError())
	}
	if !canceled {
		t.Fatal("over-limit write did not cancel the command")
	}
	if got := string(buffer.Bytes()); got != "1234" {
		t.Fatalf("captured bytes = %q, want exact bounded prefix", got)
	}
}

func TestRunCancelsChildAndRejectsTruncatedCapturedOutput(t *testing.T) {
	input, keepOpen, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer keepOpen.Close()
	t.Setenv("SPAS_GITEXEC_HELPER", "capture-overflow")

	ctx, cancel := context.WithTimeout(context.Background(), testCommandTimeout)
	defer cancel()
	result, err := (Runner{Path: os.Args[0]}).RunInput(
		ctx,
		t.TempDir(),
		input,
		"-test.run=^TestGitExecHelperProcess$",
	)
	var limitErr *OutputLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("RunInput() error = %v, want OutputLimitError", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("parent context ended before child cancellation: %v", ctx.Err())
	}
	if len(result.Stdout) != 0 || len(result.Stderr) != 0 {
		t.Fatalf("RunInput() returned truncated output: stdout=%d stderr=%d", len(result.Stdout), len(result.Stderr))
	}
}

func TestRunCancelsChildOnStderrCaptureLimit(t *testing.T) {
	input, keepOpen, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer keepOpen.Close()
	t.Setenv("SPAS_GITEXEC_HELPER", "stderr-overflow")

	ctx, cancel := context.WithTimeout(context.Background(), testCommandTimeout)
	defer cancel()
	result, err := (Runner{Path: os.Args[0]}).RunInput(
		ctx,
		t.TempDir(),
		input,
		"-test.run=^TestGitExecHelperProcess$",
	)
	var limitErr *OutputLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("RunInput() error = %v, want OutputLimitError", err)
	}
	if limitErr.Stream != "stderr" {
		t.Fatalf("OutputLimitError.Stream = %q, want stderr", limitErr.Stream)
	}
	if ctx.Err() != nil {
		t.Fatalf("parent context ended before child cancellation: %v", ctx.Err())
	}
	if len(result.Stdout) != 0 || len(result.Stderr) != 0 {
		t.Fatalf("RunInput() returned truncated output: stdout=%d stderr=%d", len(result.Stdout), len(result.Stderr))
	}
}

func TestRunBoundsInheritedPipeWaitAfterCaptureLimit(t *testing.T) {
	t.Setenv("SPAS_GITEXEC_HELPER", "overflow-with-inherited-pipes")
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	t.Setenv("SPAS_GITEXEC_GRANDCHILD_PID", pidFile)

	started := time.Now()
	result, err := (Runner{Path: os.Args[0]}).Run(
		context.Background(),
		t.TempDir(),
		"-test.run=^TestGitExecHelperProcess$",
	)
	elapsed := time.Since(started)
	var limitErr *OutputLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("Run() error = %v, want OutputLimitError", err)
	}
	if len(result.Stdout) != 0 || len(result.Stderr) != 0 {
		t.Fatalf("Run() returned truncated output: stdout=%d stderr=%d", len(result.Stdout), len(result.Stderr))
	}
	if elapsed > limits.GitCommandWaitDelay+2*time.Second {
		t.Fatalf("Run() waited %s for inherited pipes, want at most %s plus scheduling allowance", elapsed, limits.GitCommandWaitDelay)
	}

	rawPID, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("read grandchild PID: %v", readErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	if parseErr != nil {
		t.Fatalf("parse grandchild PID: %v", parseErr)
	}
	if process, findErr := os.FindProcess(pid); findErr == nil {
		_ = process.Kill()
	}
}

func TestRunStreamingForwardsAllOutputAndRetainsFixedTail(t *testing.T) {
	t.Setenv("SPAS_GITEXEC_HELPER", "stream-output")

	var streamed bytes.Buffer
	result, err := (Runner{
		Path:   os.Args[0],
		Stdout: &streamed,
	}).RunStreaming(
		context.Background(),
		t.TempDir(),
		"-test.run=^TestGitExecHelperProcess$",
	)
	if err != nil {
		t.Fatalf("RunStreaming() error = %v", err)
	}
	want := helperStreamOutput()
	if !bytes.Equal(streamed.Bytes(), want) {
		t.Fatalf("streamed %d bytes, want exact %d-byte output", streamed.Len(), len(want))
	}
	wantTail := want[len(want)-limits.StreamedGitDiagnosticBytes:]
	if !bytes.Equal(result.Stdout, wantTail) {
		t.Fatalf("retained stdout tail = %d bytes, want exact %d-byte suffix", len(result.Stdout), len(wantTail))
	}
}

func TestTailBufferRetainsExactSuffixAcrossWraps(t *testing.T) {
	t.Parallel()

	buffer := newTailBuffer(5)
	_, _ = buffer.Write([]byte("12"))
	_, _ = buffer.Write([]byte("3456"))
	_, _ = buffer.Write([]byte("789"))
	if got := string(buffer.Bytes()); got != "56789" {
		t.Fatalf("tail = %q, want %q", got, "56789")
	}
}

const testCommandTimeout = 5 * time.Second

func TestGitExecHelperProcess(t *testing.T) {
	switch os.Getenv("SPAS_GITEXEC_HELPER") {
	case "":
		return
	case "capture-overflow":
		writeRepeated(os.Stdout, limits.MaxCapturedGitStdoutBytes+1)
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case "stderr-overflow":
		writeRepeated(os.Stderr, limits.MaxCapturedGitStderrBytes+1)
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case "stream-output":
		_, _ = os.Stdout.Write(helperStreamOutput())
		os.Exit(0)
	case "overflow-with-inherited-pipes":
		command := exec.Command(os.Args[0], "-test.run=^TestGitExecHelperProcess$")
		command.Env = make([]string, 0, len(os.Environ())+1)
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "SPAS_GITEXEC_HELPER=") {
				command.Env = append(command.Env, value)
			}
		}
		command.Env = append(command.Env, "SPAS_GITEXEC_HELPER=inherited-pipe-grandchild")
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Start(); err != nil {
			os.Exit(4)
		}
		if err := os.WriteFile(
			os.Getenv("SPAS_GITEXEC_GRANDCHILD_PID"),
			[]byte(strconv.Itoa(command.Process.Pid)),
			0o600,
		); err != nil {
			os.Exit(5)
		}
		writeRepeated(os.Stdout, limits.MaxCapturedGitStdoutBytes+1)
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case "inherited-pipe-grandchild":
		time.Sleep(4 * limits.GitCommandWaitDelay)
		os.Exit(0)
	default:
		os.Exit(2)
	}
}

func helperStreamOutput() []byte {
	result := make([]byte, limits.MaxCapturedGitStdoutBytes+257)
	for index := range result {
		result[index] = byte(index % 251)
	}
	return result
}

func writeRepeated(output io.Writer, count int) {
	chunk := bytes.Repeat([]byte{'x'}, 32<<10)
	for count > 0 {
		size := len(chunk)
		if size > count {
			size = count
		}
		if _, err := output.Write(chunk[:size]); err != nil {
			os.Exit(3)
		}
		count -= size
	}
}

func TestExitCode(t *testing.T) {
	t.Parallel()

	_, err := (Runner{}).Run(context.Background(), t.TempDir(), "rev-parse", "--verify", "missing")
	if err == nil {
		t.Fatal("Run() error = nil, want non-nil")
	}
	code, ok := ExitCode(err)
	if !ok || code == 0 {
		t.Fatalf("ExitCode() = (%d, %v), want non-zero true", code, ok)
	}
}
