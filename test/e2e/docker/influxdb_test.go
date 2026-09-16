//go:build dockere2e

package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// influxQuery runs one SQL statement against the e2e database and decodes the
// rows. InfluxDB 3 answers SQL over HTTP; there is no client library in this
// module and this suite is not the place to add one.
func influxQuery(ctx context.Context, addr, sql string, out any) error {
	body, err := json.Marshal(map[string]string{"db": sweepDatabase, "q": sql, "format": "json"})
	if err != nil {
		return err
	}
	return httpJSON(ctx, "POST", "http://"+addr+"/api/v3/query_sql", "application/json", body, out)
}

// influxCount counts this run's rows in a table. Every query in here is
// scoped to the run's host tag: the database outlives a `make e2e-docker-up`
// stack, and the previous run's rows are still in it.
func influxCount(ctx context.Context, addr, table string) (int, error) {
	var rows []struct {
		N int `json:"n"`
	}
	q := fmt.Sprintf("SELECT count(*) AS n FROM %s WHERE host = '%s'", table, sweepHostTag)
	if err := influxQuery(ctx, addr, q, &rows); err != nil {
		return 0, err
	}
	if len(rows) != 1 {
		return 0, fmt.Errorf("counting %s returned %d rows, not 1", table, len(rows))
	}
	return rows[0].N, nil
}

// influxHasTables checks that every table the sink is documented to write is
// in the database. A missing one is a write InfluxDB refused, which the
// collector's own summary cannot tell you: it counts batches accepted, and a
// batch is accepted or refused as a whole.
func influxHasTables(ctx context.Context, addr string) error {
	var tables []struct {
		Schema string `json:"table_schema"`
		Name   string `json:"table_name"`
	}
	if err := influxQuery(ctx, addr, "SHOW TABLES", &tables); err != nil {
		return err
	}
	have := map[string]bool{}
	for _, tbl := range tables {
		if tbl.Schema == "iox" {
			have[tbl.Name] = true
		}
	}
	for _, want := range []string{
		"mikroscope_cpu", "mikroscope_mem", "mikroscope_stat", "mikroscope_load",
		"mikroscope_softnet", "mikroscope_irq", "mikroscope_psi", "mikroscope_self",
		"mikroscope_thermal", "mikroscope_disk", "mikroscope_slab", "mikroscope_vm",
		"mikroscope_kmsg", "mikroscope_device", "mikroscope_sample",
	} {
		if !have[want] {
			return fmt.Errorf("%s is not in the database", want)
		}
	}
	return nil
}

// TestInfluxDB is the whole reason this package exists for the InfluxDB sink:
// a capture server accepts any line protocol, and InfluxDB 3 does not. It
// fixes each column as a tag or a field the first time it sees the table and
// rejects a later write that disagrees, so a sink that is inconsistent across
// sample shapes fails here and nowhere else.
func TestInfluxDB(t *testing.T) {
	s := Sweep(t)
	ctx := t.Context()
	// The tables the sink is documented to write. A missing one is a write
	// InfluxDB refused, which the collector's own summary cannot tell you:
	// it counts batches accepted, and a batch is accepted as a whole.
	if err := WaitUntil(ctx, "InfluxDB to hold the sweep's tables", 2*time.Minute, func(ctx context.Context) error {
		return influxHasTables(ctx, s.stack.InfluxDB)
	}); err != nil {
		t.Fatal(err)
	}

	// One row per sample in a per-sample table, one row per (sample, core) in
	// a per-core one. Counting is what catches a sink that dropped a batch
	// the collector believed it had written.
	for table, want := range map[string]int{
		"mikroscope_mem":     s.Rows(t, "mem"),
		"mikroscope_stat":    s.Rows(t, "ctxt"),
		"mikroscope_cpu":     s.Rows(t, "cpu"),
		"mikroscope_psi":     s.Rows(t, "psi"),
		"mikroscope_softnet": s.Rows(t, "softnet"),
	} {
		got, err := influxCount(ctx, s.stack.InfluxDB, table)
		if err != nil {
			t.Errorf("%s: %v", table, err)
			continue
		}
		if got != want {
			t.Errorf("%s holds %d rows, the run produced %d", table, got, want)
		}
	}

	// Values, not just counts: every context-switch counter the file sink
	// recorded has to be in InfluxDB, in the same multiset. A sink that wrote
	// the right number of points with the wrong numbers in them passes every
	// count above and fails here.
	var rows []struct {
		Ctxt int64  `json:"ctxt"`
		Host string `json:"host"`
	}
	if err := influxQuery(ctx, s.stack.InfluxDB, fmt.Sprintf(
		"SELECT ctxt, host FROM mikroscope_stat WHERE host = '%s' ORDER BY time", sweepHostTag,
	), &rows); err != nil {
		t.Fatal(err)
	}
	got := make([]int64, 0, len(rows))
	for _, r := range rows {
		if r.Host != sweepHostTag {
			t.Fatalf("a row carries host=%q, not the --host-tag %q", r.Host, sweepHostTag)
		}
		got = append(got, r.Ctxt)
	}
	sameInt64s(t, "mikroscope_stat.ctxt", got, s.Int64s(t, "ctxt"))

	// The kernel-log records travel as their own table, and their count is
	// the one the Loki test checks from the other side.
	events := s.Events(t)
	if n, err := influxCount(ctx, s.stack.InfluxDB, "mikroscope_kmsg"); err != nil {
		t.Error(err)
	} else if n != len(events) {
		t.Errorf("mikroscope_kmsg holds %d rows, the run carried %d kernel records", n, len(events))
	}
}
