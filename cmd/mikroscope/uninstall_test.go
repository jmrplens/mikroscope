package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/router"
)

func TestParseTargetsTakesTheNamesAndRefusesTheRest(t *testing.T) {
	t.Parallel()
	for list, want := range map[string][]string{
		"router":             {targetRouter},
		"dashboard,data":     {targetDashboard, targetData},
		" data , dashboard ": {targetData, targetDashboard},
		"all":                {targetRouter, targetDashboard, targetData},
		// all wins over whatever else is in the list: it is a superset.
		"data,all": {targetRouter, targetDashboard, targetData},
	} {
		got, err := parseTargets(list)
		if err != nil {
			t.Errorf("parseTargets(%q): %v", list, err)
			continue
		}
		if len(got) != len(want) {
			t.Errorf("parseTargets(%q) = %v, want %v", list, got, want)
			continue
		}
		for _, name := range want {
			if !got[name] {
				t.Errorf("parseTargets(%q) is missing %q", list, name)
			}
		}
	}
}

// A misspelled target removes LESS than was asked for, silently, which is the
// one failure mode a destructive verb must not have.
func TestParseTargetsRefusesAMisspelledName(t *testing.T) {
	t.Parallel()
	for _, list := range []string{"dashbord", "router,dta", "everything", ""} {
		if _, err := parseTargets(list); err == nil {
			t.Errorf("parseTargets(%q): want an error", list)
		}
	}
}

// THE GUARANTEE THE VERB IS BUILT AROUND: without --yes nothing is removed,
// and what would be removed is printed.
func TestWithoutYesNothingIsRemovedAndEverythingIsListed(t *testing.T) {
	t.Parallel()
	var dropped []string
	found := []removal{
		{what: "dashboard mikroscope-influxdb", drop: func(context.Context) error {
			dropped = append(dropped, "dashboard")
			return nil
		}},
		{what: "--influx mikroscope_cpu", drop: func(context.Context) error {
			dropped = append(dropped, "table")
			return nil
		}},
	}
	var out strings.Builder
	if err := carryOut(context.Background(), found, false, &out); err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 0 {
		t.Errorf("a dry run removed %v", dropped)
	}
	for _, want := range []string{
		"dashboard mikroscope-influxdb",
		"--influx mikroscope_cpu",
		"2 thing(s) would be removed. Nothing was. Add --yes",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("said %q, want it to carry %q", out.String(), want)
		}
	}
}

// One that will not go is reported and the rest still go: stopping at the
// first would leave the removal half done with no list of what is left.
func TestOneThatWillNotGoDoesNotStopTheRest(t *testing.T) {
	t.Parallel()
	var dropped []string
	found := []removal{
		{what: "first", drop: func(context.Context) error { dropped = append(dropped, "first"); return nil }},
		{what: "stubborn", drop: func(context.Context) error { return errors.New("it is in use") }},
		{what: "third", drop: func(context.Context) error { dropped = append(dropped, "third"); return nil }},
	}
	var out strings.Builder
	err := carryOut(context.Background(), found, true, &out)
	if err == nil {
		t.Fatal("want an error naming what could not be removed")
	}
	if !strings.Contains(err.Error(), "stubborn") {
		t.Errorf("error = %q, want it to name the one that stayed", err)
	}
	if len(dropped) != 2 {
		t.Errorf("removed %v, want the other two to have gone anyway", dropped)
	}
	if !strings.Contains(out.String(), "it is in use") {
		t.Errorf("said %q, want what the store said", out.String())
	}
}

func TestNothingToRemoveSaysSoRatherThanNothing(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	if err := carryOut(context.Background(), nil, true, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "" {
		t.Errorf("said %q for an empty list; the caller prints that line", out.String())
	}
}

// The file sinks are the two stores that need no server, so the whole
// list-then-remove path can be exercised against real files.
func TestTheFileSinksAreListedAndThenRemoved(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	jsonl := filepath.Join(dir, "sweep.jsonl")
	sql := filepath.Join(dir, "sweep.sql")
	for _, p := range []string{jsonl, sql, sql + ".1"} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sf := &sinkFlags{file: jsonl, sqlPath: sql}
	var out strings.Builder
	found, err := dataRemovals(context.Background(), sf, &out)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 3 {
		t.Fatalf("found %d file(s), want the three that are there: %+v", len(found), found)
	}
	// The rotation beside the file counts, which is the point of globbing
	// rather than naming: a run that rotated left more than one file behind.
	if !strings.Contains(out.String(), "--sql:") {
		t.Errorf("said %q, want the file sink's own caveat printed", out.String())
	}
	if err = carryOut(context.Background(), found, true, &out); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{jsonl, sql, sql + ".1"} {
		if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
			t.Errorf("%s is still there", p)
		}
	}
}

// An --influx that cannot be taken apart cannot say which database to empty,
// and must refuse rather than guess at one.
func TestDataRemovalsRefusesAWriteURLItCannotReadBack(t *testing.T) {
	t.Parallel()
	sf := &sinkFlags{influx: "http://host:8086/api/v2/write?bucket=m&org=home"}
	_, err := dataRemovals(context.Background(), sf, &strings.Builder{})
	if err == nil {
		t.Fatal("want an error: nothing here knows which database that URL fills")
	}
	if !strings.Contains(err.Error(), "--influx-db") {
		t.Errorf("error = %q, want it to name the flag that resolves it", err)
	}
}

// Publishing adopts a datasource and leaves it alone; removing has to leave it
// alone too, or an uninstall takes away somebody else's object.
func TestAnAdoptedDatasourceIsNotRemoved(t *testing.T) {
	t.Setenv("GRAFANA_TOKEN", "t")
	// No server: Exists fails, existing() returns nil, and the point is that
	// the datasource path is never even considered. A adopted uid short-
	// circuits before any request, so this asserts the shape rather than the
	// traffic.
	sf := &sinkFlags{influx: "http://i:8181", influxDB: "m"}
	pf := &publishFlags{url: "http://grafana.invalid", dsUID: "someone-elses"}
	found, err := dashboardRemovals(context.Background(), sf, pf)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range found {
		if strings.Contains(item.what, "datasource") {
			t.Errorf("an adopted datasource was listed for removal: %s", item.what)
		}
	}
}

// The router half lists rather than removes without --yes, and the listing is
// the install plan backwards, because an object goes after whatever depends on
// it. No router is reached: a listing is built from the options alone.
func TestTheRouterHalfListsWithoutTouchingTheRouter(t *testing.T) {
	t.Parallel()
	c := &cli{opts: router.Defaults()}
	if err := c.opts.Finish(); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := removeRouterObjects(c, false, &out); err != nil {
		t.Fatal(err)
	}
	said := out.String()
	if !strings.Contains(said, "add --yes to remove them") {
		t.Errorf("said %q, want it to say nothing was removed", said)
	}
	for _, want := range []string{"container ", "veth interface "} {
		if !strings.Contains(said, want) {
			t.Errorf("the listing does not name %q:\n%s", want, said)
		}
	}
	// The container is created first and so goes last in the install plan; the
	// listing is that plan reversed, so it comes first here.
	container := strings.Index(said, "container ")
	veth := strings.Index(said, "veth interface ")
	if container > veth {
		t.Errorf("the listing is not the plan backwards:\n%s", said)
	}
}

// The whole verb, driven the way somebody types it. The two file sinks are the
// only stores that need no server, which makes them the ones that can carry
// this end to end.
func TestTheVerbListsThenRemoves(t *testing.T) {
	dir := t.TempDir()
	jsonl := filepath.Join(dir, "sweep.jsonl")
	sql := filepath.Join(dir, "sweep.sql")
	for _, p := range []string{jsonl, sql} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"--targets", "data", "--file", jsonl, "--sql", sql}

	var listed strings.Builder
	if err := uninstall(args, &listed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listed.String(), "2 thing(s) would be removed. Nothing was.") {
		t.Errorf("said %q, want the list and no removal", listed.String())
	}
	for _, p := range []string{jsonl, sql} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("a run without --yes removed %s", p)
		}
	}

	var removed strings.Builder
	if err := uninstall(append(args, "--yes"), &removed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(removed.String(), "removed --file "+jsonl) {
		t.Errorf("said %q, want it to name what went", removed.String())
	}
	for _, p := range []string{jsonl, sql} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s is still there", p)
		}
	}
	// And again on the empty set: already gone is the outcome asked for.
	var again strings.Builder
	if err := uninstall(append(args, "--yes"), &again); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(again.String(), "nothing of this is here to remove") {
		t.Errorf("said %q, want it to say the stores are already empty", again.String())
	}
}

func TestTheVerbRefusesAMisspelledTarget(t *testing.T) {
	t.Parallel()
	err := uninstall([]string{"--targets", "dashbord"}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "unknown --targets name") {
		t.Fatalf("err = %v, want the refusal", err)
	}
}

// The dashboard half against a Grafana that holds one of the two objects: the
// listing names what is there and says nothing about what is not, because an
// uninstall lists what it would take away rather than what it wishes it could.
func TestTheDashboardHalfListsOnlyWhatIsThere(t *testing.T) {
	t.Setenv("GRAFANA_TOKEN", "glsa_test")
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete:
			deleted = append(deleted, r.URL.Path)
			w.WriteHeader(http.StatusOK)
		case strings.HasPrefix(r.URL.Path, "/api/dashboards/uid/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"dashboard":{"uid":"mikroscope-influxdb"}}`))
		default: // the datasource is not there
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	args := []string{
		"--targets", "dashboard",
		"--grafana", srv.URL,
		"--influx", "http://store:8181", "--influx-db", "mikroscope",
	}
	var listed strings.Builder
	if err := uninstall(args, &listed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listed.String(), "dashboard mikroscope-influxdb") {
		t.Errorf("said %q, want the dashboard that is there", listed.String())
	}
	if strings.Contains(listed.String(), "datasource mikroscope-influxdb") {
		t.Errorf("said %q, want nothing about the datasource that is not there", listed.String())
	}
	if len(deleted) != 0 {
		t.Errorf("a run without --yes deleted %v", deleted)
	}

	var removed strings.Builder
	if err := uninstall(append(args, "--yes"), &removed); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0] != "/api/dashboards/uid/mikroscope-influxdb" {
		t.Errorf("deleted %v, want the one dashboard", deleted)
	}
}

// The router target through the verb, which is the default and so the shape
// most people will type. No router is reached without --yes.
func TestTheVerbListsTheRouterObjectsByDefault(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	if err := uninstall(nil, &out); err != nil {
		t.Fatal(err)
	}
	said := out.String()
	if !strings.Contains(said, "router object(s) tagged") {
		t.Errorf("said %q, want the router objects", said)
	}
	if !strings.Contains(said, "add --yes to remove them") {
		t.Errorf("said %q, want it to say nothing was removed", said)
	}
	// And nothing about the stores, which were not asked for.
	if strings.Contains(said, "would be removed") {
		t.Errorf("said %q, want the store half silent", said)
	}
}

// An --influx that cannot be taken apart cannot say which database to empty,
// and the verb has to stop rather than empty the wrong one or none.
func TestTheVerbRefusesAWriteURLItCannotReadBack(t *testing.T) {
	t.Parallel()
	err := uninstall([]string{
		"--targets", "data",
		"--influx", "http://host:8086/api/v2/write?bucket=m&org=home",
	}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "--influx-db") {
		t.Fatalf("err = %v, want it to name the flag that resolves it", err)
	}
}
