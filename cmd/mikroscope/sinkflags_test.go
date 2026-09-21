package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/sinks"
)

// A sink that was asked for and is silently absent is worse than no data,
// because the absence is invisible. So `any` has to see every flag: one it
// does not know turns a run with that sink alone into "forward needs at least
// one sink", which is a confusing way to say "I do not know --telegraf".
func TestEverySinkFlagCountsAsASink(t *testing.T) {
	t.Parallel()
	for name, only := range map[string]sinkFlags{
		"--file":     {file: "out.jsonl"},
		"--prom":     {prom: ":9124"},
		"--influx":   {influx: "http://i:8181"},
		"--loki":     {loki: "http://l:3100"},
		"--otlp":     {otlp: "http://o:4318"},
		"--graphite": {graph: "g:2003"},
		"--elastic":  {elastic: "http://e:9200"},
		"--sql":      {sqlPath: "out.sql"},
		"--postgres": {postgres: "postgres://u@h/d"},
		"--telegraf": {telegraf: "http://t:8186"},
		"--stdout":   {stdout: "lp"},
	} {
		if !only.any() {
			t.Errorf("%s alone does not count as a sink", name)
		}
	}
	if (&sinkFlags{}).any() {
		t.Error("no flags at all counts as a sink")
	}
}

// The order the sinks are built in is the order their startup lines appear, so
// it is fixed on purpose: two runs of the same flags have to read the same.
func TestBuildMakesEverySinkAskedForInAFixedOrder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := &sinkFlags{
		file:     filepath.Join(dir, "out.jsonl"),
		sqlPath:  filepath.Join(dir, "out.sql"),
		postgres: "postgres://u@db:5432/mikroscope",
		influx:   "http://i:8181", influxDB: "mikroscope",
		loki: "http://l:3100", otlp: "http://o:4318",
		graph: "g:2003", elastic: "http://e:9200", telegraf: "http://t:8186",
		stdout: "lp", hostTag: "rb5009", queueSeconds: 60,
	}
	// The rate is what the Prometheus sink sizes its ring with; 20 rather than
	// the 10 every other caller passes, so the parameter is exercised as one.
	built, err := s.build(context.Background(), 20, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll(built, func(string) {})

	var names []string
	for _, sk := range built {
		names = append(names, strings.Fields(sk.Name())[0])
	}
	want := []string{"file", "sql", "postgres", "stdout", "influx", "loki", "otlp", "graphite", "elasticsearch", "telegraf"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("built %v, want %v", names, want)
	}
}

// Nothing asked for is an error rather than a collector that reads the router
// and throws the data away.
func TestBuildWithNoSinkIsAnError(t *testing.T) {
	t.Parallel()
	_, err := (&sinkFlags{hostTag: "r"}).build(context.Background(), 10, func(string) {})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "no sink was constructed") {
		t.Errorf("err = %q", err)
	}
}

// A flag whose value cannot be used fails the run BEFORE the first pull, which
// is the whole rule about sinks: one that was asked for and cannot be built
// stops the collector rather than being absent from it.
func TestBuildFailsOnAValueItCannotUse(t *testing.T) {
	t.Parallel()
	for name, s := range map[string]*sinkFlags{
		"--stdout":   {stdout: "yaml"},
		"--influx":   {influx: "not a url"},
		"--postgres": {postgres: "postgres://[::1"},
		"--sql":      {sqlPath: filepath.Join(t.TempDir(), "no-such-dir", "out.sql")},
	} {
		built, err := s.build(context.Background(), 10, func(string) {})
		if err == nil {
			closeAll(built, func(string) {})
			t.Errorf("%s with a value it cannot use built a collector anyway", name)
			continue
		}
		if built != nil {
			t.Errorf("%s returned %d sink(s) beside its error", name, len(built))
		}
	}
}

// The two SQL sinks are built together because --sql-hypertable applies to
// both: one of them emitting the TimescaleDB calls and the other not would be
// two schemas again.
func TestBothSQLSinksTakeTheHypertableFlag(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "out.sql")
	s := &sinkFlags{sqlPath: path, postgres: "postgres://u@db:5432/d", sqlHyper: true, hostTag: "r"}
	built, err := s.buildSQL(60, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(built) != 2 {
		closeAll(built, func(string) {})
		t.Fatalf("built %d sink(s), want the file and the connection", len(built))
	}
	// Closed before reading: the file sink writes through a bufio.Writer, so
	// the header it emitted at construction is still in memory until then.
	closeAll(built, func(string) {})
	// The connection's header is the same renderer, which internal/sinks
	// asserts against newSQLRenderer. Here: the flag reached the file.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "create_hypertable") {
		t.Error("--sql-hypertable did not reach the file sink")
	}
}

func TestBuildSQLMakesOnlyWhatWasAskedFor(t *testing.T) {
	t.Parallel()
	for name, s := range map[string]*sinkFlags{
		"neither":         {},
		"--sql only":      {sqlPath: filepath.Join(t.TempDir(), "a.sql")},
		"--postgres only": {postgres: "postgres://u@db:5432/d"},
	} {
		built, err := s.buildSQL(60, func(string) {})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		want := 1
		if name == "neither" {
			want = 0
		}
		if len(built) != want {
			t.Errorf("%s built %d sink(s), want %d", name, len(built), want)
		}
		closeAll(built, func(string) {})
	}
}

// closeAll runs even when the forward loop failed, because the counters are
// the record of what actually reached the far end — so one sink that will not
// close must not stop the others being closed or their lines being printed.
func TestCloseAllClosesEveryOneAndReportsTheFailures(t *testing.T) {
	t.Parallel()
	var said []string
	stubborn := &stubbornSink{name: "stubborn"}
	quiet := &stubbornSink{name: "quiet", err: nil}
	closeAll([]sinks.Sink{stubborn, quiet}, func(line string) { said = append(said, line) })
	if !stubborn.closed || !quiet.closed {
		t.Errorf("closed = %v / %v, want both", stubborn.closed, quiet.closed)
	}
	if len(said) != 1 || !strings.Contains(said[0], "stubborn") {
		t.Errorf("said %v, want one line naming the one that would not close", said)
	}
}

// stubbornSink is a Sink that only records being closed.
type stubbornSink struct {
	name   string
	err    error
	closed bool
}

func (s *stubbornSink) Name() string      { return s.name }
func (s *stubbornSink) Write(sinks.Event) {}
func (s *stubbornSink) Stats() sinks.Stats {
	return sinks.Stats{}
}

func (s *stubbornSink) Close() error {
	s.closed = true
	if s.name == "stubborn" {
		return errors.New("the far end is still reading")
	}
	return s.err
}
