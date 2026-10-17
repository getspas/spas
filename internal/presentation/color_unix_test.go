//go:build !windows

package presentation

import (
	"io"
	"testing"
)

func TestUnixTerminalCapabilities(t *testing.T) {
	for _, test := range []struct {
		term      string
		supported bool
	}{
		{"", false}, {"dumb", false}, {"unknown", false},
		{"xterm", true}, {"xterm-256color", true}, {"screen-256color", true},
	} {
		t.Setenv("TERM", test.term)
		if got := supportsAutomaticColor(io.Discard); got != test.supported {
			t.Fatalf("TERM=%q: supported = %t, want %t", test.term, got, test.supported)
		}
	}
}
