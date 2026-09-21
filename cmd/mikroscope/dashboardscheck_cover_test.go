package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/dashboards"
)

// dashboardsCheck runs every panel's query through Grafana's own datasource
// proxy and classifies each answer. The classification is its whole job, and
// it is three-way rather than two: a panel with rows is ok, a panel the
// device genuinely has no measurement for is `none` and tolerated, and
// anything else is a FAIL that fails the command. Getting the middle one
// wrong is what would make the check either useless or permanently red.
//
// A dashboard of two panels is enough to walk all three, and a stub Grafana
// answering /api/ds/query is enough to choose which.
const twoPanelDashboard = `{"panels":[
 {"title":"has data","targets":[{"refId":"A","rawSql":"SELECT 1"}]},
 {"title":"nothing on this device","knownEmpty":true,"targets":[{"refId":"A","rawSql":"SELECT 1"}]}
]}`

func stubGrafana(t *testing.T, body string) *dashboards.Grafana {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ds/query" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &dashboards.Grafana{URL: srv.URL, Token: "t"}
}

func TestDashboardsCheckToleratesKnownEmptyAndFailsOnTheRest(t *testing.T) {
	t.Parallel()
	const oneRow = `{"results":{"A":{"frames":[{"schema":{"fields":[{"name":"value","type":"number"}]},"data":{"values":[[1]]}}]}}}`
	const noFrames = `{"results":{"A":{"frames":[]}}}`

	// Every panel answers: nothing is tolerated because nothing needed to be.
	out := capture(t, func() {
		if err := dashboardsCheck(stubGrafana(t, oneRow), []byte(twoPanelDashboard), "influxdb", "uid", 15*time.Minute, time.Time{}, nil); err != nil {
			t.Errorf("every panel answering was reported as a failure: %v", err)
		}
	})
	if !strings.Contains(out, "every panel returns data") {
		t.Errorf("check printed:\n%s", out)
	}
	if strings.Contains(out, "FAIL") {
		t.Errorf("a panel with rows was marked FAIL:\n%s", out)
	}

	// Nothing answers: the known-empty panel is tolerated by name and the
	// other one fails, and the error counts both so the operator can see that
	// the tolerated ones were not the problem.
	var err error
	out = capture(t, func() {
		err = dashboardsCheck(stubGrafana(t, noFrames), []byte(twoPanelDashboard), "influxdb", "uid", 15*time.Minute, time.Time{}, nil)
	})
	if err == nil {
		t.Fatal("a panel returning nothing was not reported")
	}
	if !strings.Contains(err.Error(), "1 panel(s) return no data") || !strings.Contains(err.Error(), "1 known-empty tolerated") {
		t.Errorf("error = %q, want it to count both", err)
	}
	if !strings.Contains(out, "FAIL has data") || !strings.Contains(out, "none nothing on this device") {
		t.Errorf("check printed:\n%s", out)
	}

	// A Grafana that refuses is the check's own failure, not a panel's.
	dead := &dashboards.Grafana{URL: "http://127.0.0.1:1", Token: "t"}
	_ = capture(t, func() {
		if checkErr := dashboardsCheck(dead, []byte(twoPanelDashboard), "influxdb", "uid", time.Minute, time.Time{}, nil); checkErr == nil {
			t.Error("an unreachable Grafana returned no error")
		}
	})

	// A dashboard that is not JSON is refused before any query is made.
	if checkErr := dashboardsCheck(stubGrafana(t, oneRow), []byte("{{{"), "influxdb", "uid", time.Minute, time.Time{}, nil); checkErr == nil {
		t.Error("an unparseable dashboard returned no error")
	}
}
