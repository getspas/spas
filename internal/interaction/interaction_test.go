package interaction

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestConfirmInteractiveChoicesAndDefaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		recommended bool
		want        bool
	}{
		{name: "recommended default", input: "\n", recommended: true, want: true},
		{name: "safe default", input: "\n", recommended: false, want: false},
		{name: "yes", input: "yes\n", want: true},
		{name: "no", input: "n\n", recommended: true, want: false},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			prompt := Prompter{
				In:          strings.NewReader(test.input),
				Out:         &output,
				Interactive: true,
			}
			got, err := prompt.Confirm(context.Background(), "Proceed?", test.recommended, false)
			if err != nil {
				t.Fatalf("Confirm() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("Confirm() = %t, want %t", got, test.want)
			}
			if !strings.Contains(output.String(), "Proceed?") {
				t.Fatalf("prompt output = %q", output.String())
			}
		})
	}
}

func TestConfirmAssumeYesOnlyAppliesToRecommendedSetup(t *testing.T) {
	t.Parallel()

	prompt := Prompter{AssumeYes: true}
	got, err := prompt.Confirm(context.Background(), "Enable protection?", true, true)
	if err != nil || !got {
		t.Fatalf("Confirm() = %t, %v, want accepted recommendation", got, err)
	}
	_, err = prompt.Confirm(context.Background(), "Create private commit?", false, false)
	if !errors.Is(err, ErrDecisionRequired) {
		t.Fatalf("Confirm() error = %v, want ErrDecisionRequired", err)
	}
}

func TestNonInteractivePromptsRequireExplicitDecision(t *testing.T) {
	t.Parallel()

	prompt := Prompter{Interactive: false}
	if _, err := prompt.Input(context.Background(), "Repository:"); !errors.Is(err, ErrDecisionRequired) {
		t.Fatalf("Input() error = %v, want ErrDecisionRequired", err)
	}
	if _, err := prompt.Select(context.Background(), "Conflict:", []Option{{Key: "a", Value: "abort"}}); !errors.Is(err, ErrDecisionRequired) {
		t.Fatalf("Select() error = %v, want ErrDecisionRequired", err)
	}
}

func TestInputAndSelect(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	input := Prompter{In: strings.NewReader("  getspas/private-files  \n"), Out: &output, Interactive: true}
	value, err := input.Input(context.Background(), "Repository:")
	if err != nil {
		t.Fatalf("Input() error = %v", err)
	}
	if value != "getspas/private-files" {
		t.Fatalf("Input() = %q", value)
	}

	output.Reset()
	selectPrompt := Prompter{In: strings.NewReader("override\n"), Out: &output, Interactive: true}
	value, err = selectPrompt.Select(context.Background(), "Conflict:", []Option{
		{Key: "s", Value: "skip", Label: "Skip"},
		{Key: "o", Value: "override", Label: "Override"},
	})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if value != "override" {
		t.Fatalf("Select() = %q", value)
	}
}

func TestInteractivePromptRejectsInvalidOrEmptyInput(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	confirm := Prompter{
		In: strings.NewReader("maybe\n"), Out: &output, Interactive: true,
	}
	if _, err := confirm.Confirm(context.Background(), "Proceed?", false, false); err == nil {
		t.Fatal("Confirm() error = nil, want invalid-choice error")
	}
	input := Prompter{
		In: strings.NewReader("\n"), Out: &output, Interactive: true,
	}
	if _, err := input.Input(context.Background(), "Value:"); err == nil {
		t.Fatal("Input() error = nil, want empty-value error")
	}
	selectPrompt := Prompter{
		In: strings.NewReader("x\n"), Out: &output, Interactive: true,
	}
	if _, err := selectPrompt.Select(context.Background(), "Choice:", []Option{{Key: "a", Value: "abort", Label: "Abort"}}); err == nil {
		t.Fatal("Select() error = nil, want invalid-choice error")
	}
}

func TestPromptsReturnWhenContextIsCanceledWhileReading(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		call func(Prompter, context.Context) error
	}{
		{
			name: "confirm",
			call: func(prompt Prompter, ctx context.Context) error {
				_, err := prompt.Confirm(ctx, "Proceed?", false, false)
				return err
			},
		},
		{
			name: "input",
			call: func(prompt Prompter, ctx context.Context) error {
				_, err := prompt.Input(ctx, "Value:")
				return err
			},
		},
		{
			name: "select",
			call: func(prompt Prompter, ctx context.Context) error {
				_, err := prompt.Select(ctx, "Choice:", []Option{{Key: "a", Value: "abort", Label: "Abort"}})
				return err
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			started := make(chan struct{})
			prompt := Prompter{
				In:          reader,
				Out:         promptSignalWriter{started: started},
				Interactive: true,
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- test.call(prompt, ctx) }()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("prompt did not start")
			}
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("prompt error = %v, want context.Canceled", err)
				}
			case <-time.After(time.Second):
				t.Fatal("prompt did not return after cancellation")
			}
		})
	}
}

type promptSignalWriter struct {
	started chan struct{}
}

func (w promptSignalWriter) Write(value []byte) (int, error) {
	select {
	case <-w.started:
	default:
		close(w.started)
	}
	return len(value), nil
}

func TestDetectIsNonInteractiveForNonTerminalStreams(t *testing.T) {
	t.Parallel()

	prompt := Detect(strings.NewReader(""), &bytes.Buffer{}, false)
	if prompt.Interactive {
		t.Fatal("Detect() marked ordinary streams interactive")
	}
}
