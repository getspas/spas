// Package presentation owns human-readable terminal styling and progress.
package presentation

import (
	"fmt"
	"io"
	"os"

	"github.com/getspas/spas/internal/interaction"
)

// ColorMode is also the CLI flag value, so invalid values fail during parsing.
type ColorMode string

func (m ColorMode) String() string {
	if m == "" {
		return "auto"
	}
	return string(m)
}

func (m *ColorMode) Set(value string) error {
	switch value {
	case "auto", "always", "never":
		*m = ColorMode(value)
		return nil
	default:
		return fmt.Errorf("color must be one of: auto, always, never")
	}
}

func (ColorMode) Type() string { return "string" }

type Style int

const (
	Result Style = iota
	Success
	Warning
	Error
)

// Presenter coordinates SPAS text, prompts, and forwarded Git output.
// Its zero value produces plain text.
type Presenter struct {
	outColor bool
	errColor bool
	progress *progress
}

func New(mode ColorMode, nonInteractive, machine bool, out, errOut io.Writer) Presenter {
	automatic := !nonInteractive && os.Getenv("CI") == "" && os.Getenv("NO_COLOR") == ""
	return Presenter{
		outColor: colorEnabled(mode, machine, automatic, out),
		errColor: colorEnabled(mode, machine, automatic, errOut),
	}
}

func colorEnabled(mode ColorMode, machine, automatic bool, stream io.Writer) bool {
	if machine || mode == "never" {
		return false
	}
	if mode == "always" {
		return supportsColor(stream)
	}
	return automatic && interaction.IsTerminal(stream) && supportsAutomaticColor(stream)
}

func (p Presenter) Out(text string, style Style) string { return styled(text, style, p.outColor) }
func (p Presenter) Err(text string, style Style) string { return styled(text, style, p.errColor) }

// GitColor makes the destination policy explicit for Git's forwarded diff output.
func (p Presenter) GitColor() string {
	if p.outColor {
		return "always"
	}
	return "never"
}

func styled(text string, style Style, enabled bool) string {
	if !enabled {
		return text
	}
	codes := [...]string{"36", "32", "33", "31"}
	return "\x1b[" + codes[style] + "m" + text + "\x1b[0m"
}
