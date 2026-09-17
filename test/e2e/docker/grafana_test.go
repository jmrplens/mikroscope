//go:build dockere2e

package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// grafanaPassword is the admin credential of the Grafana container, which the
// compose file sets from the same variable. Neither is a secret: the container
// listens on loopback and is deleted with the stack.
func grafanaPassword() string {
	if v := os.Getenv("MIKROSCOPE_E2E_GRAFANA_PASSWORD"); v != "" {
		return v
	}
	return "e2e-local-only"
}

// grafanaToken makes this run a service account of its own and returns its
// token: Grafana's API needs a credential even with anonymous access on, and
// `dashboards import` requires GRAFANA_TOKEN.
func grafanaToken(ctx context.Context, tb testing.TB, admin string) string {
	tb.Helper()
	var account struct {
		ID int `json:"id"`
	}
	body, err := json.Marshal(map[string]any{"name": "e2e-" + runID, "role": "Admin", "isDisabled": false})
	if err != nil {
		tb.Fatal(err)
	}
	if acctErr := httpJSON(ctx, "POST", admin+"/api/serviceaccounts", "application/json", body, &account); acctErr != nil {
		tb.Fatalf("creating a service account: %v", acctErr)
	}
	var token struct {
		Key string `json:"key"`
	}
	body, err = json.Marshal(map[string]any{"name": "e2e-" + runID})
	if err != nil {
		tb.Fatal(err)
	}
	if tokenErr := httpJSON(ctx, "POST",
		fmt.Sprintf("%s/api/serviceaccounts/%d/tokens", admin, account.ID), "application/json", body, &token); tokenErr != nil {
		tb.Fatalf("creating a service account token: %v", tokenErr)
	}
	return token.Key
}

// createDatasources points Grafana at the two stores this run filled. They are
// created over the API rather than provisioned from a file because each
// store's address is whatever port compose handed it.
func createDatasources(ctx context.Context, tb testing.TB, admin string, stack *Stack) {
	tb.Helper()
	for _, ds := range []struct {
		name, uid, kind, url string
		json                 map[string]any
		// top is merged into the datasource object itself rather than into
		// jsonData: `user` and `database` are first-class fields of Grafana's
		// datasource API, and a SQL datasource that carries them only in
		// jsonData connects as the Grafana process's own user.
		top map[string]any
	}{
		{
			"e2e-influxdb", "e2e-influxdb", "influxdb", "http://" + stack.InfluxDB,
			map[string]any{"version": "SQL", "dbName": sweepDatabase, "httpMode": "POST", "insecureGrpc": true},
			nil,
		},
		{
			"e2e-prometheus", "e2e-prometheus", "prometheus", "http://" + stack.Prometheus,
			map[string]any{"httpMethod": "POST"},
			nil,
		},
		{
			"e2e-graphite", "e2e-graphite", "graphite", "http://" + stack.GraphiteWeb,
			map[string]any{"graphiteVersion": "1.1"},
			nil,
		},
		{
			"e2e-elasticsearch", "e2e-elasticsearch", "elasticsearch", "http://" + stack.Elasticsearch,
			map[string]any{"timeField": "@timestamp", "index": sweepIndex, "maxConcurrentShardRequests": 5},
			map[string]any{"database": sweepIndex},
		},
		{
			// The PostgreSQL the SQL sink's script is loaded into, which is
			// what makes the third dashboard answerable here. sslmode=disable
			// because this server listens on a container network for one test
			// run; `postgresVersion` is what the plugin uses to decide which
			// syntax it may emit.
			"e2e-postgres", "e2e-postgres", "grafana-postgresql-datasource", stack.Postgres,
			map[string]any{
				"database": "mikroscope", "sslmode": "disable",
				"postgresVersion": 1800, "timescaledb": false,
			},
			map[string]any{"user": "mikroscope", "database": "mikroscope"},
		},
	} {
		payload := map[string]any{
			"name": ds.name, "uid": ds.uid, "type": ds.kind, "url": ds.url,
			"access": "proxy", "isDefault": false, "jsonData": ds.json,
		}
		maps.Copy(payload, ds.top)
		body, err := json.Marshal(payload)
		if err != nil {
			tb.Fatal(err)
		}
		err = httpJSON(ctx, "POST", admin+"/api/datasources", "application/json", body, nil)
		// A reused stack already has it, under whatever address the run that
		// created it was given: update rather than leave it pointing at a
		// port that now belongs to nothing.
		if err != nil && strings.Contains(err.Error(), "409") {
			err = httpJSON(ctx, "PUT", admin+"/api/datasources/uid/"+ds.uid, "application/json", body, nil)
		}
		if err != nil {
			tb.Fatalf("creating the %s datasource: %v", ds.name, err)
		}
	}
}

// TestGrafanaDashboards drives `mikroscope dashboards import` and
// `dashboards check` against a real Grafana in front of the store this run
// just filled. `check` runs every panel's query through Grafana's own query
// API, which is where a panel fails for reasons no unit test reaches: a
// datasource plugin that cannot decode the type an aggregate returns, a
// macro the plugin escapes, a field a panel names and the store does not
// have. The reference device found both of the first two in September 2026.
func TestGrafanaDashboards(t *testing.T) {
	s := Sweep(t)
	ctx := t.Context()

	admin := "http://admin:" + grafanaPassword() + "@" + s.stack.Grafana
	token := grafanaToken(ctx, t, admin)
	createDatasources(ctx, t, admin, s.stack)

	for _, store := range []struct {
		name, uid string
		// The dashboard variables this store's queries carry, which Grafana
		// would interpolate in a browser and `dashboards check` has to be
		// told: Graphite's path prefix and host node, Elasticsearch's host.
		vars []string
	}{
		{"influxdb", "e2e-influxdb", nil},
		{"prometheus", "e2e-prometheus", nil},
		{"postgres", "e2e-postgres", nil},
		{"graphite", "e2e-graphite", []string{"--var", "prefix=" + graphitePfx, "--var", "host=" + sweepHostTag}},
		{"elasticsearch", "e2e-elasticsearch", []string{"--var", "host=" + sweepHostTag}},
	} {
		t.Run(store.name, func(t *testing.T) {
			if store.name == "postgres" {
				// The SQL sink writes a script, not rows: without applying it
				// the database has no schema and every panel fails on a
				// missing relation rather than on its own query.
				loadSweepIntoPostgres(ctx, t, s)
			}
			run := func(sub string, mustSucceed bool, extra ...string) string {
				t.Helper()
				args := append([]string{
					"dashboards", sub,
					"--grafana", "http://" + s.stack.Grafana,
					"--store", store.name,
					"--datasource-uid", store.uid,
				}, extra...)
				cmdCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				defer cancel()
				// The binary is the one this package just built and the args
				// are constants above it.
				cmd := exec.CommandContext(cmdCtx, s.Bin, args...) // #nosec G204
				cmd.Env = append(os.Environ(), "GRAFANA_TOKEN="+token, "HTTP_PROXY=", "HTTPS_PROXY=", "NO_PROXY=*")
				out, err := cmd.CombinedOutput()
				if err != nil && mustSucceed {
					t.Fatalf("dashboards %s: %v\n%s", sub, err, out)
				}
				return string(out)
			}
			t.Log(strings.TrimSpace(run("import", true)))

			// `check` exits non-zero when panels return no data, and a 12 s
			// run cannot fill a panel that bins by the minute — so an empty
			// panel is not this suite's business. What is: a panel whose
			// query the datasource could not answer. Those carry the reason
			// after the counts, and they are the class of fault this stack
			// exists to catch — a plugin that cannot decode the type an
			// aggregate returns, an escaped macro, a field that is not there.
			out := run("check", false, append([]string{
				"--end", s.End.UTC().Format(time.RFC3339),
				"--window", "10m",
			}, store.vars...)...)
			withData, errors := scanCheck(t, out)
			if withData == 0 {
				t.Errorf("not one panel returned a row from the store this run just filled:\n%s", out)
			}
			t.Logf("%d panels returned data, %d could not be answered", withData, errors)
		})
	}
}

// scanCheck reads `dashboards check` output and returns how many panels
// returned a row and how many the datasource could not answer, reporting each
// of the latter with its reason.
func scanCheck(tb testing.TB, out string) (withData, errors int) {
	tb.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		m := checkLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if rows, _ := strconv.Atoi(m[3]); rows > 0 {
			withData++
		}
		if reason := strings.TrimSpace(m[5]); reason != "" {
			errors++
			tb.Errorf("panel %q: %s", strings.TrimSpace(m[2]), reason)
		}
	}
	return withData, errors
}

// checkLine matches one row of `dashboards check` output:
//
//	ok   Sample continuity        rows=5 frames=1
//	FAIL Observer effect: …       rows=0 frames=0 POST /api/ds/query: 400 …
var checkLine = regexp.MustCompile(`^\s*(ok|FAIL|none)\s+(.+?)\s+rows=(\d+)\s+frames=(\d+)\s*(.*)$`)
