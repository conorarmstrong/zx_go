// Package version is the single source of truth for the emulator's
// release version string and author/repo metadata.
package version

import (
	"runtime/debug"
	"time"
)

// Version is the current release. Bump it here for a release; the
// window title, Help → About dialog, and --version flag all read it.
const Version = "v1.3.6"

// Author and RepoURL identify the project; surfaced in the startup
// banner and the Help → About dialog.
const (
	Author  = "Conor Armstrong"
	RepoURL = "https://github.com/conorarmstrong/zx_go"
)

// BuildDate is the build timestamp in RFC 3339, normally injected at
// build/release time:
//
//	go build -ldflags "-X github.com/conorarmstrong/zx_go/pkg/version.BuildDate=2026-06-07T23:45:12Z"
//
// It must be space-free (the linker's -X splits on spaces) — Date()
// reformats it for display. When empty (a plain `go build`), Date()
// falls back to the VCS commit time the Go toolchain embeds
// automatically, so a meaningful timestamp shows with no special flags.
var BuildDate = ""

// String returns the version with the product name, e.g.
// "zx_go v1.0 RC1".
func String() string {
	return "zx_go " + Version
}

// Date returns the build timestamp formatted for humans as
// "YYYY-MM-DD HH:MM:SS UTC" (date, time, and time zone). It uses the
// ldflags-injected BuildDate if set, else the embedded VCS commit
// time, else "unknown". Both sources are RFC 3339 in UTC; an
// unparseable value is returned as-is.
func Date() string {
	raw := BuildDate
	if raw == "" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, s := range info.Settings {
				if s.Key == "vcs.time" {
					raw = s.Value
				}
			}
		}
	}
	if raw == "" {
		return "unknown"
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		// UTC zone formats as "UTC" via the MST layout token.
		return t.UTC().Format("2006-01-02 15:04:05 MST")
	}
	return raw
}
