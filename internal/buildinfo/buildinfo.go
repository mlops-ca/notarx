package buildinfo

import (
	"runtime"
	"runtime/debug"
	"strings"
)

var (
	injectedVersion  string
	injectedRevision string
)

type Info struct {
	Version   string
	Revision  string
	Modified  bool
	GoVersion string
}

// Core returns the SemVer core (MAJOR.MINOR.PATCH) of the reported version,
// dropping a leading "v", any pre-release suffix such as "-nightly.2" or
// "-dev", and build metadata. It returns "" when the version is not SemVer,
// for example the "dev" fallback of an unstamped build.
func (i Info) Core() string {
	return Core(i.Version)
}

// Core returns the SemVer core of version; see Info.Core.
func Core(version string) string {
	version = strings.TrimPrefix(version, "v")
	if cut := strings.IndexAny(version, "-+"); cut >= 0 {
		version = version[:cut]
	}
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return ""
	}
	for _, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return ""
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return ""
			}
		}
	}
	return version
}

func Read() Info {
	result := Info{Version: "dev", Revision: "unknown", GoVersion: runtime.Version()}
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			result.Version = info.Main.Version
		}
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if setting.Value != "" {
					result.Revision = setting.Value
				}
			case "vcs.modified":
				result.Modified = setting.Value == "true"
			}
		}
	}
	if injectedVersion != "" {
		result.Version = injectedVersion
	}
	if injectedRevision != "" {
		result.Revision = injectedRevision
	}
	return result
}

// CI measurement for #482 (draft pull request, never merged).
