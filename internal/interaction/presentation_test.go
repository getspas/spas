package interaction

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestPromptOwnsPresentationThroughInput(t *testing.T) {
	for _, call := range []func(Prompter) error{
		func(p Prompter) error { _, err := p.Confirm(t.Context(), "Proceed?", false, false); return err },
		func(p Prompter) error { _, err := p.Input(t.Context(), "Value:"); return err },
		func(p Prompter) error {
			_, err := p.Select(t.Context(), "Choice:", []Option{{Key: "y", Value: "yes", Label: "Yes"}})
			return err
		},
	} {
		paused, resumed := false, false
		p := Prompter{
			Interactive: true,
			Before:      func() func() { paused = true; return func() { paused = false; resumed = true } },
			Out:         ownedPromptWriter{t, &paused},
			In:          ownedPromptReader{t, &paused, strings.NewReader("y\n")},
		}
		if err := call(p); err != nil {
			t.Fatal(err)
		}
		if paused || !resumed {
			t.Fatal("presentation ownership was not released")
		}
	}
}

type ownedPromptWriter struct {
	t      *testing.T
	paused *bool
}

func (w ownedPromptWriter) Write(data []byte) (int, error) {
	if !*w.paused {
		w.t.Error("prompt wrote before pausing presentation")
	}
	return len(data), nil
}

type ownedPromptReader struct {
	t      *testing.T
	paused *bool
	reader io.Reader
}

func (r ownedPromptReader) Read(data []byte) (int, error) {
	if !*r.paused {
		r.t.Error("presentation resumed before prompt input finished")
	}
	return r.reader.Read(data)
}

func TestPromptCancellationReleasesPresentation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	resumed := false
	p := Prompter{Interactive: true, In: reader, Out: io.Discard, Before: func() func() { cancel(); return func() { resumed = true } }}
	if _, err := p.Input(ctx, "Value:"); err != context.Canceled || !resumed {
		t.Fatalf("error %v, resumed %t", err, resumed)
	}
}
