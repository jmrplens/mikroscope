package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/dashboards"
	"github.com/jmrplens/mikroscope/internal/router"
)

// `dashboards gen` is the one subcommand that needs neither a Grafana nor a
// store, and it is what the repository's committed dashboards/ is regenerated
// with, so what it writes has to be exactly the set the check target compares
// against.
func TestDashboardsGenWritesOneFilePerStoreAndItsAlerts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out := capture(t, func() {
		if err := dashboardsGen(dir); err != nil {
			t.Errorf("dashboardsGen: %v", err)
		}
	})

	for _, st := range dashboards.Stores {
		path := filepath.Join(dir, "mikroscope-"+string(st)+".json")
		b, err := os.ReadFile(path) // #nosec G304 -- the test's own temp dir
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if len(b) == 0 || b[len(b)-1] != '\n' {
			t.Errorf("%s: %d bytes, and it must end in a newline", path, len(b))
		}
		if !strings.Contains(out, path) {
			t.Errorf("gen did not report writing %s", path)
		}
		alerts := filepath.Join(dir, "mikroscope-alerts-"+string(st)+".yaml")
		_, aerr := os.Stat(alerts)
		if dashboards.HasAlerts(st) && aerr != nil {
			t.Errorf("%s has alerts but %s was not written", st, alerts)
		}
		if !dashboards.HasAlerts(st) && aerr == nil {
			t.Errorf("%s has no alerts but %s was written", st, alerts)
		}
	}

	// A directory that does not exist is the operator's typo, and it has to
	// come back as an error rather than as a silent no-op.
	if err := dashboardsGen(filepath.Join(dir, "no", "such", "dir")); err == nil {
		t.Error("gen into a missing directory returned no error")
	}
}

// The subcommand switch and the three things import/check cannot run without.
func TestRunDashboardsRefusesWhatItCannotDo(t *testing.T) {
	// Not parallel: t.Setenv clears GRAFANA_TOKEN for this test, and the two
	// cannot be combined.
	if err := runDashboards(nil); err == nil || !strings.Contains(err.Error(), "gen, import or check") {
		t.Errorf("no subcommand = %v", err)
	}
	if err := runDashboards([]string{"gen", "--no-such-flag"}); err == nil {
		t.Error("an unknown flag was accepted")
	}
	if err := runDashboards([]string{"frobnicate"}); err == nil {
		t.Errorf("an unknown subcommand = %v", err)
	}
	// import and check need all three, and the message names all three so the
	// operator does not learn them one refusal at a time.
	t.Setenv("GRAFANA_TOKEN", "")
	for _, sub := range []string{"import", "check"} {
		err := runDashboards([]string{sub, "--grafana", "http://127.0.0.1:1", "--datasource-uid", "u"})
		if err == nil || !strings.Contains(err.Error(), "GRAFANA_TOKEN") {
			t.Errorf("%s without a token = %v", sub, err)
		}
	}
}

func TestParseRecordFlagsReadsItsOwnFlags(t *testing.T) {
	t.Parallel()
	c := cli{opts: router.Defaults()}
	ro, rest, err := parseRecordFlags("record", []string{"--out", "cap", "--for", "30s", "--batch", "64", "--transport", "relay", "trailing"}, &c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ro.prefix != "cap" || ro.forDur.String() != "30s" || ro.batch != 64 || ro.transport != "relay" {
		t.Errorf("parsed %+v", ro)
	}
	if len(rest) != 1 || rest[0] != "trailing" {
		t.Errorf("positional args = %v, want the mark text to survive", rest)
	}
	if _, _, badErr := parseRecordFlags("record", []string{"--for", "not-a-duration"}, &c, nil); badErr == nil {
		t.Error("an unparseable --for was accepted")
	}
}

// choosePuller is the transport negotiation: direct if the agent answers,
// otherwise the relay, and each refusal has to say which half failed.
func TestChoosePullerPrefersDirectAndExplainsEachRefusal(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"seq":1,"oldest_seq":1,"rate_hz":10,"version":"t"}`))
	}))
	defer srv.Close()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}

	live := cli{opts: router.Defaults()}
	live.opts.ContainerIP, live.opts.Port = host, n
	p, done, err := choosePuller(context.Background(), recordOptions{transport: "auto"}, live)
	if err != nil {
		t.Fatalf("auto against a live agent: %v", err)
	}
	done()
	if !strings.HasPrefix(p.Name(), "direct ") {
		t.Errorf("auto chose %q, want the direct transport when the agent answers", p.Name())
	}

	// --transport direct is a demand, not a preference: it must not fall back.
	dead := cli{opts: router.Defaults()}
	dead.opts.ContainerIP, dead.opts.Port = "127.0.0.1", 1
	if _, _, dErr := choosePuller(context.Background(), recordOptions{transport: "direct"}, dead); dErr == nil ||
		!strings.Contains(dErr.Error(), "direct transport") {
		t.Errorf("direct against a closed port = %v", dErr)
	}

	// auto with nothing listening and no API configured names both halves,
	// and points at the flag that would fix it.
	_, _, aErr := choosePuller(context.Background(), recordOptions{transport: "auto"}, dead)
	if aErr == nil || !strings.Contains(aErr.Error(), "relay is not configured") || !strings.Contains(aErr.Error(), "--expose") {
		t.Errorf("auto with neither transport = %v", aErr)
	}
}
