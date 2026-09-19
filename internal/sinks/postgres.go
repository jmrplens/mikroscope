package sinks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres writes to a PostgreSQL that is running, rather than to a file for
// somebody to replay later.
//
// IT IS THE SQL SINK'S OTHER HALF AND NOT ITS REPLACEMENT. The file is still
// what you want when the database is somewhere the collector cannot reach,
// when the load is meant to happen later or under review, or when the reader
// is not PostgreSQL at all — the statements are ordinary SQL and another
// engine can take them. This one is for the operator whose PostgreSQL is right
// there, and it saves them a file and a timer.
//
// THE STATEMENTS ARE THE SQL SINK'S OWN, rendered by SQL.render and sent down
// a connection instead of written to a file. Not a second renderer that agrees
// with the first today: the dashboards this project generates for the postgres
// store query these tables, and two renderers would eventually make one of
// them a lie about the other. So there is one, and this sink is a transport
// for it.
type Postgres struct {
	DSN        string
	Host       string
	Hypertable bool
	Log        func(string)

	// render is an SQL sink that writes nowhere. It exists for its renderer.
	render *SQL

	// batchQueue carries mu, the bounded queue, the byte budget, the counters
	// and the backoff; see queue.go.
	batchQueue
	// pool is nil until the first batch connects and the schema is declared.
	// It is touched only by the flusher goroutine, and by Close after that
	// goroutine has finished, so it needs no lock of its own.
	pool *pgxpool.Pool

	stop chan struct{}
	done chan struct{}
	cur  bytes.Buffer
}

// NewPostgres returns a sink that has not connected yet.
//
// Connecting here rather than on the first write would make a collector fail
// to start because a database was down, and the samples of the time it spent
// not running are the thing that cannot be recovered later. So the pool is
// built lazily and a database that is not there yet costs retries, the same
// as one that goes away mid-run.
func NewPostgres(dsn, host string, hypertable bool, queueSeconds int, log func(string)) (*Postgres, error) {
	if dsn == "" {
		return nil, errors.New("--postgres needs a connection string")
	}
	// Parsed now so a DSN with a typo in it is a startup error rather than a
	// warning a minute in, when the first batch tries to connect.
	if _, err := pgxpool.ParseConfig(dsn); err != nil {
		return nil, fmt.Errorf("--postgres: %w", err)
	}
	if queueSeconds <= 0 {
		queueSeconds = 60
	}
	s := &Postgres{
		DSN: dsn, Host: host, Hypertable: hypertable, Log: log,
		render: newSQLRenderer(host, hypertable),
		stop:   make(chan struct{}), done: make(chan struct{}),
	}
	if s.Log == nil {
		s.Log = func(string) {}
	}
	s.maxQ = queueSeconds * 64 << 10
	go s.loop()
	return s, nil
}

// Name implements Sink. The DSN is not printed: it carries a password.
func (s *Postgres) Name() string { return "postgres" }

// Write implements Sink: it renders the event and appends it to the batch the
// flusher will send.
func (s *Postgres) Write(e Event) {
	stmts := s.render.statements(e)
	if len(stmts) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cur.Write(stmts)
}

// loop sends one batch a second.
func (s *Postgres) loop() {
	defer close(s.done)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			s.rotate()
			s.deliver("postgres", s.send, s.Log)
			return
		case <-t.C:
			s.rotate()
			s.deliver("postgres", s.send, s.Log)
		}
	}
}

// rotate moves the statements written since the last tick into the queue. The
// copy happens under the lock and push is called without it, for the reason
// the Influx sink's own rotate gives: push takes the lock itself.
func (s *Postgres) rotate() {
	s.mu.Lock()
	if s.cur.Len() == 0 {
		s.mu.Unlock()
		return
	}
	b := bytes.Clone(s.cur.Bytes())
	s.cur.Reset()
	s.mu.Unlock()
	s.push(b)
}

// send applies one batch inside a transaction, so a batch either lands whole
// or not at all: a retry after a partial apply would re-send statements the
// server already took, and while every INSERT carries ON CONFLICT DO NOTHING,
// leaning on that for correctness makes the primary key the only thing
// standing between a retry and a duplicate.
func (s *Postgres) send(batch []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := s.connect(ctx)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, string(batch)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// connect builds the pool and declares the schema on the first batch, and
// hands back the same pool afterwards.
//
// A failed attempt leaves pool nil and is NOT remembered as the answer: a
// database that comes up a minute after the collector did is the ordinary
// case, and the queue is already holding everything written since. The retry
// is the queue's own backoff, so this does not schedule one.
func (s *Postgres) connect(ctx context.Context) (*pgxpool.Pool, error) {
	if s.pool != nil {
		return s.pool, nil
	}
	pool, err := pgxpool.New(ctx, s.DSN)
	if err != nil {
		return nil, err
	}
	if err = s.declare(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	s.pool = pool
	return pool, nil
}

// declare applies the same header the file sink writes, and then checks the
// one assumption that header can only ASK for.
//
// The file sink's header opens with `SET standard_conforming_strings = on`,
// because without it a backslash in a kernel-log message is an escape and a
// message ending in one turns the rest of the file into string content. In a
// file that SET is advice: the operator may run the INSERTs without it. Down a
// connection it is a fact that can be read back, so it is — and a server that
// answers `off` is refused rather than written to, because every statement
// after a bad message would be parsed as something else.
func (s *Postgres) declare(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var setting string
	if err = conn.QueryRow(ctx, "SHOW standard_conforming_strings").Scan(&setting); err != nil {
		return fmt.Errorf("asking for standard_conforming_strings: %w", err)
	}
	if setting != "on" {
		return fmt.Errorf("this server has standard_conforming_strings = %s, and the statements "+
			"this sink sends quote by doubling the single quote only: with it off, a backslash "+
			"at the end of a kernel-log message escapes the closing quote and everything after "+
			"it is parsed as string content. Turn it on for this connection or use --sql", setting)
	}
	if _, err = conn.Exec(ctx, s.render.headerSQL()); err != nil {
		return fmt.Errorf("declaring the schema: %w", err)
	}
	return nil
}

// Stats implements Sink. The counters are the queue's, so Written is batches
// delivered rather than events rendered, exactly as on the other queued sinks.
func (s *Postgres) Stats() Stats { return s.snapshot() }

// Close stops the flusher, sends what is left and gives the connections back.
func (s *Postgres) Close() error {
	close(s.stop)
	<-s.done
	if s.pool != nil {
		s.pool.Close()
	}
	return nil
}
