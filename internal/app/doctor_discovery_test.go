package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorReportsCorruptRepositoryConfiguration(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace := initializePublicRepository(t, root)
	if err := os.WriteFile(filepath.Join(workspace, ".git", "config"), []byte("[unterminated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	instance, output := testApp(t, workspace, root, "")
	instance.JSON = true
	if err := instance.Doctor(t.Context()); err == nil {
		t.Error("Doctor returned success for corrupt Git configuration")
	}
	var result DoctorResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Healthy || result.Errors == 0 {
		t.Fatalf("Doctor health = %s", output.String())
	}
	for _, check := range result.Checks {
		if check.Name == "workspace" && check.Status == "error" && strings.Contains(check.Message, "bad config") {
			return
		}
	}
	t.Fatalf("Doctor omitted the configuration failure: %s", output.String())
}

func TestDoctorFailedDiscoveryPreservesOutputError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	instance, _ := testApp(t, filepath.Join(root, "missing"), root, "")
	writeErr := errors.New("diagnostic output unavailable")
	instance.Out = failedDiffWriter{err: writeErr}
	for _, jsonOutput := range []bool{false, true} {
		instance.JSON = jsonOutput
		if err := instance.Doctor(t.Context()); !errors.Is(err, writeErr) {
			t.Fatalf("Doctor output error = %v", err)
		}
	}
}

func TestDoctorCanceledInspectionIsUnhealthy(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	instance, output := testApp(t, root, root, "")
	instance.JSON = true
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := instance.Doctor(ctx); err == nil {
		t.Fatal("Doctor ignored cancellation")
	}
	var result DoctorResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	for _, check := range result.Checks {
		if check.Name == "workspace" && check.Status == "error" {
			return
		}
	}
	t.Fatalf("canceled inspection was not reported as an error: %s", output.String())
}
