package router

import (
	"os/exec"
	"runtime"
	"testing"
)

// TestShellQuoteSurvivesAnyValue: a value with single quotes, spaces or shell
// syntax comes back from a POSIX shell exactly as it went in.
func TestShellQuoteSurvivesAnyValue(t *testing.T) {
	cases := []string{"", "plain", "a b", "it's", "'", "''", `a'b'c`, "$(touch x)", "`x`", `\`, "a;b|c&d"}
	for _, v := range cases {
		q := shellQuote(v)
		if q[0] != '\'' || q[len(q)-1] != '\'' {
			t.Errorf("shellQuote(%q) = %s: not quoted", v, q)
		}
	}
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX shell to round-trip through")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	for _, v := range cases {
		out, runErr := exec.CommandContext(t.Context(), sh, "-c", "printf %s "+shellQuote(v)).Output() // #nosec G204 -- the test's own values, quoted by the function under test
		if runErr != nil {
			t.Fatalf("sh on %q: %v", v, runErr)
		}
		if string(out) != v {
			t.Errorf("round trip of %q gave %q", v, out)
		}
	}
}
