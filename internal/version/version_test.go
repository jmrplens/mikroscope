package version

import (
	"runtime/debug"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope"
)

func TestResolve(t *testing.T) {
	buildInfo := func(settings ...debug.BuildSetting) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Settings: settings}, true
		}
	}
	vcs := buildInfo(
		debug.BuildSetting{Key: "vcs.revision", Value: "cafe1234"},
		debug.BuildSetting{Key: "vcs.time", Value: "2026-01-02T03:04:05Z"},
	)

	tests := []struct {
		name                              string
		ldVersion, ldCommit, ldDate       string
		readInfo                          func() (*debug.BuildInfo, bool)
		wantVersion, wantCommit, wantDate string
	}{
		{
			name:        "nothing stamped inside a checkout",
			readInfo:    vcs,
			wantVersion: mikroscope.Version,
			wantCommit:  "cafe1234",
			wantDate:    "2026-01-02T03:04:05Z",
		},
		{
			name:        "stamped values are never overridden",
			ldVersion:   "9.9.9",
			ldCommit:    "deadbeef",
			ldDate:      "2020-12-31T23:59:59Z",
			readInfo:    vcs,
			wantVersion: "9.9.9",
			wantCommit:  "deadbeef",
			wantDate:    "2020-12-31T23:59:59Z",
		},
		{
			name:        "a stamped version still takes the commit and date from VCS",
			ldVersion:   "9.9.9",
			readInfo:    vcs,
			wantVersion: "9.9.9",
			wantCommit:  "cafe1234",
			wantDate:    "2026-01-02T03:04:05Z",
		},
		{
			name:        "build info carrying no VCS settings",
			readInfo:    buildInfo(debug.BuildSetting{Key: "-trimpath", Value: "true"}),
			wantVersion: mikroscope.Version,
		},
		{
			name:        "unavailable build info",
			readInfo:    func() (*debug.BuildInfo, bool) { return nil, false },
			wantVersion: mikroscope.Version,
		},
		{
			name:        "nil build info returned with ok true",
			readInfo:    func() (*debug.BuildInfo, bool) { return nil, true },
			wantVersion: mikroscope.Version,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, c, d := resolve(tt.ldVersion, tt.ldCommit, tt.ldDate, tt.readInfo)
			if v != tt.wantVersion {
				t.Errorf("version = %q, want %q", v, tt.wantVersion)
			}
			if c != tt.wantCommit {
				t.Errorf("commit = %q, want %q", c, tt.wantCommit)
			}
			if d != tt.wantDate {
				t.Errorf("date = %q, want %q", d, tt.wantDate)
			}
		})
	}
}

// TestVersionIsTheTrimmedVersionFile verifies the embedded floor is the file's
// contents without its trailing newline. An untrimmed value would put a line
// break in the middle of every version line, every mikroscope_info label and
// every image tag built from a Makefile that reads the file.
func TestVersionIsTheTrimmedVersionFile(t *testing.T) {
	if mikroscope.Version == "" {
		t.Fatal("embedded Version is empty; VERSION did not reach the build")
	}
	if strings.TrimSpace(mikroscope.Version) != mikroscope.Version {
		t.Errorf("embedded Version %q carries surrounding whitespace", mikroscope.Version)
	}
}

// TestLine verifies the single line both binaries print, in the shape a
// release produces and the shape an unstamped build does. The image smoke
// tests match on the "<program> <version> " prefix, so the leading two fields
// are a contract, not a formatting choice.
func TestLine(t *testing.T) {
	oldVersion, oldCommit, oldDate := Version, Commit, BuildDate
	t.Cleanup(func() { Version, Commit, BuildDate = oldVersion, oldCommit, oldDate })

	Version, Commit, BuildDate = "1.2.3", "abc1234", "2026-01-02T03:04:05Z"
	if got, want := Line("mikroscope"), "mikroscope 1.2.3 (commit abc1234, built 2026-01-02T03:04:05Z)"; got != want {
		t.Errorf("Line() = %q, want %q", got, want)
	}
	if got, want := String(), "1.2.3 (abc1234) built 2026-01-02T03:04:05Z"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}

	Version, Commit, BuildDate = "1.2.3", "", ""
	if got, want := Line("mikroscope-agent"), "mikroscope-agent 1.2.3 (commit unknown, built unknown)"; got != want {
		t.Errorf("Line() with nothing to report = %q, want %q", got, want)
	}
	if got, want := String(), "1.2.3"; got != want {
		t.Errorf("String() with nothing to report = %q, want %q", got, want)
	}
}
