// Package buildinfo holds version information injected at link time:
//
//	go build -ldflags "-X admindescargas/internal/buildinfo.Version=v1.2.3 \
//	  -X admindescargas/internal/buildinfo.Commit=abc123 \
//	  -X admindescargas/internal/buildinfo.Date=2026-01-01T00:00:00Z"
package buildinfo

import "runtime/debug"

// Set by -ldflags -X (see Makefile and .goreleaser.yaml).
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// String returns "Version (commit, date)" with whatever is known; for dev
// builds it falls back to the VCS info embedded by the Go toolchain.
func String() string {
	commit, date := Commit, Date
	if commit == "" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				switch s.Key {
				case "vcs.revision":
					commit = s.Value
				case "vcs.time":
					date = s.Value
				}
			}
		}
	}
	if len(commit) > 12 {
		commit = commit[:12]
	}
	out := Version
	if commit != "" {
		out += " (" + commit
		if date != "" {
			out += ", " + date
		}
		out += ")"
	}
	return out
}
