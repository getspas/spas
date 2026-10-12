package gitexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/getspas/spas/internal/limits"
)

type Runner struct {
	Path           string
	NonInteractive bool
	Stdin          io.Reader
	Stdout         io.Writer
	Stderr         io.Writer
	// BeforeTerminal suspends presentation through child exit and stream draining.
	BeforeTerminal func() func()
	// NoLazyFetch is enabled only for the SPAS-managed full private clone.
	// Public repositories may be partial clones whose ordinary reads need to
	// retrieve missing promisor objects.
	NoLazyFetch     bool
	NoOptionalLocks bool
}

type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

type ExitError struct {
	Operation string
	ExitCode  int
	Stdout    string
	Stderr    string
}

// OutputLimitError reports a captured Git stream that exceeded the SPAS
// supported size. The child is canceled immediately and the truncated bytes
// are never returned as successful command output.
type OutputLimitError struct {
	Operation string
	Stream    string
	Limit     int64
}

func (e *OutputLimitError) Error() string {
	return fmt.Sprintf(
		"git %s exceeded the SPAS captured %s limit of %d bytes",
		e.Operation,
		e.Stream,
		e.Limit,
	)
}

func (e *ExitError) Error() string {
	detail := e.Stderr
	if detail == "" {
		detail = e.Stdout
	}
	if detail == "" {
		return fmt.Sprintf("git %s failed with exit code %d", e.Operation, e.ExitCode)
	}
	return fmt.Sprintf("git %s failed: %s", e.Operation, detail)
}

func (r Runner) Run(ctx context.Context, dir string, args ...string) (Result, error) {
	return r.run(ctx, dir, false, args...)
}

func (r Runner) RunStreaming(ctx context.Context, dir string, args ...string) (Result, error) {
	return r.run(ctx, dir, true, args...)
}

func (r Runner) RunInput(ctx context.Context, dir string, input io.Reader, args ...string) (Result, error) {
	return r.runWithInput(ctx, dir, false, input, args...)
}

func (r Runner) run(ctx context.Context, dir string, stream bool, args ...string) (Result, error) {
	return r.runWithInput(ctx, dir, stream, nil, args...)
}

func (r Runner) runWithInput(ctx context.Context, dir string, stream bool, input io.Reader, args ...string) (Result, error) {
	operation := operationName(args)
	remote := remoteOperation(operation)
	if r.BeforeTerminal != nil && (stream || remote) {
		defer r.BeforeTerminal()()
	}
	if r.NonInteractive {
		var err error
		args, err = r.unattendedArgs(ctx, dir, args)
		if err != nil {
			return Result{}, err
		}
	}
	path := r.Path
	if path == "" {
		path = "git"
	}

	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, path, args...)
	configureCancellation(cmd, r.NonInteractive)
	cmd.Dir = dir
	cmd.Env = r.commandEnvironment(os.Environ())
	cmd.WaitDelay = limits.GitCommandWaitDelay

	var stdout outputBuffer
	var stderr outputBuffer
	if stream {
		stdout = newTailBuffer(limits.StreamedGitDiagnosticBytes)
		stderr = newTailBuffer(limits.StreamedGitDiagnosticBytes)
		if input != nil {
			cmd.Stdin = input
		} else if !r.NonInteractive {
			if r.Stdin != nil {
				cmd.Stdin = r.Stdin
			} else {
				cmd.Stdin = os.Stdin
			}
		}
		cmd.Stdout = io.MultiWriter(stdout, writerOr(r.Stdout, io.Discard))
		cmd.Stderr = io.MultiWriter(stderr, writerOr(r.Stderr, io.Discard))
	} else {
		limitState := &outputLimitState{
			cancel:    cancel,
			operation: operationName(args),
		}
		stdout = newLimitedBuffer(
			"stdout",
			limits.MaxCapturedGitStdoutBytes,
			limitState,
		)
		stderr = newLimitedBuffer(
			"stderr",
			limits.MaxCapturedGitStderrBytes,
			limitState,
		)
		cmd.Stdin = input
		cmd.Stdout = stdout
		cmd.Stderr = stderr
	}

	err := cmd.Run()
	result := Result{
		Stdout: stdout.Bytes(),
		Stderr: stderr.Bytes(),
	}
	if limitErr := outputLimitError(stdout, stderr); limitErr != nil {
		return Result{}, limitErr
	}
	if err == nil {
		return result, nil
	}
	if ctx.Err() != nil {
		return result, fmt.Errorf("git %s: %w", operationName(args), ctx.Err())
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, &ExitError{
			Operation: operationName(args),
			ExitCode:  result.ExitCode,
			Stdout:    string(bytes.TrimSpace(result.Stdout)),
			Stderr:    string(bytes.TrimSpace(result.Stderr)),
		}
	}

	return result, fmt.Errorf("start git %s: %w", operationName(args), err)
}

type outputBuffer interface {
	io.Writer
	Bytes() []byte
}

type outputLimitReporter interface {
	LimitError() *OutputLimitError
}

type outputLimitState struct {
	once      sync.Once
	cancel    context.CancelFunc
	operation string
	err       *OutputLimitError
}

func (s *outputLimitState) exceed(stream string, limit int64) {
	s.once.Do(func() {
		s.err = &OutputLimitError{
			Operation: s.operation,
			Stream:    stream,
			Limit:     limit,
		}
		s.cancel()
	})
}

type limitedBuffer struct {
	buffer bytes.Buffer
	stream string
	limit  int64
	state  *outputLimitState
}

func newLimitedBuffer(stream string, limit int64, state *outputLimitState) *limitedBuffer {
	return &limitedBuffer{stream: stream, limit: limit, state: state}
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	total := len(value)
	remaining := b.limit - int64(b.buffer.Len())
	if remaining > 0 {
		writeLength := int64(len(value))
		if writeLength > remaining {
			writeLength = remaining
		}
		_, _ = b.buffer.Write(value[:writeLength])
	}
	if int64(total) > remaining {
		b.state.exceed(b.stream, b.limit)
	}
	return total, nil
}

func (b *limitedBuffer) Bytes() []byte {
	return b.buffer.Bytes()
}

func (b *limitedBuffer) LimitError() *OutputLimitError {
	return b.state.err
}

type tailBuffer struct {
	data   []byte
	offset int
	full   bool
}

func newTailBuffer(size int) *tailBuffer {
	return &tailBuffer{data: make([]byte, size)}
}

func (b *tailBuffer) Write(value []byte) (int, error) {
	total := len(value)
	if len(b.data) == 0 {
		return total, nil
	}
	if len(value) >= len(b.data) {
		copy(b.data, value[len(value)-len(b.data):])
		b.offset = 0
		b.full = true
		return total, nil
	}
	for len(value) > 0 {
		written := copy(b.data[b.offset:], value)
		value = value[written:]
		b.offset = (b.offset + written) % len(b.data)
		if b.offset == 0 {
			b.full = true
		}
	}
	return total, nil
}

func (b *tailBuffer) Bytes() []byte {
	if !b.full {
		return append([]byte{}, b.data[:b.offset]...)
	}
	result := make([]byte, 0, len(b.data))
	result = append(result, b.data[b.offset:]...)
	result = append(result, b.data[:b.offset]...)
	return result
}

func outputLimitError(buffers ...outputBuffer) *OutputLimitError {
	for _, buffer := range buffers {
		reporter, ok := buffer.(outputLimitReporter)
		if !ok {
			continue
		}
		if err := reporter.LimitError(); err != nil {
			return err
		}
	}
	return nil
}

func (r Runner) commandEnvironment(environment []string) []string {
	if r.NonInteractive {
		filtered := make([]string, 0, len(environment))
		for _, entry := range environment {
			key, _, _ := strings.Cut(entry, "=")
			switch strings.ToUpper(key) {
			case "GIT_TERMINAL_PROMPT", "GIT_ASKPASS", "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE", "GCM_INTERACTIVE":
				continue
			}
			filtered = append(filtered, entry)
		}
		environment = append(filtered, "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=", "SSH_ASKPASS_REQUIRE=never", "GCM_INTERACTIVE=never")
	}
	result := append(
		sanitizedEnvironment(environment),
		"LC_ALL=C",
		"GIT_ATTR_NOSYSTEM=1",
		// Replacement refs can make the tree inspected by SPAS differ from the
		// object IDs that are fetched or pushed. Always inspect the actual Git
		// objects, regardless of refs/replace entries in a local clone.
		"GIT_NO_REPLACE_OBJECTS=1",
	)
	if r.NoLazyFetch {
		// The SPAS-managed private repository is always created as a full clone.
		// Do not let later local configuration turn its security-sensitive reads
		// into implicit network access.
		result = append(result, "GIT_NO_LAZY_FETCH=1")
	}
	if r.NoOptionalLocks {
		result = append(result, "GIT_OPTIONAL_LOCKS=0")
	}
	return result
}

// unattendedArgs controls Git and the default OpenSSH transport without
// replacing a user's custom SSH command. Custom transports must be configured
// for unattended use by their owner.
func (r Runner) unattendedArgs(ctx context.Context, dir string, args []string) ([]string, error) {
	operation := operationName(args)
	if !remoteOperation(operation) {
		return args, nil
	}
	controls := []string{"-c", "credential.interactive=false"}
	if operation != "credential" && os.Getenv("GIT_SSH_COMMAND") == "" && os.Getenv("GIT_SSH") == "" && os.Getenv("GIT_SSH_VARIANT") == "" {
		prefix := args[:operationIndex(args)]
		lookup := append(append([]string{}, prefix...), "config", "--get-regexp", `^(core\.sshcommand|ssh\.variant)$`)
		_, err := r.Run(ctx, dir, lookup...)
		if code, ok := ExitCode(err); ok && code == 1 {
			controls = append(controls, "-c", "core.sshCommand=ssh -oBatchMode=yes", "-c", "ssh.variant=ssh")
		} else if err != nil {
			return nil, fmt.Errorf("inspect SSH configuration: %w", err)
		}
	}
	index := operationIndex(args)
	result := append([]string{}, args[:index]...)
	result = append(result, controls...)
	return append(result, args[index:]...), nil
}

func sanitizedEnvironment(environment []string) []string {
	blocked := map[string]struct{}{
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": {},
		"GIT_COMMON_DIR":                   {},
		"GIT_CONFIG":                       {},
		"GIT_CONFIG_COUNT":                 {},
		"GIT_CONFIG_KEY_0":                 {},
		"GIT_CONFIG_PARAMETERS":            {},
		"GIT_CONFIG_VALUE_0":               {},
		"GIT_DIR":                          {},
		"GIT_EXEC_PATH":                    {},
		"GIT_GRAFT_FILE":                   {},
		"GIT_INDEX_FILE":                   {},
		"GIT_NAMESPACE":                    {},
		"GIT_NO_LAZY_FETCH":                {},
		"GIT_NO_REPLACE_OBJECTS":           {},
		"GIT_OPTIONAL_LOCKS":               {},
		"GIT_OBJECT_DIRECTORY":             {},
		"GIT_QUARANTINE_PATH":              {},
		"GIT_REPLACE_REF_BASE":             {},
		"GIT_SHALLOW_FILE":                 {},
		"GIT_WORK_TREE":                    {},
	}
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		normalizedKey := strings.ToUpper(key)
		if _, found := blocked[normalizedKey]; found ||
			strings.HasPrefix(normalizedKey, "GIT_CONFIG_KEY_") ||
			strings.HasPrefix(normalizedKey, "GIT_CONFIG_VALUE_") {
			continue
		}
		result = append(result, entry)
	}
	return result
}

func writerOr(value, fallback io.Writer) io.Writer {
	if value != nil {
		return value
	}
	return fallback
}

func operationName(args []string) string {
	if index := operationIndex(args); index < len(args) {
		return args[index]
	}
	return "command"
}

func operationIndex(args []string) int {
	skipNext := false
	for index, arg := range args {
		if skipNext {
			skipNext = false
			continue
		}
		switch arg {
		case "-C", "--git-dir", "--work-tree", "-c", "--config-env", "--namespace", "--exec-path":
			skipNext = true
			continue
		}
		if strings.HasPrefix(arg, "--") && strings.Contains(arg, "=") {
			continue
		}
		if len(arg) > 0 && arg[0] != '-' {
			return index
		}
	}
	return len(args)
}

func ExitCode(err error) (int, bool) {
	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		return 0, false
	}
	return exitErr.ExitCode, true
}

func remoteOperation(operation string) bool {
	switch operation {
	case "clone", "fetch", "push", "ls-remote", "credential":
		return true
	default:
		return false
	}
}
