package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getspas/spas/internal/spaserr"
)

func TestExclusionRepairWarnings(t *testing.T) {
	t.Parallel()
	for _, jsonMode := range []bool{false, true} {
		for _, damage := range []string{"missing-file", "missing-block", "edited", "unrecognized", "malformed", "duplicate"} {
			for _, operation := range []string{"add", "remove", "sync", "unlink"} {
				t.Run(operation+"/"+damage+map[bool]string{false: "/human", true: "/json"}[jsonMode], func(t *testing.T) {
					t.Parallel()
					ctx := context.Background()
					root := t.TempDir()
					workspace := initializePublicRepository(t, root)
					remote := filepath.Join(root, "assets.git")
					runGit(t, root, "init", "--bare", "-q", "-b", "main", remote)
					instance, out := testApp(t, workspace, root, remote)
					var stderr bytes.Buffer
					instance.Err = &stderr
					if err := instance.Link(ctx, LinkOptions{Repository: "getspas/assets", Branch: "main"}); err != nil {
						t.Fatal(err)
					}
					for _, name := range []string{"asset.env", "retained.env", "new.env"} {
						if err := os.WriteFile(filepath.Join(workspace, name), []byte("asset\n"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					if err := instance.Add(ctx, AddOptions{Paths: []string{"asset.env", "retained.env"}, ExistingExclude: ExcludePreserve, MergeProtection: MergeEnable}); err != nil {
						t.Fatal(err)
					}
					if stderr.Len() != 0 {
						t.Fatalf("ordinary enrollment warned: %s", stderr.String())
					}
					state := loadState(t, instance, workspace)
					path := filepath.Join(workspace, ".git", "info", "exclude")
					original, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					start := strings.Index(string(original), "# BEGIN SPAS link=")
					block := string(original[start:])
					outside := "# outside\r\nother-tool/\r\n"
					content := outside + block
					switch damage {
					case "missing-file":
						if err := os.Remove(path); err != nil {
							t.Fatal(err)
						}
					case "missing-block":
						content = outside
					case "edited":
						content = strings.Replace(content, "/asset.env", "/not-enrolled.env", 1)
					case "unrecognized":
						content = strings.ReplaceAll(content, "SPAS link=", "SPAS edited-link=")
					case "malformed":
						content = strings.Replace(content, "# END SPAS link="+state.Exclude.BlockID+"\n", "", 1)
					case "duplicate":
						content += block
					}
					if damage != "missing-file" {
						if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					out.Reset()
					stderr.Reset()
					instance.JSON = jsonMode
					switch operation {
					case "add":
						err = instance.Add(ctx, AddOptions{Paths: []string{"new.env"}, ExistingExclude: ExcludePreserve, MergeProtection: MergeEnable})
					case "remove":
						err = instance.Remove(ctx, RemoveOptions{Paths: []string{"asset.env"}})
					case "sync":
						err = instance.Sync(ctx, SyncOptions{Message: "Save assets", Conflict: ConflictAbort, ExistingExclude: ExcludePreserve, MergeProtection: MergeEnable})
					case "unlink":
						err = instance.Unlink(ctx, UnlinkOptions{Force: true})
					}
					if damage == "malformed" || damage == "duplicate" {
						kind, _ := spaserr.KindOf(err)
						if kind != spaserr.KindExclusionValidation || !strings.Contains(err.Error(), "manually") || !strings.Contains(err.Error(), state.Exclude.BlockID) {
							t.Fatalf("error = %v", err)
						}
						after, readErr := os.ReadFile(path)
						if readErr != nil || string(after) != content || stderr.Len() != 0 {
							t.Fatalf("failed operation changed exclude or warned: %q, %v, %s", after, readErr, stderr.String())
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					// Removing an absent block does not repair anything during unlink.
					wantWarning := operation != "unlink" || damage == "edited"
					if wantWarning {
						if jsonMode {
							var record struct {
								SchemaVersion int     `json:"schemaVersion"`
								Warning       Warning `json:"warning"`
							}
							if err := json.Unmarshal(stderr.Bytes(), &record); err != nil || record.SchemaVersion != 3 || record.Warning.Code != "exclusion_drift_repaired" {
								t.Fatalf("warning = %s, %v", stderr.String(), err)
							}
						} else if !strings.Contains(stderr.String(), "warning: Reconciled unexpected changes") {
							t.Fatalf("warning = %s", stderr.String())
						}
					} else if stderr.Len() != 0 {
						t.Fatalf("unexpected warning = %s", stderr.String())
					}
					if bytes.Contains(out.Bytes(), []byte("Reconciled unexpected")) || bytes.Contains(stderr.Bytes(), []byte("\x1b")) {
						t.Fatal("warning stream contamination")
					}
					after, readErr := os.ReadFile(path)
					if damage != "missing-file" && (readErr != nil || !bytes.HasPrefix(after, []byte(outside))) {
						t.Fatalf("outside rules changed: %q, %v", after, readErr)
					}
					if damage == "unrecognized" && !bytes.HasPrefix(after, []byte(content)) {
						t.Fatalf("unrecognized text changed: %q", after)
					}
					if operation == "sync" && strings.Count(stderr.String(), "Reconciled unexpected") != 1 {
						t.Fatalf("duplicate repair warnings: %s", stderr.String())
					}
					if operation != "unlink" {
						got := loadState(t, instance, workspace)
						if damage == "edited" && strings.Contains(strings.Join(append(got.ManagedPaths, got.PendingAdds...), ","), "not-enrolled") {
							t.Fatal("exclude edit enrolled a path")
						}
					}
				})
			}
		}
	}
}

func TestExclusionRepairWarningBeforeEffectiveExclusionFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace := initializePublicRepository(t, root)
	instance, out := testApp(t, workspace, root, filepath.Join(root, "assets.git"))
	ctx := context.Background()
	if err := instance.Link(ctx, LinkOptions{Repository: "getspas/assets", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "asset.env"), []byte("asset\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := AddOptions{Paths: []string{"asset.env"}, ExistingExclude: ExcludePreserve, MergeProtection: MergeEnable}
	if err := instance.Add(ctx, options); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, ".git", "info", "exclude")
	damaged := []byte("# external\nother/\n")
	if err := os.WriteFile(path, damaged, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".gitignore"), []byte("!asset.env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	out.Reset()
	instance.Err = &stderr
	instance.JSON = true
	err := instance.Add(ctx, options)
	if kind, _ := spaserr.KindOf(err); kind != spaserr.KindExclusionValidation {
		t.Fatalf("Add() = %v", err)
	}
	var record struct {
		Warning Warning `json:"warning"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &record); err != nil || record.Warning.Code != "exclusion_drift_repaired" || out.Len() != 0 {
		t.Fatalf("failure streams: stdout %q, stderr %q, %v", out.String(), stderr.String(), err)
	}
	content, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(content, damaged) || bytes.Contains(content, []byte("# BEGIN SPAS link=")) {
		t.Fatalf("rollback = %q, %v", content, err)
	}
}

func TestExclusionDiagnosticsAndDryRunDoNotRepair(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace := initializePublicRepository(t, root)
	instance, out := testApp(t, workspace, root, filepath.Join(root, "assets.git"))
	ctx := context.Background()
	if err := instance.Link(ctx, LinkOptions{Repository: "getspas/assets", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "asset.env"), []byte("asset\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := AddOptions{Paths: []string{"asset.env"}, ExistingExclude: ExcludePreserve, MergeProtection: MergeEnable}
	if err := instance.Add(ctx, options); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, ".git", "info", "exclude")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	instance.Err = &stderr
	instance.JSON = true
	out.Reset()
	if err := instance.Doctor(ctx); err == nil {
		t.Fatal("Doctor() accepted ineffective exclusions")
	}
	if !bytes.Contains(out.Bytes(), []byte("exclude-block-integrity")) {
		t.Fatalf("Doctor() = %s", out.String())
	}
	options.DryRun = true
	if err := instance.Add(ctx, options); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) || stderr.Len() != 0 {
		t.Fatalf("read-only operation repaired or warned: %v, %s", err, stderr.String())
	}
}

func TestExclusionRecoveryWithSkippedOwnershipAndRemoteRemovalStaysQuiet(t *testing.T) {
	t.Parallel()
	instance, workspace, root, remote := fixture(t)
	enroll(t, instance, workspace, map[string]string{"skip.env": "project-owned\n", "deleted.env": "remove\n", "keep.env": "original\n"})
	runGit(t, workspace, "add", "-f", "skip.env")
	runGit(t, workspace, "commit", "-q", "-m", "Project takes ownership")
	teammatePush(t, root, remote, nil, []string{"deleted.env"})
	if err := os.WriteFile(filepath.Join(workspace, "keep.env"), []byte("updated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(remote, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	options := syncOptions("Update retained asset")
	options.Conflict = ConflictSkip
	var stderr bytes.Buffer
	instance.Err = &stderr
	if err := instance.Sync(context.Background(), options); err == nil {
		t.Fatal("Sync() accepted rejected push")
	}
	if state := loadState(t, instance, workspace); state.Materializing == nil {
		t.Fatal("no recovery marker after rejected push")
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(workspace, "deleted.env")); err != nil {
		t.Fatal(err)
	}
	if err := instance.Sync(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr.String(), "Reconciled unexpected") {
		t.Fatalf("ordinary recovery warned about external drift: %s", stderr.String())
	}
}
