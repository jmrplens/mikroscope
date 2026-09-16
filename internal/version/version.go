// Package version carries the build identity both binaries report.
//
// A release stamps it with -ldflags "-X
// github.com/jmrplens/mikroscope/internal/version.Version=... -X ...Commit=...
// -X ...BuildDate=...". The Makefile, GoReleaser, scripts/agent-tars.sh and
// internal/image (for the agent inside an image tar) all stamp the same three.
package version

import (
	"runtime/debug"

	"github.com/jmrplens/mikroscope"
)

// Version, Commit and BuildDate are the linker's targets, and they stay plain
// uninitialized strings on purpose. The linker's -X only reaches a variable
// that is uninitialized or initialized to a constant expression, so writing
// `var Version = mikroscope.Version` here would make every release stamp a
// silent no-op and the binary would report whatever the VERSION file said when
// it was compiled, tag or no tag. The fallback belongs in resolve instead, and
// init applies it before any other package reads the values.
var (
	Version   string
	Commit    string
	BuildDate string
)

func init() {
	Version, Commit, BuildDate = resolve(Version, Commit, BuildDate, debug.ReadBuildInfo)
}

// resolve decides what this binary says about itself. A stamped value always
// wins, because that is what a release carries and it is the only one that can
// know a tag. Otherwise the version comes from the VERSION file the root
// package embeds, and the commit and the date from the VCS stamps the
// toolchain records in any build made inside a checkout, so an unstamped
// `go build` or `go run` is honest rather than "dev".
func resolve(ldVersion, ldCommit, ldDate string,
	readBuildInfo func() (*debug.BuildInfo, bool),
) (v, c, d string) {
	v, c, d = ldVersion, ldCommit, ldDate
	if v == "" {
		v = mikroscope.Version
	}
	info, ok := readBuildInfo()
	if !ok || info == nil {
		return v, c, d
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if c == "" {
				c = s.Value
			}
		case "vcs.time":
			if d == "" {
				d = s.Value
			}
		}
	}
	return v, c, d
}

// String renders the identity the agent reports in its /health answer, its
// mikroscope_info series and its start-up line.
func String() string {
	s := Version
	if Commit != "" {
		s += " (" + Commit + ")"
	}
	if BuildDate != "" {
		s += " built " + BuildDate
	}
	return s
}

// Line is what `mikroscope version` and `mikroscope-agent -version` print:
// one line, because the image smoke tests in ci.yml and release.yml match on
// its "<program> <version> " prefix and a bug report is pasted from it.
func Line(program string) string {
	c, d := Commit, BuildDate
	if c == "" {
		c = "unknown"
	}
	if d == "" {
		d = "unknown"
	}
	return program + " " + Version + " (commit " + c + ", built " + d + ")"
}
