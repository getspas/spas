package version

import (
	"runtime/debug"
	"testing"
)

func TestApplyBuildInfo(t *testing.T) {
	origVersion := Version
	origCommit := Commit
	origDate := Date
	defer func() {
		Version = origVersion
		Commit = origCommit
		Date = origDate
	}()

	Version = "dev"
	Commit = "unknown"
	Date = "unknown"

	info := &debug.BuildInfo{
		Main: debug.Module{
			Version: "v1.2.3",
		},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "0123456789abcdef"},
			{Key: "vcs.time", Value: "2026-08-26T12:00:00Z"},
			{Key: "vcs.modified", Value: "true"},
		},
	}

	applyBuildInfo(info)

	if Version != "1.2.3" {
		t.Errorf("Version = %q, want 1.2.3", Version)
	}
	if Commit != "0123456789abcdef-dirty" {
		t.Errorf("Commit = %q, want 0123456789abcdef-dirty", Commit)
	}
	if Date != "2026-08-26T12:00:00Z" {
		t.Errorf("Date = %q, want 2026-08-26T12:00:00Z", Date)
	}
}

func TestApplyBuildInfoPreservesExistingValues(t *testing.T) {
	origVersion := Version
	origCommit := Commit
	origDate := Date
	defer func() {
		Version = origVersion
		Commit = origCommit
		Date = origDate
	}()

	Version = "0.2.0-SNAPSHOT-abc"
	Commit = "custom-commit"
	Date = "custom-date"

	info := &debug.BuildInfo{
		Main: debug.Module{
			Version: "v1.2.3",
		},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "newrevision"},
			{Key: "vcs.time", Value: "newtime"},
		},
	}

	applyBuildInfo(info)

	if Version != "0.2.0-SNAPSHOT-abc" {
		t.Errorf("Version = %q, want 0.2.0-SNAPSHOT-abc", Version)
	}
	if Commit != "custom-commit" {
		t.Errorf("Commit = %q, want custom-commit", Commit)
	}
	if Date != "custom-date" {
		t.Errorf("Date = %q, want custom-date", Date)
	}
}

func TestApplyBuildInfoDevelLeavesDev(t *testing.T) {
	origVersion := Version
	origCommit := Commit
	origDate := Date
	defer func() {
		Version = origVersion
		Commit = origCommit
		Date = origDate
	}()

	Version = "dev"
	Commit = "unknown"
	Date = "unknown"

	info := &debug.BuildInfo{
		Main: debug.Module{
			Version: "(devel)",
		},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "newrevision"},
			{Key: "vcs.time", Value: "newtime"},
		},
	}

	applyBuildInfo(info)

	if Version != "dev" {
		t.Errorf("Version = %q, want dev", Version)
	}
	if Commit != "newrevision" {
		t.Errorf("Commit = %q, want newrevision", Commit)
	}
	if Date != "newtime" {
		t.Errorf("Date = %q, want newtime", Date)
	}
}

func TestApplyBuildInfoEmptyMainVersionLeavesDev(t *testing.T) {
	origVersion := Version
	origCommit := Commit
	origDate := Date
	defer func() {
		Version = origVersion
		Commit = origCommit
		Date = origDate
	}()

	Version = "dev"
	Commit = "unknown"
	Date = "unknown"

	info := &debug.BuildInfo{
		Main: debug.Module{
			Version: "",
		},
	}

	applyBuildInfo(info)

	if Version != "dev" {
		t.Errorf("Version = %q, want dev", Version)
	}
}

func TestApplyBuildInfoNilInfo(t *testing.T) {
	origVersion := Version
	origCommit := Commit
	origDate := Date
	defer func() {
		Version = origVersion
		Commit = origCommit
		Date = origDate
	}()

	Version = "dev"
	Commit = "unknown"
	Date = "unknown"

	applyBuildInfo(nil)

	if Version != "dev" || Commit != "unknown" || Date != "unknown" {
		t.Errorf("got (%q, %q, %q), want (dev, unknown, unknown)", Version, Commit, Date)
	}
}
