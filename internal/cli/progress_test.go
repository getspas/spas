package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestProgressFlagAndPlainOutput(t *testing.T) {
	for _, args := range [][]string{
		{"version", "--progress=auto"}, {"version", "--progress=never"},
		{"version", "--progress=auto", "--json"},
		{"completion", "bash", "--progress=auto"},
		{"__complete", "--progress=auto", ""},
	} {
		var out, diagnostics bytes.Buffer
		root := NewRootContext(context.Background(), strings.NewReader(""), &out, &diagnostics)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(out.Bytes(), []byte("\x1b")) || bytes.Contains(diagnostics.Bytes(), []byte("\x1b")) {
			t.Fatalf("decorated output: %q %q", out.String(), diagnostics.String())
		}
	}
}

func TestInvalidProgressIsUsageError(t *testing.T) {
	for _, args := range [][]string{{"version", "--progress=always"}, {"--progress=", "version"}, {"--progress=bad", "--help"}, {"--progress=bad", "--version"}} {
		code, out, diagnostics := executeCaptured(t, args)
		if code != 2 || len(out) != 0 || !bytes.Contains(diagnostics, []byte("auto, never")) {
			t.Fatalf("%v: code %d, stdout %q, stderr %q", args, code, out, diagnostics)
		}
	}
}
