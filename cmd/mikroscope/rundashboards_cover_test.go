package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// `dashboards import` and `dashboards check` are the two subcommands that
// talk to a Grafana, and both do the same thing first: ask the datasource
// which measurements it holds, so a panel reading a table this device has
// never written is moved into the not-available row rather than shipped to
// read "No data" forever. A stub Grafana is three endpoints.
func stubGrafanaServer(t *testing.T, probeOK bool) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/dashboards/import":
			_, _ = w.Write([]byte(`{"importedUrl":"/d/mikroscope-influxdb/x"}`))
		case "/api/ds/query":
			if !probeOK {
				http.Error(w, "datasource not found", http.StatusNotFound)
				return
			}
			// One row per refId the request actually carries. A stub that
			// answered only "A" would leave every panel whose target is B or
			// C reading no data, which is a property of the stub and not of
			// the dashboard.
			var req struct {
				Queries []struct {
					RefID string `json:"refId"`
				} `json:"queries"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			const frame = `{"frames":[{"schema":{"fields":[{"name":"value","type":"number"}]},"data":{"values":[[1]]}}]}`
			parts := make([]string, 0, len(req.Queries))
			for _, q := range req.Queries {
				id := q.RefID
				if id == "" {
					id = "A"
				}
				parts = append(parts, strconv.Quote(id)+":"+frame)
			}
			if len(parts) == 0 {
				parts = append(parts, `"A":`+frame)
			}
			_, _ = w.Write([]byte(`{"results":{` + strings.Join(parts, ",") + `}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestRunDashboardsImportsAndChecks(t *testing.T) {
	t.Setenv("GRAFANA_TOKEN", "t")
	url := stubGrafanaServer(t, true)

	out := capture(t, func() {
		if err := runDashboards([]string{"import", "--grafana", url, "--datasource-uid", "uid"}); err != nil {
			t.Errorf("import: %v", err)
		}
	})
	if !strings.Contains(out, "imported:") || !strings.Contains(out, "/d/mikroscope-influxdb/") {
		t.Errorf("import printed:\n%s", out)
	}

	out = capture(t, func() {
		if err := runDashboards([]string{"check", "--grafana", url, "--datasource-uid", "uid", "--window", "5m", "--var", "host=rb5009"}); err != nil {
			t.Errorf("check: %v", err)
		}
	})
	if !strings.Contains(out, "every panel returns data") {
		t.Errorf("check printed:\n%s", out)
	}

	// --no-probe ships the compiled defaults instead of asking, which is what
	// an operator uses against a store that is not filled yet.
	out = capture(t, func() {
		if err := runDashboards([]string{"import", "--grafana", url, "--datasource-uid", "uid", "--no-probe"}); err != nil {
			t.Errorf("import --no-probe: %v", err)
		}
	})
	if !strings.Contains(out, "imported:") {
		t.Errorf("import --no-probe printed:\n%s", out)
	}

	// An --end that is not RFC3339 is refused before anything is asked.
	if err := runDashboards([]string{"check", "--grafana", url, "--datasource-uid", "uid", "--end", "yesterday"}); err == nil {
		t.Error("an unparseable --end was accepted")
	}
}

// A probe that fails is a warning, not a refusal: the compiled defaults are a
// worse answer than the store's own, and both are better than not importing.
func TestRunDashboardsCarriesOnWhenTheProbeFails(t *testing.T) {
	t.Setenv("GRAFANA_TOKEN", "t")
	url := stubGrafanaServer(t, false)
	err := runDashboards([]string{"import", "--grafana", url, "--datasource-uid", "uid"})
	// The import itself still runs; what it cannot do is tailor the panels.
	if err != nil && !strings.Contains(err.Error(), "import") {
		t.Errorf("import with a failing probe = %v", err)
	}
}
