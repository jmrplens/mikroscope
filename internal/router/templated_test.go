package router

import (
	"testing"

	"github.com/jmrplens/mikroscope/internal/version"
)

// TestTemplatedRoundTrips verifies that Templated puts the placeholder where a
// rendering names the release, that version.Expand puts the release back, and
// that nothing else in a script changes: RouterOS's own version check and an
// address that happens to hold the same digits stay as they were.
func TestTemplatedRoundTrips(t *testing.T) {
	v, p := version.Version, version.Placeholder
	in := "# mikroscope " + v + ": install script for RouterOS 7.24 or later.\n" +
		`remote-image="registry-1.docker.io/jmrplens/mikroscope-agent:` + v + `"` + "\n" +
		`{"version": "` + v + `"}` + "\n" +
		":if (!([/system/resource/get version] ~ \"^7[.]24\")) do={}\n" +
		"address=10." + v + "/30\n"
	want := "# mikroscope " + p + ": install script for RouterOS 7.24 or later.\n" +
		`remote-image="registry-1.docker.io/jmrplens/mikroscope-agent:` + p + `"` + "\n" +
		`{"version": "` + p + `"}` + "\n" +
		":if (!([/system/resource/get version] ~ \"^7[.]24\")) do={}\n" +
		"address=10." + v + "/30\n"
	got := Templated(in)
	if got != want {
		t.Fatalf("Templated:\n got %q\nwant %q", got, want)
	}
	if back := version.Expand(got); back != in {
		t.Errorf("version.Expand(Templated(s)) = %q, want s back, %q", back, in)
	}
}
