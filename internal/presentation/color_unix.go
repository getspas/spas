//go:build !windows

package presentation

import (
	"io"
	"os"
	"strings"
)

func supportsColor(io.Writer) bool { return true }

func supportsAutomaticColor(io.Writer) bool {
	terminal := os.Getenv("TERM")
	for _, family := range []string{"ansi", "linux", "xterm", "screen", "tmux", "rxvt", "cygwin", "alacritty", "foot", "wezterm"} {
		if terminal == family || strings.HasPrefix(terminal, family+"-") {
			return true
		}
	}
	return false
}
