package main

import (
	"runtime/debug"
	"time"
)

// Set at build time by the Dockerfile (-ldflags -X); see .github/workflows/log.yml.
var (
	commit    string
	buildDate string // RFC 3339
)

// versionText describes the running build for people: "b8e67d8 · built
// 4 Oct 2026". Local builds fall back to the commit Go records from git
// (with "+" if there were uncommitted changes), or "dev".
func versionText() string {
	c, d, dirty := commit, buildDate, false
	if c == "" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, s := range info.Settings {
				switch s.Key {
				case "vcs.revision":
					c = s.Value
				case "vcs.time":
					if d == "" {
						d = s.Value
					}
				case "vcs.modified":
					dirty = s.Value == "true"
				}
			}
		}
	}
	if c == "" {
		return "dev"
	}
	if len(c) > 7 {
		c = c[:7]
	}
	if dirty {
		c += "+"
	}
	if t, err := time.Parse(time.RFC3339, d); err == nil {
		return c + " · built " + t.Format("2 Jan 2006")
	}
	return c
}
