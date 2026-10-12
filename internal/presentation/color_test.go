package presentation

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/getspas/spas/internal/interaction"
)

func TestRedirectedColorPolicy(t *testing.T) {
	for _, mode := range []ColorMode{"auto", "always", "never"} {
		for _, machine := range []bool{false, true} {
			var out, diagnostics bytes.Buffer
			p := New(mode, true, machine, &out, &diagnostics)
			colored := mode == "always" && !machine
			for _, text := range []string{p.Out("Result:", Result), p.Err("warning:", Warning), p.Err("error:", Error)} {
				if strings.Contains(text, "\x1b[") != colored {
					t.Fatalf("mode %s, machine %t: %q", mode, machine, text)
				}
			}
		}
	}
	if got := (Presenter{}).Out("Result:", Result); got != "Result:" {
		t.Fatalf("zero value: %q", got)
	}
}

// Run with terminal stdout to exercise native detection; ordinary test capture
// cannot establish terminal capabilities.
func TestNativeTerminalColorPolicy(t *testing.T) {
	if !interaction.IsTerminal(os.Stdout) {
		t.Skip("stdout is not a native terminal")
	}
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("CI", "")
	t.Setenv("NO_COLOR", "")
	if !supportsAutomaticColor(os.Stdout) {
		t.Skip("native terminal does not support color")
	}
	for _, test := range []struct {
		name                             string
		mode                             ColorMode
		term, ci, noColor                string
		nonInteractive, machine, colored bool
	}{
		{"auto", "auto", "xterm-256color", "", "", false, false, true},
		{"non-interactive", "auto", "xterm-256color", "", "", true, false, false},
		{"CI", "auto", "xterm-256color", "true", "", false, false, false},
		{"NO_COLOR", "auto", "xterm-256color", "", "1", false, false, false},
		{"empty NO_COLOR", "auto", "xterm-256color", "", "", false, false, true},
		{"dumb", "auto", "dumb", "", "", false, false, false},
		{"always overrides", "always", "dumb", "true", "1", true, false, true},
		{"never", "never", "xterm-256color", "", "", false, false, false},
		{"machine", "always", "xterm-256color", "", "", false, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TERM", test.term)
			t.Setenv("CI", test.ci)
			t.Setenv("NO_COLOR", test.noColor)
			var redirected bytes.Buffer
			p := New(test.mode, test.nonInteractive, test.machine, &redirected, os.Stdout)
			if got := strings.Contains(p.Err("warning:", Warning), "\x1b["); got != test.colored {
				t.Fatalf("terminal colored = %t, want %t", got, test.colored)
			}
			if got := strings.Contains(p.Out("Result:", Result), "\x1b["); got != (test.mode == "always" && !test.machine) {
				t.Fatalf("redirected stdout colored = %t", got)
			}
			p = New(test.mode, test.nonInteractive, test.machine, os.Stdout, &redirected)
			if got := strings.Contains(p.Out("Result:", Result), "\x1b["); got != test.colored {
				t.Fatalf("terminal stdout colored = %t, want %t", got, test.colored)
			}
		})
	}
}
