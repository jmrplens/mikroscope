//go:build dockere2e

package docker

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// `forward --grafana` against a real Grafana and five real stores.
//
// THE CLAIM IS "FIVE OF FIVE", and it is worth proving rather than asserting
// in a unit test, because the thing that goes wrong is never the shape of the
// JSON — it is a plugin setting that the API accepts and the query path then
// refuses. Both InfluxDB traps this project has hit were exactly that: a
// datasource Grafana stored happily and answered `flightsql: Unauthenticated`
// or `tls: first record does not look like a TLS handshake` from.
//
// So this does not check what was written. It lets the collector create the
// datasource, and then runs `dashboards check` — every panel's query, through
// Grafana's own API — against the datasource the collector built.
func TestForwardPublishesFiveWorkingDatasources(t *testing.T) {
	s := Sweep(t)
	ctx := t.Context()
	admin := "http://admin:" + grafanaPassword() + "@" + s.stack.Grafana
	token := grafanaToken(ctx, t, admin)
	// The Postgres store needs the script applied before any panel can answer:
	// the connecting sink filled mikroscope_direct, and the dashboard the
	// publish binds reads the default database.
	loadSweepIntoPostgres(ctx, t, s)

	for _, store := range []struct {
		name string
		// sink is what `forward` is given so the store is configured at all.
		sink []string
		// extra is what it needs to describe the datasource: nothing for the
		// three that know their own address, an address for the two that
		// cannot.
		extra []string
		// vars are the dashboard variables `check` has to be told, as in
		// TestGrafanaDashboards.
		vars []string
	}{
		{
			name: "influxdb",
			sink: []string{
				"--influx", "http://" + s.stack.InfluxDB,
				"--influx-db", sweepDatabase,
			},
		},
		{
			name: "elasticsearch",
			sink: []string{"--elastic", "http://" + s.stack.Elasticsearch, "--elastic-index", sweepIndex},
			vars: []string{"--var", "host=" + sweepHostTag},
		},
		{
			name: "postgres",
			sink: []string{"--postgres", "postgres://mikroscope@" + s.stack.Postgres + "/mikroscope?sslmode=disable"},
		},
		{
			name:  "prometheus",
			sink:  []string{"--prom", "127.0.0.1:0"},
			extra: []string{"--grafana-datasource-url", "http://" + s.stack.Prometheus},
		},
		{
			name:  "graphite",
			sink:  []string{"--graphite", s.stack.GraphitePlain, "--graphite-prefix", graphitePfx},
			extra: []string{"--grafana-datasource-url", "http://" + s.stack.GraphiteWeb},
			vars:  []string{"--var", "prefix=" + graphitePfx, "--var", "host=" + sweepHostTag},
		},
	} {
		t.Run(store.name, func(t *testing.T) {
			sink := store.sink
			if store.name == "prometheus" {
				// :0 would bind a random port and publish nothing useful.
				port, err := freePort(ctx)
				if err != nil {
					t.Fatal(err)
				}
				sink = []string{"--prom", "127.0.0.1:" + strconv.Itoa(port)}
			}
			env := append(os.Environ(), "GRAFANA_TOKEN="+token, "HTTP_PROXY=", "HTTPS_PROXY=", "NO_PROXY=*")
			publishWith(ctx, t, s, store.name, append(sink, store.extra...), env)
			checkAgainstWhatItBuilt(ctx, t, s, store.name, store.vars, env)
		})
	}
}

// publishWith runs `forward --grafana` for one store and asserts it wrote both
// objects. The run needs no agent: publishing happens before the router is
// reached, and the pull failing afterwards is not what is under test.
func publishWith(ctx context.Context, t *testing.T, s *sweep, store string, extra, env []string) {
	t.Helper()
	args := append([]string{
		"forward",
		"--grafana", "http://" + s.stack.Grafana,
		"--grafana-folder", "mikroscope-e2e",
		"--host-tag", sweepHostTag,
		"--for", "1s",
		"--api-mode", "off",
		"--transport", "direct",
	}, extra...)
	runCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(runCtx, s.Bin, args...) // #nosec G204 -- constants above
	cmd.Env = env
	out, _ := cmd.CombinedOutput()
	said := string(out)
	if !strings.Contains(said, "datasource mikroscope-"+store) {
		t.Fatalf("the collector did not write a datasource for %s:\n%s", store, said)
	}
	if !strings.Contains(said, "dashboard http://"+s.stack.Grafana) {
		t.Fatalf("the collector did not publish a dashboard for %s:\n%s", store, said)
	}
	t.Logf("%s:\n%s", store, grafanaLines(said))
}

// checkAgainstWhatItBuilt runs every panel's query through Grafana against the
// datasource the collector just created. A panel with no rows is the store
// having nothing to say in the window; a panel with an error is the
// datasource, and that is the only thing this fails on.
func checkAgainstWhatItBuilt(ctx context.Context, t *testing.T, s *sweep, store string, vars, env []string) {
	t.Helper()
	args := append([]string{
		"dashboards", "check",
		"--grafana", "http://" + s.stack.Grafana,
		"--store", store,
		"--datasource-uid", "mikroscope-" + store,
		"--window", "15m",
	}, vars...)
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, s.Bin, args...) // #nosec G204 -- constants above
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	said := string(out)
	for trap, why := range map[string]string{
		"flightsql: Unauthenticated":         "cannot authenticate on the FlightSQL path",
		"does not look like a TLS handshake": "attempts TLS against a plaintext store",
	} {
		if strings.Contains(said, trap) {
			t.Fatalf("%s: the datasource it built %s:\n%s", store, why, tailOf(said))
		}
	}
	for line := range strings.SplitSeq(said, "\n") {
		if strings.Contains(line, " err ") {
			t.Errorf("%s: a panel errored against the datasource it built: %s", store, line)
		}
	}
	if err != nil {
		t.Logf("%s: check exited non-zero (empty panels are expected here):\n%s", store, tailOf(said))
	}
}

// grafanaLines is just the publish's own output, for the log.
func grafanaLines(said string) string {
	var keep []string
	for line := range strings.SplitSeq(said, "\n") {
		if strings.HasPrefix(line, "grafana:") {
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n")
}

// tailOf keeps the last few lines of a long output for a failure message.
func tailOf(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	return strings.Join(lines, "\n")
}
