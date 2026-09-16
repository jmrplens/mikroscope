package main

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/jmrplens/mikroscope/internal/sinks"
	"github.com/jmrplens/mikroscope/internal/version"
)

// promHistogramRateHz is the nominal sampler rate the Prometheus sink uses to
// size its busy-ratio histogram. It is the agent's default, not a measurement
// of the connected agent: the histogram only needs the right order of
// magnitude, and a collector that changed bucket layout when it reconnected
// to a differently configured agent would break every existing query.
const promHistogramRateHz = 10

// sinkFlags is every destination `forward` can write to. They are in one
// struct with one builder because there are ten of them now: a chain of ten
// `if` blocks inside runForward was what pushed it past the complexity limit,
// and more importantly it made it easy to add a sink to the flags and forget
// to construct it.
//
// More than one at a time is the normal arrangement — the kernel tier at
// 10 Hz into InfluxDB for history, Prometheus for alerting, Loki for the
// kernel-log events, and a file as a durable buffer is a reasonable set.
type sinkFlags struct {
	file     string
	prom     string
	influx   string
	loki     string
	lokiTen  string
	otlp     string
	graph    string
	graphPx  string
	elastic  string
	elIndex  string
	sqlPath  string
	sqlHyper bool
	telegraf string
	stdout   string

	// Credentials never come from a flag: a flag is visible in `ps` and in a
	// shell history. Each is read from the environment by register().
	influxToken, lokiToken, otlpToken, elasticAuth, telegrafToken string

	hostTag      string
	queueSeconds int
}

// register declares the flags and reads the credentials from the environment.
func (s *sinkFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&s.file, "file", "", "sink: write the merged timeline as JSONL to this path")
	fs.StringVar(&s.prom, "prom", "", "sink: serve Prometheus /metrics on this address, e.g. :9124")
	fs.StringVar(&s.influx, "influx", env("INFLUX_URL", ""), "sink: InfluxDB 3 write URL, e.g. http://host:8181/api/v3/write_lp?db=mikroscope&precision=nanosecond (MIKROSCOPE_INFLUX_URL)")
	fs.StringVar(&s.loki, "loki", env("LOKI_URL", ""), "sink: Loki push URL, e.g. http://host:3100/loki/api/v1/push — carries the kernel-log events and gaps, not the metrics (MIKROSCOPE_LOKI_URL)")
	fs.StringVar(&s.lokiTen, "loki-tenant", env("LOKI_TENANT", ""), "sink: X-Scope-OrgID for a multi-tenant Loki (MIKROSCOPE_LOKI_TENANT)")
	fs.StringVar(&s.otlp, "otlp", env("OTLP_URL", ""), "sink: OTLP/HTTP metrics endpoint, e.g. http://host:4318/v1/metrics (MIKROSCOPE_OTLP_URL)")
	fs.StringVar(&s.graph, "graphite", env("GRAPHITE_ADDR", ""), "sink: Graphite carbon plaintext listener, host:port (MIKROSCOPE_GRAPHITE_ADDR)")
	fs.StringVar(&s.graphPx, "graphite-prefix", "mikroscope", "sink: first node of every Graphite metric path")
	fs.StringVar(&s.elastic, "elastic", env("ELASTIC_URL", ""), "sink: Elasticsearch/OpenSearch base URL for _bulk, e.g. http://host:9200 (MIKROSCOPE_ELASTIC_URL)")
	fs.StringVar(&s.elIndex, "elastic-index", "mikroscope-%Y.%m.%d", "sink: Elasticsearch index name; %Y %m %d expand to the event's date")
	fs.StringVar(&s.sqlPath, "sql", "", "sink: write PostgreSQL/TimescaleDB statements to this path (deliberately driverless: pipe the file into psql)")
	fs.BoolVar(&s.sqlHyper, "sql-hypertable", false, "sink: emit TimescaleDB create_hypertable in the SQL header")
	fs.StringVar(&s.telegraf, "telegraf", env("TELEGRAF_URL", ""), "sink: Telegraf listener — http://host:8186/telegraf, tcp://host:8094 or udp://host:8094 (MIKROSCOPE_TELEGRAF_URL)")
	fs.StringVar(&s.stdout, "stdout", "", "sink: write to stdout as `lp` (InfluxDB line protocol) or `json` (NDJSON)")
	fs.StringVar(&s.hostTag, "host-tag", env("HOST_TAG", "router"), "host tag or label on every point, in every sink (MIKROSCOPE_HOST_TAG)")
	fs.IntVar(&s.queueSeconds, "queue-seconds", 60, "how many seconds of data each queued sink may hold before it drops the oldest batch")

	s.influxToken = env("INFLUX_TOKEN", "")
	s.lokiToken = env("LOKI_TOKEN", "")
	s.otlpToken = env("OTLP_TOKEN", "")
	s.elasticAuth = env("ELASTIC_AUTH", "")
	s.telegrafToken = env("TELEGRAF_TOKEN", "")
}

// any reports whether at least one sink was asked for. `forward` with no sink
// would read the router and throw the data away, which is never what was
// meant, so it is an error rather than a silent no-op.
func (s *sinkFlags) any() bool {
	return s.file != "" || s.prom != "" || s.influx != "" || s.loki != "" || s.otlp != "" ||
		s.graph != "" || s.elastic != "" || s.sqlPath != "" || s.telegraf != "" || s.stdout != ""
}

// build constructs every sink that was asked for, in a fixed order so the
// startup lines an operator sees are stable. A constructor that fails fails
// the run: a sink that was asked for and silently absent is worse than no
// data at all, because the absence is invisible.
func (s *sinkFlags) build(ctx context.Context, rateHz int, logf func(string)) ([]sinks.Sink, error) {
	q := s.queueSeconds
	if q <= 0 {
		q = 60
	}
	var out []sinks.Sink
	add := func(sk sinks.Sink) { out = append(out, sk) }

	if s.file != "" {
		f, err := sinks.NewFile(s.file)
		if err != nil {
			return nil, err
		}
		add(f)
	}
	if s.prom != "" {
		p, err := sinks.NewPrometheus(ctx, s.prom, rateHz, version.Version)
		if err != nil {
			return nil, err
		}
		add(p)
	}
	if s.sqlPath != "" {
		q, err := sinks.NewSQL(s.sqlPath, s.hostTag, s.sqlHyper, logf)
		if err != nil {
			return nil, err
		}
		add(q)
	}
	if s.stdout != "" {
		if s.stdout != "lp" && s.stdout != "json" {
			return nil, fmt.Errorf("--stdout must be lp or json, got %q", s.stdout)
		}
		add(sinks.NewStdout(s.stdout, s.hostTag, q, logf))
	}
	if s.influx != "" {
		add(sinks.NewInflux(s.influx, s.influxToken, s.hostTag, q, logf))
	}
	if s.loki != "" {
		add(sinks.NewLoki(s.loki, s.lokiToken, s.lokiTen, s.hostTag, q, logf))
	}
	if s.otlp != "" {
		// The collector's context is deliberately not threaded into the
		// queued sinks: their delivery must outlive it, or Close would
		// cancel the final flush and lose the last batch on shutdown. Each
		// post bounds itself with its own timeout instead.
		add(sinks.NewOTLP(s.otlp, s.otlpToken, s.hostTag, q, logf)) //nolint:contextcheck // delivery must outlive ctx so Close can flush
	}
	if s.graph != "" {
		add(sinks.NewGraphite(s.graph, s.graphPx, s.hostTag, q, logf))
	}
	if s.elastic != "" {
		add(sinks.NewElasticsearch(s.elastic, s.elIndex, s.elasticAuth, s.hostTag, q, logf)) //nolint:contextcheck // as above: delivery outlives ctx
	}
	if s.telegraf != "" {
		add(sinks.NewTelegraf(s.telegraf, s.telegrafToken, s.hostTag, q, logf))
	}
	if len(out) == 0 {
		return nil, errors.New("no sink was constructed")
	}
	return out, nil
}

// closeAll closes every sink and reports what each one did. It runs even when
// the forward loop failed, because the counters are the record of what
// actually reached the far end.
func closeAll(out []sinks.Sink, logf func(string)) {
	for _, sk := range out {
		if err := sk.Close(); err != nil {
			logf("close " + sk.Name() + ": " + err.Error())
		}
	}
}
