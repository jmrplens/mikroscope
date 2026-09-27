package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/jmrplens/mikroscope/internal/dashboards"
	"github.com/jmrplens/mikroscope/internal/sinks"
)

// publishFlags is `forward --grafana`: point the collector at a Grafana and it
// reconciles one datasource and one dashboard per store it writes to, once, at
// start, before the first sample.
//
// It is off unless --grafana is passed. A collector that wrote to somebody's
// Grafana because it could is not a collector anyone should run, and the token
// is required for the same reason: some Grafanas accept an anonymous request
// and would publish under whoever the server thinks is asking.
type publishFlags struct {
	url    string
	folder string
	dsUID  string
	dsURL  string
	dsSSL  string
	dryRun bool
}

// register adds the --grafana flags to `dashboards publish` and `uninstall`,
// with --grafana read from MIKROSCOPE_GRAFANA_URL and then from GRAFANA_URL.
// Naming a Grafana is what those two verbs are for, and `dashboards import`
// and `check` have always read GRAFANA_URL: one variable set for the one verb
// should reach the others in the same family.
func (p *publishFlags) register(fs *flag.FlagSet) {
	p.registerWith(fs, firstNonEmpty(env("GRAFANA_URL", ""), os.Getenv("GRAFANA_URL")),
		"the Grafana the dashboards are published to; token from GRAFANA_TOKEN (MIKROSCOPE_GRAFANA_URL, then GRAFANA_URL)")
}

// registerForCollector is register for `forward`, whose --grafana reads
// MIKROSCOPE_GRAFANA_URL and never the unprefixed GRAFANA_URL.
//
// NOT THE FALLBACK THE OTHER VERBS HAVE. GRAFANA_URL and GRAFANA_TOKEN are
// unprefixed names that other Grafana tooling may read too, so a shell set up
// for it can hold both, and a collector started from that shell would begin
// writing a folder, a datasource and a dashboard into that Grafana without
// anybody having asked it to: exactly what "off unless --grafana is passed"
// is there to prevent. The prefixed name is one somebody set for this.
func (p *publishFlags) registerForCollector(fs *flag.FlagSet) {
	p.registerWith(fs, env("GRAFANA_URL", ""),
		"publish the dashboards to this Grafana once, at start; token from GRAFANA_TOKEN (MIKROSCOPE_GRAFANA_URL)")
}

func (p *publishFlags) registerWith(fs *flag.FlagSet, grafanaURL, grafanaUsage string) {
	fs.StringVar(&p.url, "grafana", grafanaURL, grafanaUsage)
	fs.StringVar(&p.folder, "grafana-folder", env("GRAFANA_FOLDER", "mikroscope"), "the Grafana folder to publish into; empty means the General folder")
	fs.StringVar(&p.dsUID, "grafana-datasource-uid", env("GRAFANA_DATASOURCE_UID", ""), "adopt this existing datasource instead of creating one; required only for --sql, while --prom and --graphite can use --grafana-datasource-url instead. One store per run")
	fs.StringVar(&p.dsURL, "grafana-datasource-url", env("GRAFANA_DATASOURCE_URL", ""), "the address Grafana queries, for the sinks that cannot know it: --prom, --graphite. It also overrides the address a sink does know. One store per run")
	fs.StringVar(&p.dsSSL, "grafana-datasource-sslmode", env("GRAFANA_DATASOURCE_SSLMODE", ""), "sslmode for the PostgreSQL datasource: disable, require, verify-ca or verify-full. Read from --postgres when it names one Grafana understands")
	fs.BoolVar(&p.dryRun, "grafana-dry-run", false, "print the datasource and dashboard --grafana would write and write nothing (forward: then stop before collecting)")
}

// dashboardsPublish is `dashboards publish`: what `forward --grafana` does at
// start, done once, with no router and no collector, and then exit. It takes
// the sink flags forward takes, because each datasource is described from the
// sink that writes to it, and the same --grafana flags. The collector carries
// on when Grafana refuses (publishOrCarryOn); here the publish is the whole
// run, so a refusal is the command's error and its exit status.
func dashboardsPublish(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("mikroscope dashboards publish", flag.ContinueOnError)
	var sf sinkFlags
	var pf publishFlags
	sf.register(fs)
	pf.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("dashboards publish takes flags only, not %q", fs.Arg(0))
	}
	if !pf.asked() {
		return errors.New("dashboards publish needs --grafana (or MIKROSCOPE_GRAFANA_URL or GRAFANA_URL), with the token in GRAFANA_TOKEN")
	}
	if !sf.any() {
		return errors.New("dashboards publish needs the sink flags the collector runs with (--influx, --prom, --postgres, --sql, --graphite or --elastic): the datasource is described from them")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return pf.publish(ctx, &sf, out)
}

// asked reports whether a Grafana was named at all.
func (p *publishFlags) asked() bool { return p.url != "" }

// publish reconciles a datasource and a dashboard per store the sinks write
// to. Everything it does is reported to out, line per object, so a --dry-run
// and a real run print the same list.
//
// The flags are checked before anything else, the token included, so a run
// that could not do what it was told says so before it is given a credential,
// and a dry run refuses what the real run would.
func (p *publishFlags) publish(ctx context.Context, s *sinkFlags, out io.Writer) error {
	stores := storesToPublish(s)
	if len(stores) == 0 {
		return errors.New("no sink this builds a dashboard for is configured, so there is nothing to publish")
	}
	if err := p.check(stores); err != nil {
		return err
	}
	token := os.Getenv("GRAFANA_TOKEN")
	if token == "" && !p.dryRun {
		return errors.New("--grafana needs GRAFANA_TOKEN: publishing without one would write as whoever an anonymous request is to that server")
	}
	g := &dashboards.Grafana{URL: strings.TrimRight(p.url, "/"), Token: token}

	folderUID := ""
	if !p.dryRun {
		uid, outcome, err := g.EnsureFolder(ctx, p.folder)
		if err != nil {
			return fmt.Errorf("the folder %q: %w", p.folder, err)
		}
		folderUID = uid
		if p.folder != "" {
			fmt.Fprintf(out, "folder %q (%s) %s\n", p.folder, uid, outcome)
		}
	} else if p.folder != "" {
		fmt.Fprintf(out, "would ensure folder %q\n", p.folder)
	}

	// EVERY STORE GETS ITS TURN. One store's datasource failing says nothing
	// about the next one's, which has its own address, plugin and permissions:
	// stopping at the first left the stores after it unpublished and
	// unmentioned, and the operator to find out one run at a time. Each
	// store's outcome is a line of out or an error naming the store, and the
	// errors come back joined. Only a canceled run stops early, because every
	// store after it would fail the same way.
	var failed storeErrors
	for _, store := range stores {
		if ctx.Err() != nil {
			failed = append(failed, fmt.Errorf("%s: not published: %w", store, ctx.Err()))
			break
		}
		if err := p.publishOne(ctx, g, s, store, folderUID, out); err != nil {
			failed = append(failed, err)
		}
	}
	if len(failed) == 0 {
		return nil
	}
	return failed
}

// storeErrors is publish's error when one store or more could not be
// published: each store's own error, in the order the stores were tried. It
// reads as errors.Join does, one line per store, and unwraps to the list, so
// the collector can warn once per store (publishOrCarryOn).
type storeErrors []error

func (f storeErrors) Error() string   { return errors.Join(f...).Error() }
func (f storeErrors) Unwrap() []error { return f }

// check refuses the flag values no store could be published with.
//
// ONE DATASOURCE ADDRESS, ONE STORE. --grafana-datasource-url and
// --grafana-datasource-uid each take a single value, and every store of a run
// was given it: a collector writing to InfluxDB and Prometheus with
// --grafana-datasource-url pointed at Prometheus built an InfluxDB datasource
// that queried Prometheus's port. There is no value either flag could hold
// that is right for two stores, which are different servers read by
// different plugins, so a run of several stores that sets one is refused
// before anything is written, and the message says how to do it instead.
func (p *publishFlags) check(stores []dashboards.Store) error {
	if p.dsSSL != "" && !slices.Contains(grafanaSSLModes, p.dsSSL) {
		return fmt.Errorf("--grafana-datasource-sslmode %q is not a mode Grafana's PostgreSQL datasource has: use one of %s",
			p.dsSSL, strings.Join(grafanaSSLModes, ", "))
	}
	if len(stores) < 2 {
		return nil
	}
	var set []string
	if p.dsURL != "" {
		set = append(set, "--grafana-datasource-url")
	}
	if p.dsUID != "" {
		set = append(set, "--grafana-datasource-uid")
	}
	if len(set) == 0 {
		return nil
	}
	names := make([]string, len(stores))
	for i, store := range stores {
		names[i] = string(store)
	}
	verb := "names"
	if len(set) > 1 {
		verb = "name"
	}
	return fmt.Errorf("%s %s one datasource for every store, and this run has %d (%s): "+
		"publish them one at a time with `mikroscope dashboards publish`, each with that store's sink flag "+
		"and its own --grafana-datasource-url or --grafana-datasource-uid, and run the collector without them",
		strings.Join(set, " and "), verb, len(stores), strings.Join(names, ", "))
}

// publishOne is the datasource and the dashboard for one store.
func (p *publishFlags) publishOne(ctx context.Context, g *dashboards.Grafana,
	s *sinkFlags, store dashboards.Store, folderUID string, out io.Writer,
) error {
	want, err := datasourceFor(store, s, p, out)
	if err != nil {
		// Named here because publish reports every store's failure together,
		// and --influx's or --sql's own message does not say which store.
		return fmt.Errorf("the datasource for %s: %w", store, err)
	}
	switch {
	case p.dsUID != "":
		// An adopted datasource is somebody else's to describe. Correcting one
		// this did not make would overwrite settings nobody asked it to have.
		fmt.Fprintf(out, "%s: datasource %s adopted as configured, left as it is\n", store, want.UID)
	case p.dryRun:
		fmt.Fprintf(out, "%s: would write datasource %s (%s) at %s\n", store, want.UID, want.Type, want.URL)
	default:
		outcome, dsErr := g.EnsureDatasource(ctx, want)
		if dsErr != nil {
			return fmt.Errorf("the datasource for %s: %w", store, dsErr)
		}
		fmt.Fprintf(out, "%s: datasource %s (%s) %s\n", store, want.UID, want.Type, outcome)
	}

	// Ask the store what it holds before generating, exactly as `dashboards
	// import` does, so "not available on this device" stays measured rather
	// than compiled in. A probe that fails is a warning: the compiled defaults
	// are a working dashboard and the operator is told which one they got.
	var present map[string]bool
	if !p.dryRun {
		got, probeErr := g.Measurements(ctx, dashboards.PluginID(store), want.UID)
		if probeErr != nil {
			fmt.Fprintf(out, "%s: could not ask the datasource which measurements it holds (%v); using the compiled defaults\n", store, probeErr)
		} else {
			present = got
		}
	}
	doc, err := dashboards.GenerateFor(store, present)
	if err != nil {
		return fmt.Errorf("the dashboard for %s: %w", store, err)
	}
	if p.dryRun {
		fmt.Fprintf(out, "%s: would write dashboard %q (%d bytes) into folder %q\n",
			store, dashboards.Title(store), len(doc), p.folder)
		return nil
	}
	url, err := g.ImportInto(ctx, doc, dashboards.PluginID(store), want.UID, folderUID)
	if err != nil {
		return fmt.Errorf("the dashboard for %s: %w", store, err)
	}
	fmt.Fprintf(out, "%s: dashboard %s%s\n", store, g.URL, url)
	return nil
}

// storesToPublish is one store per metric sink configured, in a fixed order so
// two runs of the same flags report the same way. Six of the eleven sinks feed
// the five dashboards, --postgres and --sql sharing one; --file, --loki,
// --otlp, --telegraf and --stdout have none, and a collector writing only to
// those publishes nothing.
func storesToPublish(s *sinkFlags) []dashboards.Store {
	var out []dashboards.Store
	for _, named := range []struct {
		store      dashboards.Store
		configured bool
	}{
		{dashboards.Influx, s.influx != ""},
		{dashboards.Elasticsearch, s.elastic != ""},
		{dashboards.Prometheus, s.prom != ""},
		// EITHER SQL SINK puts the postgres store on the list. --postgres was
		// added in 1.1.0 and this check still only knew --sql, so a collector
		// writing to a live PostgreSQL and nothing else published nothing at
		// all and said there was nothing to publish. Caught by the end-to-end
		// run that publishes all five, not by any unit test: storesToPublish
		// was right about the four stores its own test covered.
		{dashboards.Postgres, s.sqlPath != "" || s.postgres != ""},
		{dashboards.Graphite, s.graph != ""},
	} {
		if named.configured {
			out = append(out, named.store)
		}
	}
	return out
}

// datasourceFor describes the datasource a store's dashboard reads from.
//
// FIVE OF FIVE, by two different routes. Three sinks KNOW the address Grafana
// queries, because it is the address they write to or dial: --influx,
// --elastic and --postgres. The other two cannot know it and no amount of
// reading their flags would find it — --prom SERVES /metrics and is scraped,
// so the Prometheus Grafana asks is one this has never heard of; --graphite
// speaks the carbon ingest port, which is not the web API Grafana queries and
// is usually not even the same port. Told the address in
// --grafana-datasource-url, though, there is nothing else to derive: a
// Prometheus datasource is a URL, and so is a Graphite one.
//
// That leaves exactly one that can never be described: --sql, which writes
// statements to a file and never connects, so there is no host, port, user or
// password anywhere in the flags to build one out of. It says so, and names
// --postgres, which is the sink that can.
func datasourceFor(store dashboards.Store, s *sinkFlags, p *publishFlags, out io.Writer) (dashboards.Datasource, error) {
	want := dashboards.Datasource{
		UID:  p.dsUID,
		Name: "mikroscope-" + string(store),
		Type: dashboards.PluginID(store),
	}
	if want.UID == "" {
		want.UID = "mikroscope-" + string(store)
	}
	if p.dsUID != "" {
		return want, nil
	}
	switch {
	case store == dashboards.Influx:
		t, err := resolveInflux(s.influx, s.influxDB)
		if err != nil {
			return want, err
		}
		if t.Base == "" || t.Database == "" {
			return want, fmt.Errorf("--influx is a write URL this cannot take apart, so it cannot describe a "+
				"datasource: pass the server in --influx and the database in --influx-db, or name an existing "+
				"datasource in --grafana-datasource-uid (%s)", s.influx)
		}
		want.URL = firstNonEmpty(p.dsURL, t.Base)
		// dbName, NOT the top-level `database` field: the InfluxDB 3 SQL
		// plugin reads the database out of jsonData, and the datasource that
		// works against the reference store leaves `database` empty.
		want.JSON = map[string]any{
			"version":         "SQL",
			"httpMode":        "POST",
			"dbName":          t.Database,
			"httpHeaderName1": "Authorization",
			// insecureGrpc FOLLOWS THE SCHEME THE SINK WRITES TO. The plugin
			// has two transports: HTTP for some calls and FlightSQL over gRPC
			// for the rest, and the gRPC side attempts TLS unless this is set.
			// Against a plain-HTTP InfluxDB, a datasource without it answers
			// every panel with `transport: authentication handshake failed:
			// tls: first record does not look like a TLS handshake` — measured
			// on 2026-09-19 against the reference store, by creating the
			// datasource without it. A store reached over https gets the TLS
			// handshake it is expecting, so the flag is not simply always on.
			"insecureGrpc": strings.HasPrefix(want.URL, "http://"),
		}
		// BOTH PLACES. This plugin reads the token from one and the header
		// from the other depending on the call: with only httpHeaderValue1
		// set, the panels answer `flightsql: Unauthenticated` (Grafana 12.3.2,
		// measured 2026-09-12), and with only token set the HTTP path is
		// unauthenticated instead. It is the single most common way to build
		// this datasource by hand and get a dashboard where nothing loads.
		if s.influxToken != "" {
			want.Secret = map[string]string{
				"token":            s.influxToken,
				"httpHeaderValue1": "Bearer " + s.influxToken,
			}
		}
	case store == dashboards.Elasticsearch:
		want.URL = firstNonEmpty(p.dsURL, strings.TrimRight(s.elastic, "/"))
		want.JSON = map[string]any{
			"index": elasticIndexPattern(s.elIndex),
			// @timestamp, THE FIELD THE SINK STAMPS every document with, and
			// the one every panel's query, the annotations and the stores
			// suite's hand-built datasource name. It was "time", a field no
			// document has. What that did to the panels was not run: the
			// stores suite checked the collector's datasource for query
			// errors, and a range over a missing field is expected to answer
			// with nothing rather than with an error.
			"timeField": "@timestamp",
		}
		// THE HEADER THE SINK SENDS, not the variable as written: `user:password`
		// becomes basic auth and anything else an API key behind its scheme
		// (sinks.ElasticAuthorization). Copied as written, neither form was a
		// header Elasticsearch accepts.
		if auth := sinks.ElasticAuthorization(s.elasticAuth); auth != "" {
			want.Secret = map[string]string{"httpHeaderValue1": auth}
			want.JSON["httpHeaderName1"] = "Authorization"
		}
	case store == dashboards.Postgres && s.postgres != "":
		// The connecting sink knows the server, because it dials it. Everything
		// Grafana needs is in the connection string.
		if err := fromDSN(&want, s.postgres, p.dsURL, p.dsSSL, out); err != nil {
			return want, err
		}
	case store == dashboards.Prometheus && p.dsURL != "":
		// Told rather than derived: the sink is scraped rather than written
		// to, so it has no idea where the Prometheus server is. Told the
		// address, there is nothing else to know — a Prometheus datasource is
		// a URL.
		want.URL = p.dsURL
		want.JSON = map[string]any{"httpMethod": "POST"}
	case store == dashboards.Graphite && p.dsURL != "":
		// Also told, and for a sharper reason: the sink speaks the ingest port
		// while Grafana queries the web API, which is a different port on the
		// same host.
		want.URL = p.dsURL
		want.JSON = map[string]any{"graphiteVersion": "1.1"}
	case store == dashboards.Postgres:
		// Only the file sink is configured. It writes statements and never
		// connects, so there is no host, port, user or password anywhere in
		// the flags to build a datasource out of.
		return want, errors.New(
			"--sql writes statements to a file and never connects, so nothing here knows the " +
				"server Grafana would query: use --postgres instead, which does, or create the " +
				"datasource in Grafana and name it in --grafana-datasource-uid",
		)
	default:
		return want, fmt.Errorf(
			"the %s sink does not know the address Grafana would query: pass it in "+
				"--grafana-datasource-url, or create the datasource in Grafana and name it in "+
				"--grafana-datasource-uid", store,
		)
	}
	if want.URL == "" {
		return want, fmt.Errorf("the %s sink names no address, so pass --grafana-datasource-url", store)
	}
	return want, nil
}

// firstNonEmpty is the first of these that says something.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// elasticIndexPattern turns --elastic-index into the wildcard a datasource
// reads. The sink expands %Y %m %d per event, so `mikroscope-%Y.%m.%d` is a
// new index every day and a datasource must ask for all of them.
func elasticIndexPattern(index string) string {
	if before, _, found := strings.Cut(index, "%"); found {
		return strings.TrimRight(before, ".-") + "*"
	}
	if index == "" {
		return "mikroscope*"
	}
	return index
}
