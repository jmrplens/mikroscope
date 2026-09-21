package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/jmrplens/mikroscope/internal/dashboards"
	"github.com/jmrplens/mikroscope/internal/router"
	"github.com/jmrplens/mikroscope/internal/teardown"
)

// What `uninstall --targets` takes.
const (
	targetRouter    = "router"
	targetDashboard = "dashboard"
	targetData      = "data"
	targetAll       = "all"
)

// uninstallTargets is every name --targets takes, for the usage line and for
// refusing a misspelled one rather than quietly removing less than was asked.
var uninstallTargets = []string{targetRouter, targetDashboard, targetData, targetAll}

// removal is one thing that would go, with the line it prints as and the call
// that takes it away.
type removal struct {
	what string
	drop func(context.Context) error
}

// runUninstall removes what this put in place.
//
// THE DEFAULT IS ROUTER, AND THE DEFAULT IS A LIST. `uninstall` with no flags
// has meant "the objects install created on the router" since 1.0.0, and it
// still does: widening what an existing destructive verb does by default is
// not a thing to do to somebody who has it in a script. Everything else is
// opt-in through --targets.
//
// And nothing at all is removed without --yes. The alternative is one mistyped
// command that empties a store, and unlike the router objects — which `install`
// puts back — a dropped table is a dropped table.
func runUninstall(args []string, _ cli) error {
	return uninstall(args, os.Stdout)
}

// uninstall is runUninstall with the destination named, so the whole verb can
// be driven by a test rather than only the pieces under it.
func uninstall(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("mikroscope uninstall", flag.ContinueOnError)
	var (
		targets string
		sf      sinkFlags
		pf      publishFlags
	)
	fs.StringVar(&targets, "targets", targetRouter,
		"what to remove, comma-separated: "+strings.Join(uninstallTargets, ", "))
	sf.register(fs)
	pf.register(fs)
	c, err := parseWith("uninstall", args, fs)
	if err != nil {
		return err
	}
	// --yes is the deployment flag, not one of this verb's own: it means the
	// same thing here as it does on install, and two flags spelled the same
	// would be a flag redefined.
	yes := c.yes
	wanted, err := parseTargets(targets)
	if err != nil {
		return err
	}
	ctx := context.Background()

	if wanted[targetRouter] {
		if failed := removeRouterObjects(&c, yes, out); failed != nil {
			return failed
		}
	}

	var found []removal
	if wanted[targetDashboard] {
		grafanaSide, failed := dashboardRemovals(ctx, &sf, &pf)
		if failed != nil {
			return failed
		}
		found = append(found, grafanaSide...)
	}
	if wanted[targetData] {
		storeSide, failed := dataRemovals(ctx, &sf, out)
		if failed != nil {
			return failed
		}
		found = append(found, storeSide...)
	}
	if len(found) == 0 && !wanted[targetRouter] {
		fmt.Fprintln(out, "nothing of this is here to remove")
		return nil
	}
	return carryOut(ctx, found, yes, out)
}

// removeRouterObjects is the router half, which keeps its own shape: Uninstall
// already prints a line per object and verifies by ownership count afterwards,
// and re-listing it here would say the same thing twice in two formats.
func removeRouterObjects(c *cli, yes bool, out io.Writer) error {
	if !yes {
		fmt.Fprintln(out, "router objects (add --yes to remove them):")
		router.RemovalListing(c.opts, out)
		return nil
	}
	r, err := c.runner()
	if err != nil {
		return err
	}
	return router.Uninstall(r, c.opts, out)
}

// carryOut prints what would go, and takes it away when told to.
func carryOut(ctx context.Context, found []removal, confirmed bool, out io.Writer) error {
	if len(found) == 0 {
		return nil
	}
	for _, item := range found {
		fmt.Fprintf(out, "  %s\n", item.what)
	}
	if !confirmed {
		fmt.Fprintf(out, "\n%d thing(s) would be removed. Nothing was. Add --yes to go ahead.\n",
			len(found))
		return nil
	}
	fmt.Fprintln(out)
	var failed []string
	for _, item := range found {
		if err := item.drop(ctx); err != nil {
			// ONE THAT WILL NOT GO IS REPORTED AND THE REST STILL GO. Stopping
			// at the first would leave the removal half done and no list of
			// what is left, which is the worst of both outcomes.
			fmt.Fprintf(out, "  could not remove %s: %v\n", item.what, err)
			failed = append(failed, item.what)
			continue
		}
		fmt.Fprintf(out, "  removed %s\n", item.what)
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d of %d could not be removed: %s",
			len(failed), len(found), strings.Join(failed, ", "))
	}
	return nil
}

// parseTargets turns the comma-separated list into a set, refusing a name it
// does not know rather than removing less than was asked for.
func parseTargets(list string) (map[string]bool, error) {
	wanted := map[string]bool{}
	for name := range strings.SplitSeq(list, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !slices.Contains(uninstallTargets, name) {
			return nil, fmt.Errorf("unknown --targets name %q, expected some of %s",
				name, strings.Join(uninstallTargets, ", "))
		}
		if name == targetAll {
			return map[string]bool{targetRouter: true, targetDashboard: true, targetData: true}, nil
		}
		wanted[name] = true
	}
	if len(wanted) == 0 {
		return nil, errors.New("--targets named nothing")
	}
	return wanted, nil
}

// dashboardRemovals is the dashboard and the datasource per store this
// collector writes to, for the ones that are actually there.
func dashboardRemovals(ctx context.Context, sf *sinkFlags, pf *publishFlags) ([]removal, error) {
	if pf.url == "" {
		return nil, errors.New("--targets dashboard needs --grafana, and GRAFANA_TOKEN: " +
			"without them there is nothing this could have published")
	}
	token := os.Getenv("GRAFANA_TOKEN")
	if token == "" {
		return nil, errors.New("--targets dashboard needs GRAFANA_TOKEN")
	}
	g := &dashboards.Grafana{URL: strings.TrimRight(pf.url, "/"), Token: token}
	var found []removal
	for _, store := range storesToPublish(sf) {
		uid := "mikroscope-" + string(store)
		if item := existing(ctx, g, "dashboard", "/api/dashboards/uid/"+uid, uid); item != nil {
			found = append(found, *item)
		}
		// AN ADOPTED DATASOURCE IS NOT OURS TO TAKE AWAY. It was somebody
		// else's before this ran and it is somebody else's after.
		if pf.dsUID != "" {
			continue
		}
		if item := existing(ctx, g, "datasource", "/api/datasources/uid/"+uid, uid); item != nil {
			found = append(found, *item)
		}
	}
	return found, nil
}

// existing is a removal for an object that is there, and nil for one that is
// not: an uninstall lists what it would take away, not what it wishes it could.
func existing(ctx context.Context, g *dashboards.Grafana, kind, path, uid string) *removal {
	there, err := g.Exists(ctx, path)
	if err != nil || !there {
		return nil
	}
	return &removal{
		what: kind + " " + uid,
		drop: func(ctx context.Context) error { return g.Delete(ctx, path) },
	}
}

// dataRemovals is every item every emptiable store holds, and a printed reason
// for each sink whose store cannot be emptied from here.
func dataRemovals(ctx context.Context, sf *sinkFlags, out io.Writer) ([]removal, error) {
	influxURL, influxDB := "", ""
	if sf.influx != "" {
		t, err := resolveInflux(sf.influx, sf.influxDB)
		if err != nil {
			return nil, err
		}
		// The base and the database, not the write URL: dropping a table is a
		// different endpoint from writing to one.
		influxURL, influxDB = t.Base, t.Database
		if influxURL == "" {
			return nil, fmt.Errorf("--influx is a write URL this cannot take apart, so it cannot "+
				"say which database to empty: pass the server in --influx and the database in "+
				"--influx-db (%s)", sf.influx)
		}
	}
	stores, cannot := teardown.For(teardown.Sinks{
		InfluxURL: influxURL, InfluxToken: sf.influxToken, InfluxDB: influxDB,
		ElasticURL: sf.elastic, ElasticAuth: sf.elasticAuth, ElasticIndex: sf.elIndex,
		PostgresDSN: sf.postgres,
		SQLPath:     sf.sqlPath,
		FilePath:    sf.file,
		PromAddr:    sf.prom, GraphiteAddr: sf.graph, LokiURL: sf.loki,
		OTLPURL: sf.otlp, TelegrafURL: sf.telegraf,
	})
	var found []removal
	for _, store := range stores {
		held, err := store.Holds(ctx)
		if err != nil {
			return nil, fmt.Errorf("asking %s what it holds: %w", store.Name(), err)
		}
		for _, item := range held {
			found = append(found, removal{
				what: store.Name() + " " + item,
				drop: func(ctx context.Context) error { return store.Drop(ctx, item) },
			})
		}
	}
	// Printed rather than returned: silence would read as nothing to remove,
	// which is the opposite of what these mean.
	for _, why := range cannot {
		fmt.Fprintf(out, "  not removed — %s\n", why)
	}
	return found, nil
}
