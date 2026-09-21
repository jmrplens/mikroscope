package main

import (
	"flag"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/router"
)

// captureMu serializes the redirection. os.Stdout is one global, so two
// parallel tests swapping it race: each restores what IT saved, so the loser
// hands back a pipe the winner has already closed and the winner's output
// lands in the loser's buffer. It surfaced as one intermittent failure in a
// coverage run, which is the only way a test like this ever announces itself.
var captureMu sync.Mutex

// capture runs f with os.Stdout redirected and returns what it printed. The
// CLI's reporting functions write to stdout directly, which is right for a
// terminal tool and is why they need this to be read back.
func capture(t *testing.T, f func()) string {
	t.Helper()
	captureMu.Lock()
	defer captureMu.Unlock()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	f()
	os.Stdout = saved
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

// run is the verb switch. Every arm but the default needs a router, and with
// none configured each fails in the same place and says so — which is the
// behavior worth pinning, because the alternative is a verb that silently
// does nothing.
func TestRunDispatchesEveryVerbAndNamesAnUnknownOne(t *testing.T) {
	t.Parallel()
	c := cli{opts: router.Defaults()}
	// A registry reference means no image is built, so these exercise the
	// verb switch rather than this machine's Go toolchain.
	c.opts.RemoteImage = "jmrplens/mikroscope-agent:1.0.10"
	if err := c.opts.Finish(); err != nil {
		t.Fatal(err)
	}
	if c.router != "" {
		t.Skip("MIKROSCOPE_ROUTER is set in this environment; this test needs none")
	}

	for _, verb := range []string{"doctor", "install", "upgrade", "status", "image"} {
		err := run(verb, c)
		if err == nil {
			t.Errorf("%s with no router returned no error", verb)
			continue
		}
		// `image` fails for its own reason: --remote-image means there is no
		// tar to write, which it refuses before it would need a router.
		if verb != "image" && !strings.Contains(err.Error(), "--router") {
			t.Errorf("%s: error %q does not name the missing flag", verb, err)
		}
	}

	// `plan` is `install --dry-run`, so it writes nothing and needs no router.
	out := capture(t, func() {
		if err := run("plan", c); err != nil {
			t.Errorf("plan returned %v", err)
		}
	})
	if !strings.Contains(out, "install plan for") || !strings.Contains(out, "nothing above has been written yet") {
		t.Errorf("plan printed:\n%s", out)
	}

	// An unknown verb prints the usage and names what it did not recognize.
	err := run("frobnicate", c)
	if err == nil || !strings.Contains(err.Error(), `unknown verb "frobnicate"`) {
		t.Errorf("unknown verb returned %v", err)
	}
}

func TestRunnerNeedsARouterAndOtherwiseCarriesTheSSHSettings(t *testing.T) {
	t.Parallel()
	if _, err := (cli{}).runner(); err == nil || !strings.Contains(err.Error(), "--router") {
		t.Errorf("runner with no router = %v, want an error naming --router", err)
	}
	r, err := (cli{router: "admin@192.0.2.1", sshPort: "2222", sshKey: "/k"}).runner()
	if err != nil {
		t.Fatal(err)
	}
	ssh, ok := r.(router.SSHRunner)
	if !ok {
		t.Fatalf("runner = %T, want router.SSHRunner", r)
	}
	if ssh.Target != "admin@192.0.2.1" || ssh.Port != "2222" || ssh.Key != "/k" {
		t.Errorf("runner = %+v, the flags did not reach it", ssh)
	}
	if ssh.Timeout <= 0 {
		t.Errorf("runner timeout = %v, an unbounded ssh can hang a script forever", ssh.Timeout)
	}
}

// parse is what every deployment verb is configured through, and an
// unparseable flag has to fail before anything reaches the router.
func TestParseReadsFlagsAndRefusesBadOnes(t *testing.T) {
	t.Parallel()
	c, err := parse("install", []string{"--name", "probe", "--rate", "50", "--port", "9999"})
	if err != nil {
		t.Fatal(err)
	}
	if c.opts.Name != "probe" || c.opts.RateHz != 50 || c.opts.Port != 9999 {
		t.Errorf("parsed %+v", c.opts)
	}
	if _, badErr := parse("install", []string{"--rate", "not-a-number"}); badErr == nil {
		t.Error("a non-numeric --rate parsed without error")
	}
	if _, unknownErr := parse("install", []string{"--no-such-flag"}); unknownErr == nil {
		t.Error("an unknown flag parsed without error")
	}
}

// --var is how `dashboards check` is told what the browser would have
// resolved; a value it cannot split is a typo worth refusing rather than a
// variable silently left unset.
func TestSetVarSplitsOnTheFirstEqualsOnly(t *testing.T) {
	t.Parallel()
	into := map[string]string{}
	set := setVar(into)
	for _, v := range []string{"host=rb5009", "empty=", "q=a=b"} {
		if err := set(v); err != nil {
			t.Errorf("--var %q = %v", v, err)
		}
	}
	if into["host"] != "rb5009" {
		t.Errorf("host = %q", into["host"])
	}
	if _, ok := into["empty"]; !ok {
		t.Error("an empty value is a value, and must be set")
	}
	if into["q"] != "a=b" {
		t.Errorf("q = %q, want the split to take the first = only", into["q"])
	}
	for _, bad := range []string{"novalue", "=novalue", ""} {
		if err := set(bad); err == nil {
			t.Errorf("--var %q was accepted", bad)
		}
	}
}

// --api-mode is a preset over three flags, and it must never overwrite one the
// operator set explicitly on the same command line.
func TestAPIModePresetsYieldToExplicitFlags(t *testing.T) {
	t.Parallel()
	newFS := func(setArgs ...string) (*flag.FlagSet, *time.Duration, *time.Duration, *bool) {
		apiEvery, conntrackEvery := time.Second, 5*time.Minute
		noHealth := false
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.DurationVar(&apiEvery, "api-every", apiEvery, "")
		fs.DurationVar(&conntrackEvery, "conntrack-every", conntrackEvery, "")
		fs.BoolVar(&noHealth, "no-health", noHealth, "")
		if err := fs.Parse(setArgs); err != nil {
			t.Fatal(err)
		}
		return fs, &apiEvery, &conntrackEvery, &noHealth
	}

	fs, apiEvery, conntrackEvery, noHealth := newFS()
	if err := applyAPIMode("full", fs, apiEvery, conntrackEvery, noHealth); err != nil {
		t.Fatal(err)
	}
	if *apiEvery != time.Second {
		t.Errorf("full changed --api-every to %v; the defaults already are full", *apiEvery)
	}

	fs, apiEvery, _, _ = newFS()
	if err := applyAPIMode("off", fs, apiEvery, conntrackEvery, noHealth); err != nil {
		t.Fatal(err)
	}
	if *apiEvery != 0 {
		t.Errorf("off left --api-every at %v", *apiEvery)
	}

	fs, apiEvery, conntrackEvery, noHealth = newFS()
	if err := applyAPIMode("slow", fs, apiEvery, conntrackEvery, noHealth); err != nil {
		t.Fatal(err)
	}
	if *apiEvery != 10*time.Second || *conntrackEvery != 0 || !*noHealth {
		t.Errorf("slow gave api-every=%v conntrack-every=%v no-health=%v", *apiEvery, *conntrackEvery, *noHealth)
	}

	// Set explicitly, so the preset must not touch it.
	fs, apiEvery, _, _ = newFS("--api-every", "2s")
	if err := applyAPIMode("slow", fs, apiEvery, conntrackEvery, noHealth); err != nil {
		t.Fatal(err)
	}
	if *apiEvery != 2*time.Second {
		t.Errorf("slow overwrote an explicit --api-every with %v", *apiEvery)
	}

	if err := applyAPIMode("sideways", fs, apiEvery, conntrackEvery, noHealth); err == nil ||
		!strings.Contains(err.Error(), "off, slow or full") {
		t.Errorf("an unknown mode returned %v, want an error listing the three", err)
	}
}

// printBoard is the one place the CLI turns a board string into port names,
// and its whole purpose is to say plainly when it cannot.
func TestPrintBoardSaysWhatItCannotMap(t *testing.T) {
	t.Parallel()
	if out := capture(t, func() { printBoard("") }); !strings.Contains(out, "no model") {
		t.Errorf("an empty board printed:\n%s", out)
	}
	out := capture(t, func() { printBoard("NOT-A-REAL-BOARD") })
	if !strings.Contains(out, "no kernel-to-RouterOS port map") || !strings.Contains(out, "To contribute one") {
		t.Errorf("an unknown board printed:\n%s", out)
	}
	out = capture(t, func() { printBoard("RB5009") })
	if !strings.Contains(out, "RB5009") || !strings.Contains(out, "ports mapped") {
		t.Errorf("a known board printed:\n%s", out)
	}
}

// confirm is the last thing between a plan and a write, so "anything that is
// not yes is no" is the property that matters.
func TestConfirmTakesOnlyYes(t *testing.T) {
	answer := func(t *testing.T, typed string) bool {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		if _, wErr := io.WriteString(w, typed); wErr != nil {
			t.Fatal(wErr)
		}
		_ = w.Close()
		saved := os.Stdin
		os.Stdin = r
		defer func() { os.Stdin = saved }()
		var got bool
		_ = capture(t, func() { got = confirm() })
		return got
	}
	for _, yes := range []string{"y\n", "Y\n", " y \n"} {
		if !answer(t, yes) {
			t.Errorf("confirm(%q) said no", yes)
		}
	}
	for _, no := range []string{"\n", "n\n", "yes\n", "yep\n", "q\n"} {
		if answer(t, no) {
			t.Errorf("confirm(%q) said yes — only an exact y may write to a router", no)
		}
	}
}
