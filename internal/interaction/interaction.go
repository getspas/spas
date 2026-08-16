package interaction

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

var ErrDecisionRequired = errors.New("user decision required")

type Prompter struct {
	In          io.Reader
	Out         io.Writer
	Interactive bool
	AssumeYes   bool
}

func Detect(in io.Reader, out io.Writer, nonInteractive bool) Prompter {
	interactive := !nonInteractive && isTerminal(in) && isTerminalWriter(out)
	return Prompter{In: in, Out: out, Interactive: interactive}
}

func (p Prompter) Confirm(ctx context.Context, question string, recommended bool, allowAssumeYes bool) (bool, error) {
	if err := context.Cause(ctx); err != nil {
		return false, err
	}
	if p.AssumeYes && allowAssumeYes {
		return recommended, nil
	}
	if !p.Interactive {
		return false, fmt.Errorf("%w: %s", ErrDecisionRequired, question)
	}
	suffix := " [y/N]: "
	if recommended {
		suffix = " [Y/n]: "
	}
	if _, err := fmt.Fprint(p.Out, question+suffix); err != nil {
		return false, err
	}
	value, err := readLineContext(ctx, p.In)
	if err != nil {
		return false, err
	}
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return recommended, nil
	}
	switch value {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, fmt.Errorf("expected yes or no")
	}
}

func (p Prompter) Input(ctx context.Context, question string) (string, error) {
	if err := context.Cause(ctx); err != nil {
		return "", err
	}
	if !p.Interactive {
		return "", fmt.Errorf("%w: %s", ErrDecisionRequired, question)
	}
	if _, err := fmt.Fprint(p.Out, question+"\n> "); err != nil {
		return "", err
	}
	value, err := readLineContext(ctx, p.In)
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("a non-empty value is required")
	}
	return value, nil
}

func (p Prompter) Select(ctx context.Context, question string, options []Option) (string, error) {
	if err := context.Cause(ctx); err != nil {
		return "", err
	}
	if !p.Interactive {
		return "", fmt.Errorf("%w: %s", ErrDecisionRequired, question)
	}
	if _, err := fmt.Fprintln(p.Out, question); err != nil {
		return "", err
	}
	for _, option := range options {
		if _, err := fmt.Fprintf(p.Out, "  [%s] %s\n", option.Key, option.Label); err != nil {
			return "", err
		}
	}
	if _, err := fmt.Fprint(p.Out, "Choice: "); err != nil {
		return "", err
	}
	value, err := readLineContext(ctx, p.In)
	if err != nil {
		return "", err
	}
	value = strings.ToLower(strings.TrimSpace(value))
	for _, option := range options {
		if value == strings.ToLower(option.Key) || value == strings.ToLower(option.Value) {
			return option.Value, nil
		}
	}
	return "", fmt.Errorf("invalid choice %q", value)
}

type Option struct {
	Key   string
	Value string
	Label string
}

func readLineContext(ctx context.Context, reader io.Reader) (string, error) {
	type result struct {
		value string
		err   error
	}
	results := make(chan result, 1)
	go func() {
		value, err := readLine(reader)
		results <- result{value: value, err: err}
	}()
	select {
	case result := <-results:
		return result.value, result.err
	case <-ctx.Done():
		return "", context.Cause(ctx)
	}
}

func readLine(reader io.Reader) (string, error) {
	var value strings.Builder
	var buffer [1]byte
	for {
		count, err := reader.Read(buffer[:])
		if count == 1 {
			if buffer[0] == '\n' {
				return value.String(), nil
			}
			value.WriteByte(buffer[0])
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return value.String(), nil
			}
			return "", err
		}
	}
}

func isTerminal(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func isTerminalWriter(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}
