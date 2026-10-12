package gitexec

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestStreamingOwnsPresentationUntilOutputDrained(t *testing.T) {
	t.Setenv("SPAS_GITEXEC_HELPER", "stream-output")
	paused, resumed := false, false
	var out bytes.Buffer
	runner := Runner{
		Path:           os.Args[0],
		BeforeTerminal: func() func() { paused = true; return func() { paused = false; resumed = true } },
		Stdout:         ownedStreamWriter{t, &paused, &out},
	}
	result, err := runner.RunStreaming(t.Context(), t.TempDir(), "-test.run=^TestGitExecHelperProcess$")
	if err != nil {
		t.Fatal(err)
	}
	if paused || !resumed || !bytes.Equal(out.Bytes(), helperStreamOutput()) || len(result.Stdout) == 0 {
		t.Fatalf("paused %t, resumed %t, streamed %d bytes", paused, resumed, out.Len())
	}
}

type ownedStreamWriter struct {
	t      *testing.T
	paused *bool
	out    *bytes.Buffer
}

func (w ownedStreamWriter) Write(data []byte) (int, error) {
	if !*w.paused {
		w.t.Error("child output arrived without terminal ownership")
	}
	return w.out.Write(data)
}

func TestChildFailureAndCancellationReleasePresentation(t *testing.T) {
	for _, helper := range []string{"sleep", ""} {
		t.Setenv("SPAS_GITEXEC_HELPER", helper)
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		resumed := false
		runner := Runner{Path: os.Args[0], BeforeTerminal: func() func() { return func() { resumed = true } }}
		args := []string{"-test.run=^TestGitExecHelperProcess$"}
		if helper == "" {
			runner.Path = "missing-git-executable"
		}
		_, err := runner.RunStreaming(ctx, t.TempDir(), args...)
		cancel()
		if err == nil || !resumed {
			t.Fatalf("helper %q: error %v, resumed %t", helper, err, resumed)
		}
		if helper == "sleep" && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	}
}

func TestCapturedRemoteOperationOwnsPresentation(t *testing.T) {
	paused, resumed := false, false
	_, err := (Runner{Path: "missing-git-executable", BeforeTerminal: func() func() { paused = true; return func() { paused = false; resumed = true } }}).Run(t.Context(), t.TempDir(), "ls-remote", "https://example.invalid/assets.git")
	if err == nil || paused || !resumed {
		t.Fatalf("error %v, paused %t, resumed %t", err, paused, resumed)
	}
}
