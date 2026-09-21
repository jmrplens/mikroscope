package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jmrplens/mikroscope/internal/dashboards"
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
	dryRun bool
}

func (p *publishFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&p.url, "grafana", env("GRAFANA_URL", ""), "publish the dashboards to this Grafana at start; token from GRAFANA_TOKEN (MIKROSCOPE_GRAFANA_URL)")
	fs.StringVar(&p.folder, "grafana-folder", env("GRAFANA_FOLDER", "mikroscope"), "the Grafana folder to publish into; empty means the General folder")
	fs.StringVar(&p.dsUID, "grafana-datasource-uid", env("GRAFANA_DATASOURCE_UID", ""), "adopt this existing datasource instead of creating one — required for the stores that cannot describe their own")
	fs.BoolVar(&p.dryRun, "grafana-dry-run", false, "print the datasource and dashboard --grafana would write, write nothing, and stop before collecting")
}

// asked reports whether a Grafana was named at all.
func (p *publishFlags) asked() bool { return p.url != "" }

// publish reconciles a datasource and a dashboard per store the sinks write
// to. Everything it does is reported to out, line per object, so a --dry-run
// and a real run print the same list.
func (p *publishFlags) publish(ctx context.Context, s *sinkFlags, out io.Writer) error {
	token := os.Getenv("GRAFANA_TOKEN")
	if token == "" && !p.dryRun {
		return errors.New("--grafana needs GRAFANA_TOKEN: publishing without one would write as whoever an anonymous request is to that server")
	}
	stores := storesToPublish(s)
	if len(stores) == 0 {
		return errors.New("no sink this builds a dashboard for is configured, so there is nothing to publish")
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

	for _, store := range stores {
		if err := p.publishOne(ctx, g, s, store, folderUID, out); err != nil {
			return err
		}
	}
	return nil
}

// publishOne is the datasource and the dashboard for one store.
func (p *publishFlags) publishOne(ctx context.Context, g *dashboards.Grafana,
	s *sinkFlags, store dashboards.Store, folderUID string, out io.Writer,
) error {
	want, err := datasourceFor(store, s, p.dsUID)
	if err != nil {
		return err
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
		return err
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
// two runs of the same flags report the same way. Five of the eleven sinks have a
// dashboard; --file, --loki, --otlp, --telegraf and --stdout do not, and a
// collector writing only to those publishes nothing.
func storesToPublish(s *sinkFlags) []dashboards.Store {
	var out []dashboards.Store
	for _, named := range []struct {
		store      dashboards.Store
		configured bool
	}{
		{dashboards.Influx, s.influx != ""},
		{dashboards.Elasticsearch, s.elastic != ""},
		{dashboards.Prometheus, s.prom != ""},
		{dashboards.Postgres, s.sqlPath != ""},
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
// TWO OF THE FIVE SINKS KNOW THE ADDRESS GRAFANA QUERIES, because it is the
// address they write to. The other three do not, and no amount of reading
// their flags would find it: --prom SERVES /metrics and is scraped rather than
// written to, so the address Grafana asks belongs to a Prometheus this has
// never heard of; --sql writes statements to a file rather than to a server;
// --graphite speaks the carbon ingest port, which is not the API Grafana
// queries. Those three are adopted through --grafana-datasource-uid or they
// are not published, and saying so is better than creating a datasource
// pointed at a port that answers nothing.
func datasourceFor(store dashboards.Store, s *sinkFlags, adopt string) (dashboards.Datasource, error) {
	want := dashboards.Datasource{
		UID:  adopt,
		Name: "mikroscope-" + string(store),
		Type: dashboards.PluginID(store),
	}
	if want.UID == "" {
		want.UID = "mikroscope-" + string(store)
	}
	if adopt != "" {
		return want, nil
	}
	switch store {
	case dashboards.Influx:
		t, err := resolveInflux(s.influx, s.influxDB)
		if err != nil {
			return want, err
		}
		if t.Base == "" || t.Database == "" {
			return want, fmt.Errorf("--influx is a write URL this cannot take apart, so it cannot describe a "+
				"datasource: pass the server in --influx and the database in --influx-db, or name an existing "+
				"datasource in --grafana-datasource-uid (%s)", s.influx)
		}
		want.URL = t.Base
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
			"insecureGrpc": strings.HasPrefix(t.Base, "http://"),
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
	case dashboards.Elasticsearch:
		want.URL = strings.TrimRight(s.elastic, "/")
		want.JSON = map[string]any{
			"index":     elasticIndexPattern(s.elIndex),
			"timeField": "time",
		}
		if s.elasticAuth != "" {
			want.Secret = map[string]string{"httpHeaderValue1": s.elasticAuth}
			want.JSON["httpHeaderName1"] = "Authorization"
		}
	default:
		return want, fmt.Errorf("the %s sink does not know the address Grafana would query, so it cannot "+
			"describe a datasource: create one in Grafana and name it in --grafana-datasource-uid", store)
	}
	return want, nil
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
