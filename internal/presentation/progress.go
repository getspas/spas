package presentation

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/getspas/spas/internal/interaction"
	"golang.org/x/term"
)

// ProgressMode controls motion independently of color.
type ProgressMode string

func (m ProgressMode) String() string {
	if m == "" {
		return "auto"
	}
	return string(m)
}
func (m *ProgressMode) Set(value string) error {
	switch value {
	case "auto", "never":
		*m = ProgressMode(value)
		return nil
	}
	return fmt.Errorf("progress must be one of: auto, never")
}
func (ProgressMode) Type() string { return "string" }

// StartProgress attaches a command-scoped worker only to a supported stderr.
// Call Stop before returning from the command, including error paths.
func (p *Presenter) StartProgress(ctx context.Context, mode ProgressMode, nonInteractive, machine bool, stream io.Writer) {
	if mode == "never" || nonInteractive || machine || os.Getenv("CI") != "" || !interaction.IsTerminal(stream) || !supportsAutomaticColor(stream) {
		return
	}
	file := stream.(*os.File)
	p.progress = newProgress(ctx, stream, func() int {
		width, _, err := term.GetSize(int(file.Fd()))
		if err != nil {
			return 0
		}
		return width
	})
}

// Phase reports a real operation; count is the known number of files, or -1.
// Phase names are application-owned ASCII text, never paths or child output.
func (p Presenter) Phase(name string, count int) {
	if p.progress == nil {
		return
	}
	if count >= 0 {
		unit := "files"
		if count == 1 {
			unit = "file"
		}
		name = fmt.Sprintf("%s (%d %s)", name, count, unit)
	}
	g := p.progress
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped {
		return
	}
	g.clear()
	g.phase = name
	g.since = time.Now()
	g.stable = false
}

// Pause clears motion for a prompt or child operation. Git gets a stable phase
// line, once per phase, until its terminal ownership ends.
func (p Presenter) Pause(stable bool) func() {
	if p.progress == nil {
		return func() {}
	}
	g := p.progress
	g.mu.Lock()
	g.clear()
	g.paused++
	if stable && !g.stopped && !g.stable && g.phase != "" && !g.lineOpen {
		if text := g.fit(g.phase); text != "" {
			_, _ = fmt.Fprintln(g.out, text)
		}
		g.stable = true
	}
	g.mu.Unlock()
	return func() {
		g.mu.Lock()
		g.paused--
		if !stable {
			g.lineOpen = false
		}
		g.since = time.Now()
		g.mu.Unlock()
	}
}

// Stop joins the animation worker and clears its last frame. It is idempotent.
func (p Presenter) Stop() {
	if p.progress == nil {
		return
	}
	p.progress.cancel()
	<-p.progress.done
}

// Writer serializes SPAS and forwarded child output with animation. Capability
// and prompt detection must happen on the original streams before wrapping.
func (p Presenter) Writer(stream io.Writer) io.Writer {
	if p.progress == nil {
		return stream
	}
	return progressWriter{p.progress, stream}
}

type progressWriter struct {
	progress *progress
	out      io.Writer
}

func (w progressWriter) Write(data []byte) (int, error) {
	g := w.progress
	g.mu.Lock()
	defer g.mu.Unlock()
	g.clear()
	n, err := w.out.Write(data)
	if n > 0 {
		g.lineOpen = data[n-1] != '\n'
	}
	g.since = time.Now()
	return n, err
}

type progress struct {
	mu                                 sync.Mutex
	out                                io.Writer
	width                              func() int
	cancel                             context.CancelFunc
	done                               chan struct{}
	phase                              string
	since                              time.Time
	frame                              int
	paused                             int
	visible, stable, stopped, lineOpen bool
}

func newProgress(parent context.Context, out io.Writer, width func() int) *progress {
	ctx, cancel := context.WithCancel(parent)
	g := &progress{out: out, width: width, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(g.done)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		defer func() { g.mu.Lock(); g.clear(); g.stopped = true; g.mu.Unlock() }()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				g.mu.Lock()
				if ctx.Err() == nil && g.phase != "" && g.paused == 0 && !g.lineOpen && time.Since(g.since) >= 200*time.Millisecond {
					text := g.fit(string("|/-\\"[g.frame%4]) + " " + g.phase)
					if text != "" {
						_, _ = fmt.Fprint(g.out, "\r\x1b[2K"+text)
						g.visible = true
						g.frame++
					}
				}
				g.mu.Unlock()
			}
		}
	}()
	return g
}

func (g *progress) clear() {
	if g.visible {
		_, _ = fmt.Fprint(g.out, "\r\x1b[2K")
		g.visible = false
	}
}
func (g *progress) fit(text string) string {
	width := g.width() - 1 // Avoid wrapping at the terminal's final column.
	if width < 3 {
		return ""
	}
	if len(text) > width {
		return text[:width-3] + "..."
	}
	return text
}
