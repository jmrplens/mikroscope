package main

import (
	"context"
	"errors"
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
