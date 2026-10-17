package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestColorFlagAndMachineOutput(t *testing.T) {
	for _, test := range []struct {
		name    string
		args    []string
		colored bool
	}{
		{"auto redirected", []string{"version"}, false},
		{"always", []string{"version", "--color=always"}, true},
		{"never", []string{"version", "--color=never"}, false},
		{"explicit override", []string{"version", "--non-interactive", "--color=always"}, true},
		{"json", []string{"version", "--json", "--color=always"}, false},
		{"completion", []string{"completion", "bash", "--color=always", "--verbose"}, false},
		{"completion protocol", []string{"__complete", "--color=always", "--verbose", ""}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", "1")
			t.Setenv("CI", "true")
			t.Setenv("TERM", "dumb")
			var out, diagnostics bytes.Buffer
			root := NewRootContext(context.Background(), strings.NewReader(""), &out, &diagnostics)
			root.SetArgs(test.args)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(out.String(), "\x1b["); got != test.colored {
				t.Fatalf("colored = %t, want %t: %q", got, test.colored, out.String())
			}
			if strings.Contains(diagnostics.String(), "\x1b") {
				t.Fatalf("decorated machine diagnostics: %q", diagnostics.String())
			}
			if test.name == "json" && !json.Valid(out.Bytes()) {
				t.Fatalf("invalid JSON: %q", out.String())
			}
		})
	}
}

func TestInvalidColorIsUsageError(t *testing.T) {
	for _, args := range [][]string{{"version", "--color=bad"}, {"--color=", "version"}, {"--color=bad", "--help"}, {"--color=bad", "--version"}} {
		code, out, diagnostics := executeCaptured(t, args)
		if code != 2 || len(out) != 0 || !bytes.Contains(diagnostics, []byte("auto, always, never")) {
			t.Fatalf("%v: code %d, stdout %q, stderr %q", args, code, out, diagnostics)
		}
	}
}

func TestColorErrorsAndVerboseDiagnostics(t *testing.T) {
	for _, test := range []struct {
		args  []string
		code  int
		label string
	}{
		{[]string{"version", "extra", "--color=always"}, 2, "\x1b[31merror:\x1b[0m"},
		{[]string{"version", "--timeout=-1s", "--color=always"}, 2, "\x1b[31merror:\x1b[0m"},
		{[]string{"version", "--verbose", "--color=always"}, 0, "\x1b[36mspas:\x1b[0m"},
	} {
		code, _, diagnostics := executeCaptured(t, test.args)
		if code != test.code || !bytes.Contains(diagnostics, []byte(test.label)) {
			t.Fatalf("%v: code %d, stderr %q", test.args, code, diagnostics)
		}
	}
	code, out, diagnostics := executeCaptured(t, []string{"version", "extra", "--json", "--color=always"})
	if code != 2 || len(out) != 0 || bytes.Contains(diagnostics, []byte("\x1b")) || !json.Valid(diagnostics) {
		t.Fatalf("JSON error: code %d, stdout %q, stderr %q", code, out, diagnostics)
	}
}
