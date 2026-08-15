package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/getspas/spas/internal/interaction"
)

func TestApproveCommitRequiresExplicitInteractiveApprovalAndReason(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	instance := App{
		Out: &output,
		Prompt: interaction.Prompter{
			In:          strings.NewReader("y\nDocument the architecture decision\n"),
			Out:         &output,
			Interactive: true,
		},
	}
	message, err := instance.approveCommit(
		context.Background(),
		[]plannedChange{{Path: "docs/ARCHITECTURE.md", Status: "M"}},
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if message != "Document the architecture decision" {
		t.Fatalf("approveCommit() message = %q", message)
	}
	if !strings.Contains(output.String(), "M  docs/ARCHITECTURE.md") ||
		!strings.Contains(output.String(), "Create this commit in the linked repository?") {
		t.Fatalf("approval output is incomplete:\n%s", output.String())
	}
}

func TestApproveCommitNeedsMessageInNonInteractiveMode(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	instance := App{
		Out: &output,
		Prompt: interaction.Prompter{
			In:          strings.NewReader(""),
			Out:         &output,
			Interactive: false,
		},
	}
	changes := []plannedChange{{Path: ".env", Status: "A"}}
	if _, err := instance.approveCommit(context.Background(), changes, ""); !errors.Is(err, interaction.ErrDecisionRequired) {
		t.Fatalf("approveCommit(no message) error = %v", err)
	}
	if message, err := instance.approveCommit(context.Background(), changes, "  Add development environment  "); err != nil ||
		message != "Add development environment" {
		t.Fatalf("approveCommit(supplied) = %q, %v", message, err)
	}
	if message, err := instance.approveCommit(context.Background(), nil, "unused"); err != nil || message != "" {
		t.Fatalf("approveCommit(no changes) = %q, %v", message, err)
	}
}

func TestApproveCommitDeclineStopsTheOperation(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	instance := App{
		Out: &output,
		Prompt: interaction.Prompter{
			In:          strings.NewReader("n\n"),
			Out:         &output,
			Interactive: true,
		},
	}
	_, err := instance.approveCommit(context.Background(), []plannedChange{{Path: ".env", Status: "M"}}, "")
	if err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("approveCommit(decline) error = %v", err)
	}
}
