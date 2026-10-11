package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getspas/spas/internal/spaserr"
)

func TestLinkPublicRepositoryWarnsWithoutApproval(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace := initializePublicRepository(t, root)
	remote := filepath.Join(root, "assets.git")
	instance, out := testApp(t, workspace, root, remote)
	instance.Provider = testRepositoryProvider{remoteURL: remote, isPublic: true}
	var warnings bytes.Buffer
	instance.Err = &warnings
	if err := instance.Link(context.Background(), LinkOptions{Repository: "getspas/public-assets", Branch: "main"}); err != nil {
		t.Fatalf("Link(public, non-interactive): %v", err)
	}
	if !strings.Contains(warnings.String(), "warning:") || !strings.Contains(warnings.String(), "publicly readable") {
		t.Fatalf("stderr lacks public readability warning: %q", warnings.String())
	}
	if strings.Contains(out.String(), "publicly readable") || strings.Contains(out.String(), "Continue?") {
		t.Fatalf("stdout contains warning or prompt: %q", out.String())
	}
}

func TestLinkPublicRepositoryJSONWarningSurvivesLaterFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace := initializePublicRepository(t, root)
	instance, out := testApp(t, workspace, root, filepath.Join(root, "assets.git"))
	instance.Provider = testRepositoryProvider{remoteURL: filepath.Join(root, "assets.git"), isPublic: true}
	instance.JSON = true
	var warnings bytes.Buffer
	instance.Err = &warnings
	options := LinkOptions{Repository: "getspas/public-assets", Branch: "main"}
	for _, wantFailure := range []bool{false, true} {
		out.Reset()
		warnings.Reset()
		err := instance.Link(context.Background(), options)
		if (err != nil) != wantFailure {
			t.Fatalf("Link() error = %v, want failure %v", err, wantFailure)
		}
		var record struct {
			SchemaVersion int `json:"schemaVersion"`
			Warning       struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"warning"`
		}
		if err := json.Unmarshal(warnings.Bytes(), &record); err != nil {
			t.Fatalf("warning is not one JSON object: %v, %q", err, warnings.String())
		}
		if record.SchemaVersion != 2 || record.Warning.Code != "public_repository" || !strings.Contains(record.Warning.Message, "publicly readable") {
			t.Fatalf("warning record = %+v", record)
		}
		if wantFailure {
			if out.Len() != 0 {
				t.Fatalf("failed link wrote result: %q", out.String())
			}
		} else {
			var result map[string]any
			if err := json.Unmarshal(out.Bytes(), &result); err != nil || result["networkAccess"] != true || result["schemaVersion"] != float64(2) {
				t.Fatalf("result = %q, decode error %v", out.String(), err)
			}
		}
	}
}

func TestSyncPublicRepositoryWarnsOnEveryRun(t *testing.T) {
	t.Parallel()
	_, remote, instance := initializedApp(t, t.TempDir())
	var calls int
	instance.Provider = testRepositoryProvider{remoteURL: remote, isPublic: true, probeCalls: &calls}
	var warnings bytes.Buffer
	instance.Err = &warnings
	for i := 0; i < 2; i++ {
		if err := instance.Sync(context.Background(), SyncOptions{Conflict: ConflictAbort, ExistingExclude: ExcludePreserve, MergeProtection: MergeEnable}); err != nil {
			t.Fatalf("Sync(public, non-interactive) run %d: %v", i, err)
		}
	}
	if calls != 2 || strings.Count(warnings.String(), "publicly readable") != 2 {
		t.Fatalf("got %d probes, warnings %q; want two of each", calls, warnings.String())
	}
}

func TestVisibilityProbeOutcomes(t *testing.T) {
	t.Parallel()
	operationalError := errors.New("visibility probe execution failed")
	for _, sync := range []bool{false, true} {
		for _, probeError := range []error{nil, context.Canceled, operationalError} {
			t.Run(fmt.Sprintf("sync=%v/error=%v", sync, probeError), func(t *testing.T) {
				root := t.TempDir()
				workspace := initializePublicRepository(t, root)
				remote := filepath.Join(root, "assets.git")
				instance, out := testApp(t, workspace, root, remote)
				if sync {
					workspace, remote, instance = initializedApp(t, t.TempDir())
					out = instance.Out.(*bytes.Buffer)
				}
				var calls int
				instance.Provider = testRepositoryProvider{remoteURL: remote, probeErr: probeError, probeCalls: &calls}
				var warnings bytes.Buffer
				instance.Err = &warnings
				out.Reset()
				var err error
				if sync {
					err = instance.Sync(context.Background(), SyncOptions{Conflict: ConflictAbort, ExistingExclude: ExcludePreserve, MergeProtection: MergeEnable})
				} else {
					err = instance.Link(context.Background(), LinkOptions{Repository: "getspas/assets", Branch: "main"})
				}
				if !errors.Is(err, probeError) || calls != 1 || warnings.Len() != 0 {
					t.Fatalf("operation error %v, probes %d, warning %q; want %v, one probe, no warning", err, calls, warnings.String(), probeError)
				}
				if strings.Contains(out.String(), "private visibility") || (probeError != nil && out.Len() != 0) {
					t.Fatalf("unexpected result: %q", out.String())
				}
			})
		}
	}
}

func TestPublicLinkThenSyncRechecksReadabilityAndDryRunsSkipProbe(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	workspace := initializePublicRepository(t, root)
	remote := filepath.Join(root, "assets.git")
	createRemoteWithInitialCommit(t, root, remote)
	instance, out := testApp(t, workspace, root, remote)
	var calls int
	instance.Provider = testRepositoryProvider{remoteURL: remote, isPublic: true, probeCalls: &calls}
	var warnings bytes.Buffer
	instance.Err = &warnings
	for _, dryRun := range []bool{true, false} {
		if err := instance.Link(ctx, LinkOptions{Repository: "getspas/assets", Branch: "main", DryRun: dryRun}); err != nil {
			t.Fatal(err)
		}
	}
	for _, dryRun := range []bool{true, false, false} {
		if err := instance.Sync(ctx, SyncOptions{Conflict: ConflictAbort, ExistingExclude: ExcludePreserve, MergeProtection: MergeEnable, DryRun: dryRun}); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 || strings.Count(warnings.String(), "publicly readable") != 3 {
		t.Fatalf("got %d probes and warnings %q; want three", calls, warnings.String())
	}
	if !strings.Contains(out.String(), "networkAccess: false") || !strings.Contains(out.String(), "networkAccess: true") {
		t.Fatalf("link network reporting: %q", out.String())
	}
}

func TestSyncJSONWarningPrecedesMissingCommitApproval(t *testing.T) {
	t.Parallel()
	workspace, remote, instance := initializedApp(t, t.TempDir())
	instance.Provider = testRepositoryProvider{remoteURL: remote, isPublic: true}
	instance.JSON = true
	var out, warnings bytes.Buffer
	instance.Out, instance.Err = &out, &warnings
	if err := os.WriteFile(filepath.Join(workspace, "docs", "ARCHITECTURE.md"), []byte("new work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := instance.Sync(context.Background(), SyncOptions{Conflict: ConflictAbort, ExistingExclude: ExcludePreserve, MergeProtection: MergeEnable})
	kind, _ := spaserr.KindOf(err)
	if kind != spaserr.KindDecisionRequired || out.Len() != 0 || !json.Valid(warnings.Bytes()) {
		t.Fatalf("Sync error %v, stdout %q, stderr %q", err, out.String(), warnings.String())
	}
}
