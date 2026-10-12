package presentation

import (
	"io"
	"os"

	"github.com/getspas/spas/internal/interaction"
	"golang.org/x/sys/windows"
)

func supportsColor(stream io.Writer) bool {
	if !interaction.IsTerminal(stream) {
		return true
	} // Explicit color in redirected output.
	file := stream.(*os.File)
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(file.Fd()), &mode) == nil && mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0
}

func supportsAutomaticColor(stream io.Writer) bool {
	return os.Getenv("TERM") != "dumb" && supportsColor(stream)
}
