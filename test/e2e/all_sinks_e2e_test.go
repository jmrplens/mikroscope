package e2e

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// everySinkPrefix is the leading word of every sink's Name(), which is what
// `forward` prints per sink at startup and again in the run summary. A sink
// that stops being wired up — a flag that no longer reaches its constructor,
// a constructor renamed in one place and not the other — still passes its
// own unit tests and every single-sink suite that does not mention it. This
// is the list that notices.
var everySinkPrefix = []string{
	"file", "prometheus", "sql", "stdout", "influx",
	"loki", "otlp", "graphite", "elasticsearch", "telegraf",
}

// TestEverySinkAtOnce is the normal arrangement, not an exotic one: the
// kernel tier at 10 Hz into InfluxDB for history, Prometheus for alerting,
// Loki for the kernel-log events and a file as a durable buffer is a
// reasonable set (cmd/mikroscope/sinkflags.go), and an operator adding a
// tenth destination should not have to wonder whether it disturbs the nine.
func TestEverySinkAtOnce(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")

	influx := newCapture(t, nil)
	loki := newCapture(t, nil)
	otlp := newCapture(t, nil)
	elastic := newCapture(t, bulkReply)
	telegraf := newCapture(t, nil)
	graphite := newLineListener(t)
	promAddr := freeAddr(t)
	dir := t.TempDir()
	filePath := filepath.Join(dir, "points.jsonl")
	sqlPath := filepath.Join(dir, "points.sql")

	p := startForward(t, a, 8*time.Second,
		"--file", filePath,
		"--prom", promAddr,
		"--sql", sqlPath,
		"--stdout", "lp",
		"--influx", influx.URL()+"/api/v3/write_lp?db=mikroscope",
		"--loki", loki.URL()+"/loki/api/v1/push",
		"--otlp", otlp.URL()+"/v1/metrics",
		"--graphite", graphite.Addr(),
		"--elastic", elastic.URL(),
		"--telegraf", telegraf.URL(),
	)

	// Every sink announced itself at startup, which is the builder's half of
	// the wiring.
	if !waitFor(60*time.Second, func() bool { return strings.Count(p.Stderr(), "sink: ") >= len(everySinkPrefix) }) {
		t.Fatalf("only %d sinks announced themselves:\n%s", strings.Count(p.Stderr(), "sink: "), p.Stderr())
	}
	for _, name := range everySinkPrefix {
		if !strings.Contains(p.Stderr(), "sink: "+name+" ") {
			t.Errorf("the %s sink was not constructed; the log says:\n%s", name, p.Stderr())
		}
	}

	var exposition string
	if !waitFor(60*time.Second, func() bool {
		b, up := scrape(t, promAddr)
		if !up || !strings.Contains(b, `mikroscope_cpu_ticks_total{`) {
			return false
		}
		exposition = b
		return true
	}) {
		t.Fatalf("the Prometheus sink served nothing while nine others ran:\n%s", p.Output())
	}
	p.Wait(t, 120*time.Second)

	// And every sink reported its counters at the end, which is the fan-out's
	// half. A sink that was constructed and never written to would show here.
	//
	// The summary is read off stdout alone and only after the "forwarded …"
	// line, because the stdout sink's own records are in the same stream and
	// carry field names like `dropped=` that would otherwise be mistaken for
	// a sink's counters.
	_, summary := splitRunSummary(p.Stdout())
	if summary == "" {
		t.Fatalf("forward printed no run summary:\n%s", tail(p.Output(), 800))
	}
	for _, name := range everySinkPrefix {
		if !strings.Contains(summary, "  "+name+" ") {
			t.Errorf("the %s sink is missing from the run summary:\n%s", name, summary)
		}
	}

	// Each receiver holds what it was sent. Any one of these being empty
	// while the others are full is the interference this test exists to find.
	assertReceived(t, "influx", len(parseLineProtocol(t, influx.Body())) >= 100, p)
	assertReceived(t, "telegraf", len(parseLineProtocol(t, telegraf.Body())) >= 50, p)
	assertReceived(t, "loki", strings.Contains(loki.Body(), `"streams"`), p)
	assertReceived(t, "otlp", strings.Contains(otlp.Body(), `"resourceMetrics"`), p)
	assertReceived(t, "graphite", len(graphite.Lines()) >= 200, p)
	if docs := func() int { _, d := parseBulk(t, elastic.Body()); return len(d) }(); docs == 0 {
		t.Errorf("the Elasticsearch receiver got no documents:\n%s", tail(p.Output(), 800))
	}
	if samples, _ := parseExposition(t, exposition); len(samples) < 50 {
		t.Errorf("the Prometheus sink served %d series while nine others ran", len(samples))
	}
	assertFileHasLines(t, filePath, 40)
	assertFileContains(t, sqlPath, "CREATE TABLE IF NOT EXISTS ")
	data, _ := splitRunSummary(p.Stdout())
	if n := len(parseLineProtocol(t, data)); n < 100 {
		t.Errorf("the stdout sink printed %d points while nine others ran", n)
	}

	// Nothing was dropped and nothing errored. The queues are bounded and
	// drop-oldest by design, so a drop under this load
	// would be a real finding about the defaults rather than a test
	// artifact — which is why it is asserted rather than tolerated.
	for _, line := range nonEmptyLines(summary) {
		if !strings.Contains(line, " written, ") {
			continue
		}
		if !strings.Contains(line, " 0 dropped, ") || !strings.Contains(line, " 0 errors") {
			t.Errorf("a sink lost data with ten sinks running: %s", strings.TrimSpace(line))
		}
	}
}

func assertReceived(t *testing.T, name string, ok bool, p *proc) {
	t.Helper()
	if !ok {
		t.Errorf("the %s receiver got nothing while nine other sinks ran:\n%s", name, tail(p.Output(), 800))
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
