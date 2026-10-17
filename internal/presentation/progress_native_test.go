package presentation

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/getspas/spas/internal/gitexec"
	"github.com/getspas/spas/internal/interaction"
)

// Run the compiled test binary on a terminal to exercise the native boundary.
func TestNativeTerminalProgressPolicy(t *testing.T) {
	if !interaction.IsTerminal(os.Stdout) {
		t.Skip("stdout is not a native terminal")
	}
	t.Setenv("TERM", "xterm-256color")
	if !supportsAutomaticColor(os.Stdout) {
		t.Skip("terminal lacks cursor capabilities")
	}
	for _, test := range []struct {
		name                             string
		mode                             ProgressMode
		term, ci, noColor                string
		nonInteractive, machine, enabled bool
	}{
		{"auto", "auto", "xterm-256color", "", "", false, false, true},
		{"never", "never", "xterm-256color", "", "", false, false, false},
		{"NO_COLOR", "auto", "xterm-256color", "", "1", false, false, true},
		{"CI", "auto", "xterm-256color", "1", "", false, false, false},
		{"dumb", "auto", "dumb", "", "", false, false, false},
		{"non-interactive", "auto", "xterm-256color", "", "", true, false, false},
		{"machine", "auto", "xterm-256color", "", "", false, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TERM", test.term)
			t.Setenv("CI", test.ci)
			t.Setenv("NO_COLOR", test.noColor)
			p := New("never", test.nonInteractive, test.machine, os.Stdout, os.Stdout)
			p.StartProgress(t.Context(), test.mode, test.nonInteractive, test.machine, os.Stdout)
			defer p.Stop()
			if (p.progress != nil) != test.enabled {
				t.Fatalf("progress enabled = %t, want %t", p.progress != nil, test.enabled)
			}
			var redirected bytes.Buffer
			p = New("always", false, false, os.Stdout, &redirected)
			p.StartProgress(t.Context(), test.mode, false, false, &redirected)
			defer p.Stop()
			if p.progress != nil {
				t.Fatal("terminal stdout enabled redirected progress")
			}
		})
	}
}

func TestNativeTerminalProgressLifecycle(t *testing.T) {
	if !interaction.IsTerminal(os.Stdout) {
		t.Skip("stdout is not a native terminal")
	}
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("CI", "")
	t.Setenv("NO_COLOR", "1")
	if !supportsAutomaticColor(os.Stdout) {
		t.Skip("terminal lacks cursor capabilities")
	}
	var result bytes.Buffer
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	p := New("never", false, false, &result, os.Stdout)
	p.StartProgress(ctx, "auto", false, false, os.Stdout)
	defer p.Stop()
	p.Phase("Inspecting assets", 3)
	time.Sleep(350 * time.Millisecond)
	fmt.Fprintln(p.Writer(os.Stdout), "warning: example diagnostic")
	prompt := interaction.Prompter{Interactive: true, In: strings.NewReader("answer\n"), Out: p.Writer(os.Stdout), Before: func() func() { return p.Pause(false) }}
	if _, err := prompt.Input(ctx, "Message:"); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(p.Writer(os.Stdout), "answer")
	p.Phase("Reading Git version", -1)
	runner := gitexec.Runner{NonInteractive: true, Stdout: p.Writer(os.Stdout), Stderr: p.Writer(os.Stdout), BeforeTerminal: func() func() { return p.Pause(true) }}
	if _, err := runner.RunStreaming(ctx, "", "--version"); err != nil {
		t.Fatal(err)
	}
	p.Phase("Waiting for cancellation", -1)
	time.Sleep(350 * time.Millisecond)
	cancel()
	p.Stop()
	fmt.Fprintln(p.Writer(&result), "Result: complete")
	if result.String() != "Result: complete\n" {
		t.Fatal(result.String())
	}
}
