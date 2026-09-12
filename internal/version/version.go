package version

import (
	"runtime/debug"
	"strings"
)

var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

func init() {
	populateFromBuildInfo()
}

func populateFromBuildInfo() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	applyBuildInfo(info)
}

func applyBuildInfo(info *debug.BuildInfo) {
	if info == nil {
		return
	}
	if Version == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		Version = strings.TrimPrefix(info.Main.Version, "v")
	}
	var rev, date, dirty string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			rev = setting.Value
		case "vcs.time":
			date = setting.Value
		case "vcs.modified":
			if setting.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if Commit == "unknown" && rev != "" {
		Commit = rev + dirty
	}
	if Date == "unknown" && date != "" {
		Date = date
	}
}
