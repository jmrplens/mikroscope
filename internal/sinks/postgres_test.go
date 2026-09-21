package sinks

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jmrplens/mikroscope/internal/sample"
	"github.com/jmrplens/mikroscope/internal/transport"
)

// A connection string pgx cannot read is a startup error, not a warning a
// minute in when the first batch tries to connect: a typo in a DSN should
// stop the run the way a sink that cannot be built always has.
func TestNewPostgresRefusesAConnectionStringItCannotRead(t *testing.T) {
	t.Parallel()
	for _, dsn := range []string{"", "postgres://[::1", "host=h port=notanumber"} {
		s, err := NewPostgres(dsn, "router", false, 60, nil)
		if err == nil {
			_ = s.Close()
			t.Errorf("NewPostgres(%q): want an error", dsn)
		}
	}
}

// The DSN carries a password, so it must not reach the sink's name, which is
// printed at start and in every summary line.
func TestThePostgresSinkNameCarriesNoConnectionString(t *testing.T) {
	t.Parallel()
	const dsn = "postgres://user:hunter2@db.example:5432/mikroscope"
	s, err := NewPostgres(dsn, "router", false, 60, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if strings.Contains(s.Name(), "hunter2") || strings.Contains(s.Name(), "db.example") {
		t.Errorf("Name() = %q, want nothing from the connection string in it", s.Name())
	}
}

// THE CLAIM THIS SINK IS BUILT ON, at unit level: what it queues is byte for
// byte what the SQL sink would have written. The end-to-end suite proves the
// same thing against a server; this proves it without one, so a change to the
// renderer cannot pass CI on a machine with no Docker.
func TestThePostgresSinkQueuesTheSQLSinksOwnStatements(t *testing.T) {
	t.Parallel()
	s, err := NewPostgres("postgres://u@nowhere.invalid:5432/d", "rb5009", false, 60, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	e := Event{At: 1_700_000_000_000_000_000, Gap: &transport.Gap{From: 4, To: 9}}
	s.Write(e)

	want := newSQLRenderer("rb5009", false).statements(e)
	if len(want) == 0 {
		t.Fatal("the renderer produced nothing for a gap; the test is testing nothing")
	}
	s.mu.Lock()
	got := s.cur.String()
	s.mu.Unlock()
	if got != string(want) {
		t.Errorf("queued %q, want the SQL sink's own %q", got, want)
	}
}

// An event the renderer has nothing to say about must not become an empty
// batch: a transaction with no statements in it is a round trip for nothing.
func TestAnEventWithNoStatementsIsNotQueued(t *testing.T) {
	t.Parallel()
	s, err := NewPostgres("postgres://u@nowhere.invalid:5432/d", "rb5009", false, 60, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	s.Write(Event{}) // no kernel sample, no API sample, no gap: nothing
	s.mu.Lock()
	queued := s.cur.Len()
	s.mu.Unlock()
	if queued != 0 {
		t.Errorf("an empty event queued %d bytes", queued)
	}
}

// The schema the connection declares is the file's own header, so the two
// cannot describe different tables.
func TestTheHeaderAppliedOverAConnectionIsTheFilesOwn(t *testing.T) {
	t.Parallel()
	header := newSQLRenderer("rb5009", false).headerSQL()
	if !strings.Contains(header, "SET standard_conforming_strings = on;") {
		t.Error("the header does not set standard_conforming_strings")
	}
	for _, table := range []string{"mikroscope_cpu", "mikroscope_mem", "mikroscope_api_iface"} {
		if !strings.Contains(header, "CREATE TABLE IF NOT EXISTS "+table+" (") {
			t.Errorf("the header does not declare %s", table)
		}
	}
	if strings.Contains(header, "create_hypertable") {
		t.Error("the header carries a hypertable call without --sql-hypertable")
	}
	withHyper := newSQLRenderer("rb5009", true).headerSQL()
	if !strings.Contains(withHyper, "create_hypertable('mikroscope_cpu', 'time'") {
		t.Error("--sql-hypertable did not reach the header the connection applies")
	}
}

// headerSQL must not disturb the renderer it borrows: the next event has to
// render into an empty buffer, or the header ends up prepended to a batch.
func TestHeaderSQLLeavesTheRendererEmpty(t *testing.T) {
	t.Parallel()
	r := newSQLRenderer("rb5009", false)
	_ = r.headerSQL()
	got := r.statements(Event{At: 1, Gap: &transport.Gap{From: 1, To: 2}})
	if strings.Contains(string(got), "CREATE TABLE") {
		t.Errorf("the event carried the header with it: %q", got)
	}
}

// A database that is not there costs retries and counts errors; it must not
// stall Write, lose the batch, or panic. This is the "collect anyway" promise
// at the sink's own level.
func TestADatabaseThatIsNotThereCountsErrorsAndKeepsCollecting(t *testing.T) {
	t.Parallel()
	var logged []string
	s, err := NewPostgres("postgres://u@127.0.0.1:1/d?connect_timeout=1", "rb5009", false, 60,
		func(line string) { logged = append(logged, line) })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for range 3 {
		s.Write(Event{At: time.Now().UnixNano(), Gap: &transport.Gap{From: 1, To: 2}})
	}
	// The flusher ticks once a second; two seconds is one tick plus slack.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s.Stats().Errors > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got := s.Stats(); got.Errors == 0 {
		t.Errorf("stats = %+v, want an error counted against the sink", got)
	}
	if got := s.Stats(); got.Written != 0 {
		t.Errorf("stats = %+v, want nothing counted as written to a database that is not there", got)
	}
}

// Close on a sink that never connected is the ordinary case for a short run
// or a config check, and must not panic on the nil pool.
func TestClosingASinkThatNeverConnected(t *testing.T) {
	t.Parallel()
	s, err := NewPostgres("postgres://u@nowhere.invalid:5432/d", "rb5009", false, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}

// A kernel sample is the shape the sink spends its life on; this is the one
// assertion that it renders through the whole path rather than only for the
// small events above.
func TestAKernelSampleRendersThroughThePostgresSink(t *testing.T) {
	t.Parallel()
	s, err := NewPostgres("postgres://u@nowhere.invalid:5432/d", "rb5009", false, 60, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	k := &sample.Sample{
		Seq: 7, WallNS: 1_700_000_000_000_000_000, DtNS: 100_000_000,
		CPU: []sample.CPUDelta{{User: 3, System: 2, Idle: 95}},
	}
	s.Write(Event{At: k.WallNS, Kernel: k})
	s.mu.Lock()
	got := s.cur.String()
	s.mu.Unlock()
	if !strings.Contains(got, "INSERT INTO mikroscope_cpu (time, host, cpu,") {
		t.Errorf("queued %q, want the cpu INSERT", got)
	}
	if !strings.Contains(got, "'rb5009'") {
		t.Errorf("queued %q, want the host tag in it", got)
	}
	if !strings.HasSuffix(strings.TrimSpace(got), "ON CONFLICT DO NOTHING;") {
		t.Errorf("queued %q, want every statement idempotent", got)
	}
}

// The setting the sink refuses on, and the message an operator reads when it
// does — which is the whole value of the check, since a collector that will
// not write has to say why in a line that survives a journal.
func TestTheSinkRefusesAServerThatWouldMisreadItsQuoting(t *testing.T) {
	t.Parallel()
	if err := conformingStrings("on"); err != nil {
		t.Errorf("conformingStrings(\"on\") = %v, want nil", err)
	}
	// PostgreSQL answers this setting in lower case, but a server or a pooler
	// that echoes it differently is not a reason to refuse a healthy one.
	if err := conformingStrings("ON"); err != nil {
		t.Errorf("conformingStrings(\"ON\") = %v, want nil", err)
	}
	for _, answer := range []string{"off", "", "unknown"} {
		err := conformingStrings(answer)
		if err == nil {
			t.Errorf("conformingStrings(%q) = nil, want a refusal", answer)
			continue
		}
		// The three things the line has to carry: what the server said, what
		// goes wrong, and the two ways out.
		for _, want := range []string{answer, "backslash", "--sql"} {
			if want != "" && !strings.Contains(err.Error(), want) {
				t.Errorf("conformingStrings(%q) = %q, want it to carry %q", answer, err, want)
			}
		}
	}
}

// The paths that need a pool but not a server. pgxpool.New connects lazily, so
// a pool built on an address nothing listens on is a real pool that fails on
// first use — which is exactly the shape of a database that went away
// mid-run, and it reaches the code a dead DSN alone never does: the
// already-connected branch, the transaction that cannot begin, and Close with
// something to close.
func TestTheSinkHandlesAPoolWhoseServerIsGone(t *testing.T) {
	t.Parallel()
	const dsn = "postgres://u@127.0.0.1:1/d?sslmode=disable&connect_timeout=1"
	s, err := NewPostgres(dsn, "rb5009", false, 60, nil)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	s.pool = pool

	// connect hands back the pool it already has rather than building another.
	got, err := s.connect(context.Background())
	if err != nil {
		t.Fatalf("connect with a pool already built: %v", err)
	}
	if got != pool {
		t.Error("connect built a second pool over the one it had")
	}
	// And a batch against it fails at the transaction rather than panicking.
	if err = s.send([]byte("SELECT 1;")); err == nil {
		t.Error("a batch against a server that is not there reported success")
	}
	// Close gives the pool back.
	if err = s.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}
