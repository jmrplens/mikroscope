package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/version"
)

// TestVersionFlagPrintsTheBuildLineAndNothingElse is the contract the image
// smoke tests rely on: `-version` exits 0 with the build line, before the
// configuration is read. The environment is made invalid on purpose, so a
// run that reached agent.FromEnv would exit 2 instead.
func TestVersionFlagPrintsTheBuildLineAndNothingElse(t *testing.T) {
	t.Setenv("RATE_HZ", "not-a-number")
	for _, flag := range []string{"-version", "--version"} {
		var out bytes.Buffer
		if code := run([]string{flag}, &out); code != 0 {
			t.Fatalf("%s: exit %d, want 0; output %q", flag, code, out.String())
		}
		want := version.Line("mikroscope-agent") + "\n"
		if out.String() != want {
			t.Errorf("%s printed %q, want %q", flag, out.String(), want)
		}
		if !strings.HasPrefix(out.String(), "mikroscope-agent "+version.Version+" ") {
			t.Errorf("%s: %q does not start with the prefix the smoke tests match", flag, out.String())
		}
	}
}

// TestBadConfigurationExitsTwoWithOneLine keeps the failure RouterOS logs
// under the container topic to one line and a distinct exit status.
func TestBadConfigurationExitsTwoWithOneLine(t *testing.T) {
	t.Setenv("RATE_HZ", "not-a-number")
	var out bytes.Buffer
	if code := run(nil, &out); code != 2 {
		t.Fatalf("exit %d, want 2; output %q", code, out.String())
	}
	if got := out.String(); !strings.HasPrefix(got, "mikroscope-agent: bad configuration:") || strings.Count(got, "\n") != 1 {
		t.Errorf("output %q, want one bad-configuration line", got)
	}
}
