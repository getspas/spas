package presentation

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestProgressLifecycleAndOutputOwnership(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var diagnostics, out synchronizedBuffer
		p := Presenter{progress: newProgress(context.Background(), &diagnostics, func() int { return 80 })}
		defer p.Stop()
		p.Phase("Snapshotting assets", 7)
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		if diagnostics.Len() != 0 {
			t.Fatalf("fast operation animated: %q", diagnostics.String())
		}
		time.Sleep(200 * time.Millisecond)
		synctest.Wait()
		if !strings.Contains(diagnostics.String(), "Snapshotting assets (7 files)") {
			t.Fatal(diagnostics.String())
		}
		fmt.Fprintln(p.Writer(&diagnostics), "warning: preserve this")
		if !strings.HasSuffix(diagnostics.String(), "\r\x1b[2Kwarning: preserve this\n") {
			t.Fatal(diagnostics.String())
		}
		p.Stop()
		fmt.Fprintln(p.Writer(&out), "Synchronized: true")
		time.Sleep(time.Second)
		synctest.Wait()
		if out.String() != "Synchronized: true\n" {
			t.Fatalf("result stdout: %q", out.String())
		}
		before := diagnostics.String()
		p.Phase("After completion", -1)
		time.Sleep(time.Second)
		synctest.Wait()
		if diagnostics.String() != before {
			t.Fatal("worker wrote after Stop")
		}
	})
}

func TestProgressSuspendsForPromptAndChild(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var out synchronizedBuffer
		p := Presenter{progress: newProgress(context.Background(), &out, func() int { return 80 })}
		defer p.Stop()
		p.Phase("Fetching assets", -1)
		time.Sleep(300 * time.Millisecond)
		synctest.Wait()
		resume := p.Pause(true)
		fmt.Fprintln(p.Writer(&out), "Git output")
		before := out.String()
		time.Sleep(time.Second)
		synctest.Wait()
		if out.String() != before || !strings.HasSuffix(before, "Fetching assets\nGit output\n") {
			t.Fatal(out.String())
		}
		resume()
		time.Sleep(300 * time.Millisecond)
		synctest.Wait()
		if out.String() == before {
			t.Fatal("did not resume after child")
		}
		resume = p.Pause(false)
		fmt.Fprint(p.Writer(&out), "Commit message: ")
		before = out.String()
		time.Sleep(time.Second)
		synctest.Wait()
		if out.String() != before {
			t.Fatal("animation overlapped prompt")
		}
		fmt.Fprintln(p.Writer(&out), "answer")
		resume()
		time.Sleep(300 * time.Millisecond)
		synctest.Wait()
		if out.String() == before+"answer\n" {
			t.Fatal("did not resume after prompt")
		}
	})
}

func TestProgressCancellationAndNarrowTerminal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, width := range []int{0, 2, 4, 12, 80} {
			var out synchronizedBuffer
			ctx, cancel := context.WithCancel(context.Background())
			p := Presenter{progress: newProgress(ctx, &out, func() int { return width })}
			p.Phase("Materializing assets", 42)
			time.Sleep(300 * time.Millisecond)
			synctest.Wait()
			frames := strings.Split(out.String(), "\r\x1b[2K")
			for _, frame := range frames {
				if len(frame) >= width && frame != "" {
					t.Fatalf("width %d: wrapped %q", width, frame)
				}
			}
			if width < 4 && out.Len() != 0 {
				t.Fatal("unusable terminal animated")
			}
			cancel()
			p.Stop()
			before := out.String()
			time.Sleep(time.Second)
			synctest.Wait()
			if out.String() != before {
				t.Fatal("animation survived cancellation")
			}
			if width >= 4 && !strings.HasSuffix(before, "\r\x1b[2K") {
				t.Fatal("frame not cleared")
			}
		}
	})
}

// Reads of captured frames share the same boundary as the renderer's writes.
type synchronizedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *synchronizedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(data)
}
func (b *synchronizedBuffer) Len() int { b.mu.Lock(); defer b.mu.Unlock(); return b.Buffer.Len() }
func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}
