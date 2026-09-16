package sinks

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmrplens/mikroscope/internal/apitier"
	"github.com/jmrplens/mikroscope/internal/derive"
	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/sample"
	"github.com/jmrplens/mikroscope/internal/transport"
)

// SQL writes the timeline as PostgreSQL/TimescaleDB text — a DDL header
// followed by one INSERT per record — to a file or to stdout, so the
// operator pipes it into psql: `mikroscope forward --sql out.sql && psql -f
// out.sql`, or `--sql - | psql`.
//
// It is driverless on purpose. Speaking the PostgreSQL wire protocol needs a
// driver, a third-party dependency this module does not take (the agent's
// link set is fixed at procfs, sample, agent and the standard library), so
// the SQL text is the interface and psql owns the
// connection. The cost is that delivery is the operator's problem: this sink
// cannot know whether a row was accepted, so it counts what it wrote, not
// what the server stored.
//
// Tables, declared by the header and created with IF NOT EXISTS so a second
// file replays onto the same schema: mikroscope_cpu{cpu},
// mikroscope_softnet{cpu}, mikroscope_irq{irq,name}, mikroscope_mem,
// mikroscope_self, mikroscope_psi, mikroscope_thermal{zone},
// mikroscope_slab{cache}, mikroscope_disk{device}, mikroscope_flash{device},
// mikroscope_event, mikroscope_api_system, mikroscope_api_core{cpu},
// mikroscope_api_health{name}, mikroscope_api_iface{interface},
// mikroscope_api_conntrack, mikroscope_api_error and mikroscope_gap. Every
// table is keyed on (time, host, …its identifying tags) and every INSERT ends
// in ON CONFLICT DO NOTHING, so applying the same file twice is a no-op
// rather than a duplicate-key abort. That is the opposite of the sibling
// ghchronicle sink's ON CONFLICT DO UPDATE, and for the opposite reason: a
// mikroscope row is one immutable instant of a counter delta, never a running
// total that a later sweep revises.
//
// Cadence is synchronous — Write renders into memory and hands the bytes to a
// 64 KiB bufio.Writer, with no queue, no goroutine and no backoff, as in
// file.go, because a file cannot stall the way a remote can. Nothing is
// dropped except on a write error (a full filesystem), which counts one
// Errors and one Dropped together because that event is then gone.
//
// A pipe is the exception, and it is not handled: with Path "-" feeding
// `| psql`, a psql that falls behind fills the pipe buffer and the next
// os.Stdout write blocks, and because this shape has no queue that blocks the
// collector's pull loop instead of dropping. Each INSERT is its own
// transaction, so psql commits — and fsyncs — once per statement, which is
// the realistic way for it to fall behind a 10 Hz agent. Not measured;
// prefer a file and apply it afterwards, or wrap the file in BEGIN/COMMIT by
// hand, until this is either measured or given a queue.
//
// Stats counters are in EVENT units, as in file.go: Written is one per Event
// accepted (kernel samples, API samples and gaps alike), not one per INSERT
// and not one per row the database ingested.
//
// Deltas and levels share a row in four tables and the column names do not
// say which is which, so: mikroscope_mem and mikroscope_load are levels and
// mikroscope_stat is deltas; in mikroscope_self, cpu_us
// is a delta and rss/cgroup_mem are levels; in mikroscope_disk, inflight is a
// level and the rest are deltas; in mikroscope_flash, bad_blocks and
// free_chunks are levels and the rest are deltas. Levels are never summed.
// dt_ns rides on mikroscope_cpu only (as in influx.go), so a rate over any
// other delta table joins mikroscope_cpu on (time, host) for the real
// interval rather than assuming the nominal period.
//
// Absent sources emit no row at all: PSI is nil on the reference RB5009 (its
// 5.6.3 kernel has no /proc/pressure, RouterOS 7.24.2, arm64, 2026-09-11),
// and Slab and Events need privileged=yes, which drops the user namespace and
// makes /proc/slabinfo and /dev/kmsg readable (same device, 2026-09-12), so a
// NULL-free zero row there would be indistinguishable from a real zero.
//
// Size, measured on 2026-09-12 from this package's own test fixture (2 cores,
// one softnet queue, one IRQ, no privileged sources) and not on the device: a
// kernel event renders to 1375 B of SQL and an API event to 1138 B, so 10 Hz
// plus the 1 Hz API tier costs ≈ 14 KiB/s of file, after a 5.6 KiB header.
// The same two events in Influx line protocol are 716 B and 608 B, so naming
// every column on every row costs ≈ 1.9× — though part of that is content,
// not overhead: this sink also carries steal_ticks, sunreclaim_kb,
// pgmajfault and cgroup_mem, which influx.go omits. With the privileged
// sources present the same fixture grows to 2749 B. Not measured on the
// RB5009 itself and not above 10 Hz. Never run against a live TimescaleDB in
// this repository either: the create_hypertable calls follow its documented
// 2.x signature, they are not verified.
type SQL struct {
	Path       string // output file, or "-" for stdout
	Host       string // host column on every row
	Hypertable bool   // also emit TimescaleDB create_hypertable() calls
	Log        func(string)

	mu      sync.Mutex
	f       *os.File // nil when writing to stdout
	w       *bufio.Writer
	buf     bytes.Buffer // the rows of the event being rendered
	hostLit string       // Host as a quoted SQL literal, escaped once
	ct      uint64       // last conntrack count, held so the series does not blink
	haveCT  bool
	stats   Stats
	lastLog time.Time
}

// sqlTable is one table's DDL. Keeping the column definitions beside the name
// is what lets the header be generated instead of pasted, but the INSERT
// column lists below are still written by hand — TestSQLSinkHeaderDeclares…
// is the guard that the two never drift apart.
type sqlTable struct {
	name string
	cols string // column definitions after the implicit `time TIMESTAMPTZ NOT NULL`
	key  string // PRIMARY KEY list; `time` leads it because create_hypertable
	// requires the partitioning column to be part of every unique index.
}

// sqlSchema is every table the sink can write, in header order.
//
// Column names are chosen so that none of them needs quoting: the CPU tick
// columns are `user_ticks` and friends rather than `user`, which is reserved
// in PostgreSQL, and the file is nicer to read in psql for it. The counters
// are BIGINT rather than NUMERIC because each one is a per-tick delta, not a
// since-boot total — PostgreSQL has no unsigned 64-bit type, and no delta a
// router produces in 100 ms comes near 2^63.
var sqlSchema = []sqlTable{
	{"mikroscope_cpu", "host TEXT NOT NULL, cpu INTEGER NOT NULL, user_ticks BIGINT, nice_ticks BIGINT, system_ticks BIGINT, idle_ticks BIGINT, iowait_ticks BIGINT, irq_ticks BIGINT, softirq_ticks BIGINT, steal_ticks BIGINT, busy_ratio DOUBLE PRECISION, dt_ns BIGINT", "time, host, cpu"},
	{"mikroscope_softnet", "host TEXT NOT NULL, cpu INTEGER NOT NULL, processed BIGINT, dropped BIGINT, time_squeeze BIGINT", "time, host, cpu"},
	{"mikroscope_irq", "host TEXT NOT NULL, irq TEXT NOT NULL, name TEXT, count BIGINT", "time, host, irq"},
	{"mikroscope_mem", "host TEXT NOT NULL, free_kb BIGINT, available_kb BIGINT, cached_kb BIGINT, slab_kb BIGINT, sunreclaim_kb BIGINT", "time, host"},
	{"mikroscope_load", "host TEXT NOT NULL, load1 DOUBLE PRECISION, load5 DOUBLE PRECISION, load15 DOUBLE PRECISION, running BIGINT, threads BIGINT, procs_blocked BIGINT", "time, host"},
	// /proc/stat's scalars as deltas, with the two page-fault counters that
	// used to ride on mikroscope_mem beside levels they are not.
	{"mikroscope_stat", "host TEXT NOT NULL, ctxt BIGINT, intr BIGINT, forks BIGINT, irq_total BIGINT, irq_err BIGINT, pgfault BIGINT, pgmajfault BIGINT", "time, host"},
	// throttled, throttled_us and oom_kill are the container's own cgroup
	// events, deltas; NULL when the container has no cgroup2 to ask.
	{"mikroscope_self", "host TEXT NOT NULL, cpu_us BIGINT, rss BIGINT, cgroup_mem BIGINT, throttled BIGINT, throttled_us BIGINT, oom_kill BIGINT, seq BIGINT", "time, host"},
	// Long form, one row per (zone, order): free blocks of 2^order pages, a
	// level. `order` is reserved, hence block_order.
	{"mikroscope_buddy", "host TEXT NOT NULL, node INTEGER NOT NULL, zone TEXT NOT NULL, block_order INTEGER NOT NULL, free_blocks BIGINT", "time, host, node, zone, block_order"},
	// Flash ECC state: the counters are the kernel's cumulative-since-boot
	// figures, the rest levels; thresholds NULL where the kernel publishes none.
	{"mikroscope_mtd", "host TEXT NOT NULL, device TEXT NOT NULL, partition TEXT, corrected_bits BIGINT, ecc_failures BIGINT, bad_blocks BIGINT, bbt_blocks BIGINT, bitflip_threshold BIGINT, ecc_strength BIGINT", "time, host, device"},
	{"mikroscope_psi", "host TEXT NOT NULL, cpu_some_us BIGINT, mem_some_us BIGINT, mem_full_us BIGINT, io_some_us BIGINT, io_full_us BIGINT", "time, host"},
	{"mikroscope_thermal", "host TEXT NOT NULL, zone TEXT NOT NULL, celsius DOUBLE PRECISION, critical_celsius DOUBLE PRECISION", "time, host, zone"},
	{"mikroscope_slab", "host TEXT NOT NULL, cache TEXT NOT NULL, active_objs BIGINT, limit_objs BIGINT", "time, host, cache"},
	{"mikroscope_disk", "host TEXT NOT NULL, device TEXT NOT NULL, reads BIGINT, read_sectors BIGINT, writes BIGINT, write_sectors BIGINT, io_s DOUBLE PRECISION, inflight BIGINT", "time, host, device"},
	{"mikroscope_flash", "host TEXT NOT NULL, device TEXT NOT NULL, page_writes BIGINT, page_reads BIGINT, erasures BIGINT, gc_copies BIGINT, gcs BIGINT, bad_blocks BIGINT, free_chunks BIGINT", "time, host, device"},
	// port is the RouterOS name the record names (the kernel name on an
	// unknown board), kind what happened to it (procfs.KmsgKind); both NULL
	// for a record that names no port.
	{"mikroscope_event", "host TEXT NOT NULL, level SMALLINT, facility SMALLINT, kernel_seq BIGINT NOT NULL, time_usec BIGINT, message TEXT, port TEXT, kind TEXT", "time, host, kernel_seq"},
	{"mikroscope_api_system", "host TEXT NOT NULL, cpu_load BIGINT, free_memory BIGINT, total_memory BIGINT, free_hdd BIGINT, uptime_s BIGINT, version TEXT", "time, host"},
	{"mikroscope_api_core", "host TEXT NOT NULL, cpu INTEGER NOT NULL, load BIGINT, irq BIGINT, disk BIGINT", "time, host, cpu"},
	{"mikroscope_api_health", "host TEXT NOT NULL, name TEXT NOT NULL, value DOUBLE PRECISION", "time, host, name"},
	{"mikroscope_api_iface", "host TEXT NOT NULL, interface TEXT NOT NULL, label TEXT, rx_bps BIGINT, tx_bps BIGINT, rx_pps BIGINT, tx_pps BIGINT, rx_drops BIGINT, tx_drops BIGINT, tx_queue_drops BIGINT, rx_errors BIGINT, tx_errors BIGINT", "time, host, interface"},
	{"mikroscope_api_conntrack", "host TEXT NOT NULL, entries BIGINT", "time, host"},
	// What each interface is, one row per interface per inventory read (at
	// start and every --labels-every): join on interface to name, type and
	// role any interface series.
	{"mikroscope_api_ifinfo", "host TEXT NOT NULL, interface TEXT NOT NULL, default_name TEXT, type TEXT, role TEXT, bridge TEXT, label TEXT, mtu BIGINT", "time, host, interface"},
	// Long form on purpose: the counter set differs per port and per board,
	// and a wide table would need a column per counter the router might ever
	// return. counter holds RouterOS's own name.
	{"mikroscope_api_ifcounter", "host TEXT NOT NULL, interface TEXT NOT NULL, counter TEXT NOT NULL, value BIGINT", "time, host, interface, counter"},
	{"mikroscope_api_error", "host TEXT NOT NULL, message TEXT NOT NULL", "time, host, message"},
	{"mikroscope_gap", "host TEXT NOT NULL, seq_from BIGINT NOT NULL, seq_to BIGINT NOT NULL", "time, host, seq_from, seq_to"},
	// A capture marker: the agent fired a trigger on sample seq. The window
	// is on the agent, not here.
	{"mikroscope_trigger", "host TEXT NOT NULL, id BIGINT NOT NULL, cause TEXT, field TEXT, value DOUBLE PRECISION, threshold DOUBLE PRECISION, seq BIGINT", "time, host, id"},
	// The derive stage's output, beside its inputs: the per-packet columns
	// are NULL where they could not be computed (no PMU, no packets, a reset).
	{"mikroscope_derived", "host TEXT NOT NULL, seq BIGINT, mem_pressure SMALLINT, burst BOOLEAN, suspect BOOLEAN, cycles_per_packet DOUBLE PRECISION, instructions_per_packet DOUBLE PRECISION, cache_misses_per_packet DOUBLE PRECISION, packets_per_irq DOUBLE PRECISION", "time, host"},
	{"mikroscope_derived_iface", "host TEXT NOT NULL, interface TEXT NOT NULL, rx_bytes BIGINT, fp_rx_bytes BIGINT, tx_bytes BIGINT, fp_tx_bytes BIGINT, fp_rx_share DOUBLE PRECISION, fp_tx_share DOUBLE PRECISION", "time, host, interface"},
	{"mikroscope_detection", "host TEXT NOT NULL, rule TEXT NOT NULL, key TEXT, seq BIGINT, value DOUBLE PRECISION, threshold DOUBLE PRECISION, message TEXT", "time, host, rule, key"},
	// The device-info stream: board facts at the collector's clock, one row
	// per capability hash seen, with the zones, cores and cadences beside it.
	{"mikroscope_device", "host TEXT NOT NULL, board TEXT, kernel TEXT, cores INTEGER, privileged BOOLEAN, cgroup BOOLEAN, sources TEXT, conntrack_max BIGINT, cgroup_mem_max BIGINT, ports_from TEXT, hash TEXT", "time, host"},
	{"mikroscope_device_thermal", "host TEXT NOT NULL, zone TEXT NOT NULL, critical_celsius DOUBLE PRECISION, polling_ms INTEGER", "time, host, zone"},
	{"mikroscope_device_cpufreq", "host TEXT NOT NULL, cpu INTEGER NOT NULL, cluster INTEGER, min_khz BIGINT, max_khz BIGINT, governor TEXT, steps TEXT", "time, host, cpu"},
	{"mikroscope_device_cadence", "host TEXT NOT NULL, source TEXT NOT NULL, reason TEXT, hz DOUBLE PRECISION", "time, host, source"},
}

// NewSQL opens (truncates) path, or writes to stdout when path is "-", and
// emits the DDL header so the file is applyable on its own.
func NewSQL(path, host string, hypertable bool, log func(string)) (*SQL, error) {
	s := &SQL{Path: path, Host: host, Hypertable: hypertable, Log: log, hostLit: sqlQuote(host)}
	if s.Log == nil {
		s.Log = func(string) {}
	}
	var out io.Writer = os.Stdout
	if path != "-" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 -- the operator's own path
		if err != nil {
			return nil, err
		}
		s.f, out = f, f
	}
	s.w = bufio.NewWriterSize(out, 64<<10)
	s.header()
	if _, err := s.w.Write(s.buf.Bytes()); err != nil {
		if s.f != nil {
			_ = s.f.Close()
		}
		return nil, err
	}
	s.buf.Reset()
	return s, nil
}

// Name implements Sink.
func (s *SQL) Name() string { return "sql " + s.Path }

// Write implements Sink: it renders the event's rows into memory, then hands
// them to the buffered writer in one call, so one error site covers the whole
// event. It does not make the event atomic on disk — bufio flushes when its
// buffer fills, so an event that straddles a flush that fails is truncated
// mid-statement in the file. The trailing statement is then a syntax error
// psql reports rather than a row that silently lies.
func (s *SQL) Write(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf.Reset()
	switch {
	case e.Kernel != nil:
		ts := sqlStamp(e.Kernel.WallNS)
		s.kernelCore(ts, e.Kernel)
		s.kernelExtra(ts, e.Kernel)
		s.derivedRow(ts, e.Derived)
	case e.API != nil:
		s.apiRows(e.API)
		for _, sh := range e.Shares {
			fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_derived_iface (time, host, interface, rx_bytes, fp_rx_bytes, tx_bytes, fp_tx_bytes, fp_rx_share, fp_tx_share) VALUES (%s, %s, %s, %d, %d, %d, %d, %s, %s)%s",
				sqlStamp(e.API.WallNS), s.hostLit, sqlQuote(sh.Interface), sh.RxBytes, sh.FpRxBytes, sh.TxBytes, sh.FpTxBytes, sqlFloatPtr(sh.FpRxShare), sqlFloatPtr(sh.FpTxShare), sqlEnd)
		}
	case e.Gap != nil:
		// A gap carries no timestamp of its own; the collector noticed it now.
		s.gapRow(sqlStamp(time.Now().UnixNano()), e.Gap)
	case e.Device != nil:
		s.deviceRows(sqlStamp(time.Now().UnixNano()), e.Device)
	case e.Detection != nil:
		d := e.Detection
		fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_detection (time, host, rule, key, seq, value, threshold, message) VALUES (%s, %s, %s, %s, %d, %s, %s, %s)%s",
			sqlStamp(d.WallNS), s.hostLit, sqlQuote(d.Rule), sqlQuote(d.Key), d.Seq, sqlFloat(d.Value, -1), sqlFloat(d.Threshold, -1), sqlQuote(d.Message), sqlEnd)
	case e.Trigger != nil:
		t := e.Trigger
		fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_trigger (time, host, id, cause, field, value, threshold, seq) VALUES (%s, %s, %d, %s, %s, %s, %s, %d)%s",
			sqlStamp(t.WallNS), s.hostLit, t.ID, sqlQuote(t.Cause), sqlLabel(t.Field), sqlFloat(t.Value, -1), sqlFloat(t.Threshold, -1), t.Seq, sqlEnd)
	default:
		return
	}
	if _, err := s.w.Write(s.buf.Bytes()); err != nil {
		s.stats.Errors++
		s.stats.Dropped++
		if time.Since(s.lastLog) > time.Minute {
			s.lastLog = time.Now()
			s.Log("sql: " + err.Error() + " (event dropped)")
		}
		return
	}
	s.stats.Written++
}

// header renders the DDL. CREATE TABLE IF NOT EXISTS and the hypertable's
// if_not_exists make it idempotent, so the operator applies it once and every
// later file re-states it harmlessly.
func (s *SQL) header() {
	fmt.Fprint(&s.buf, "-- mikroscope sql sink: apply this header once, then the INSERTs below.\n")
	fmt.Fprint(&s.buf, "-- Timestamps are the agent's wall clock, skew corrected by the collector.\n")
	fmt.Fprint(&s.buf, "-- TIMESTAMPTZ resolves to 1 us, so two samples closer than that would\n-- collide on the primary key; at the 10 Hz design rate they are 100 ms apart.\n")
	// sqlQuote doubles the single quote and nothing else, which is the whole
	// of the escaping only while standard_conforming_strings is on. It has
	// been the default since PostgreSQL 9.1, but postgresql.conf can turn it
	// off, and then a backslash in a kmsg message is an escape: a message
	// ending in one turns `'…\'` into an unterminated literal and every
	// statement after it in the file is parsed as string content. One SET
	// costs nothing and removes the whole class.
	fmt.Fprint(&s.buf, "SET standard_conforming_strings = on;\n")
	for _, t := range sqlSchema {
		fmt.Fprintf(&s.buf, "CREATE TABLE IF NOT EXISTS %s (time TIMESTAMPTZ NOT NULL, %s, PRIMARY KEY (%s));\n", t.name, t.cols, t.key)
	}
	if !s.Hypertable {
		return
	}
	fmt.Fprint(&s.buf, "-- TimescaleDB only; harmless to delete on plain PostgreSQL.\n")
	for _, t := range sqlSchema {
		fmt.Fprintf(&s.buf, "SELECT create_hypertable('%s', 'time', if_not_exists => TRUE);\n", t.name)
	}
}

// kernelCore renders the sources every container can read.
func (s *SQL) kernelCore(ts string, k *sample.Sample) {
	for i, c := range k.CPU {
		fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_cpu (time, host, cpu, user_ticks, nice_ticks, system_ticks, idle_ticks, iowait_ticks, irq_ticks, softirq_ticks, steal_ticks, busy_ratio, dt_ns) VALUES (%s, %s, %d, %d, %d, %d, %d, %d, %d, %d, %d, %s, %d)%s",
			ts, s.hostLit, i, c.User, c.Nice, c.System, c.Idle, c.IOWait, c.IRQ, c.SoftIRQ, c.Steal, sqlFloat(c.BusyRatio(k.DtNS), 4), k.DtNS, sqlEnd)
	}
	for i, n := range k.Softnet {
		fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_softnet (time, host, cpu, processed, dropped, time_squeeze) VALUES (%s, %s, %d, %d, %d, %d)%s",
			ts, s.hostLit, i, n.Processed, n.Dropped, n.TimeSqueeze, sqlEnd)
	}
	for _, q := range k.IRQ {
		var total uint64
		for _, v := range q.PerCPU {
			total += v
		}
		fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_irq (time, host, irq, name, count) VALUES (%s, %s, %s, %s, %d)%s",
			ts, s.hostLit, sqlQuote(q.ID), sqlQuote(q.Name), total, sqlEnd)
	}
	fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_mem (time, host, free_kb, available_kb, cached_kb, slab_kb, sunreclaim_kb) VALUES (%s, %s, %d, %d, %d, %d, %d)%s",
		ts, s.hostLit, k.Mem.MemFree, k.Mem.MemAvailable, k.Mem.Cached, k.Mem.Slab, k.Mem.SUnreclaim, sqlEnd)
	fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_load (time, host, load1, load5, load15, running, threads, procs_blocked) VALUES (%s, %s, %s, %s, %s, %d, %d, %d)%s",
		ts, s.hostLit, sqlFloat(k.Load.Load1, 2), sqlFloat(k.Load.Load5, 2), sqlFloat(k.Load.Load15, 2), k.Load.Running, k.Load.Total, k.ProcsBlocked, sqlEnd)
	fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_stat (time, host, ctxt, intr, forks, irq_total, irq_err, pgfault, pgmajfault) VALUES (%s, %s, %d, %d, %d, %d, %d, %d, %d)%s",
		ts, s.hostLit, k.Ctxt, k.Intr, k.Forks, k.IRQTotal, k.IRQErr, k.VM.PgFault, k.VM.PgMajFault, sqlEnd)
	throttled, throttledUs, oomKill := "NULL", "NULL", "NULL"
	if k.Self.HasCgroup {
		throttled, throttledUs, oomKill = strconv.FormatUint(k.Self.Throttled, 10), strconv.FormatUint(k.Self.ThrottledUsec, 10), strconv.FormatUint(k.Self.OOMKill, 10)
	}
	fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_self (time, host, cpu_us, rss, cgroup_mem, throttled, throttled_us, oom_kill, seq) VALUES (%s, %s, %d, %d, %d, %s, %s, %s, %d)%s",
		ts, s.hostLit, k.Self.CPUUsec, k.Self.RSSBytes, k.Self.CgroupMem, throttled, throttledUs, oomKill, k.Seq, sqlEnd)
	for _, z := range k.Buddy {
		for o, n := range z.Free {
			fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_buddy (time, host, node, zone, block_order, free_blocks) VALUES (%s, %s, %d, %s, %d, %d)%s",
				ts, s.hostLit, z.Node, sqlQuote(z.Zone), o, n, sqlEnd)
		}
	}
	if k.PSI != nil {
		fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_psi (time, host, cpu_some_us, mem_some_us, mem_full_us, io_some_us, io_full_us) VALUES (%s, %s, %d, %d, %d, %d, %d)%s",
			ts, s.hostLit, k.PSI.CPUSome, k.PSI.MemSome, k.PSI.MemFull, k.PSI.IOSome, k.PSI.IOFull, sqlEnd)
	}
}

// kernelExtra renders the sources that depend on the container's privileges
// or on the board: each is skipped entirely when the kernel said nothing, so
// a query can tell "unreadable" from a genuine zero.
func (s *SQL) kernelExtra(ts string, k *sample.Sample) {
	for _, h := range k.MTD {
		fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_mtd (time, host, device, partition, corrected_bits, ecc_failures, bad_blocks, bbt_blocks, bitflip_threshold, ecc_strength) VALUES (%s, %s, %s, %s, %d, %d, %d, %d, %s, %s)%s",
			ts, s.hostLit, sqlQuote(h.Dev), sqlLabel(h.Name), h.CorrectedBits, h.ECCFailures, h.BadBlocks, h.BBTBlocks, sqlNullIfZero(h.BitflipThreshold), sqlNullIfZero(h.ECCStrength), sqlEnd)
	}
	for _, t := range k.Thermal {
		crit := "NULL"
		if v := k.ThermalCritical[t.Type]; v > 0 {
			crit = sqlFloat(float64(v)/1000, 3)
		}
		fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_thermal (time, host, zone, celsius, critical_celsius) VALUES (%s, %s, %s, %s, %s)%s",
			ts, s.hostLit, sqlQuote(t.Type), sqlFloat(float64(t.MilliC)/1000, 3), crit, sqlEnd)
	}
	for _, name := range sqlSortedKeys(k.Slab) {
		// limit_objs is NULL for the caches the kernel publishes no ceiling
		// for, which is every one but nf_conntrack today. NULL and not 0: a
		// cache with no published limit has not got a limit of nothing.
		lim := "NULL"
		if v := k.SlabLimit[name]; v > 0 {
			lim = strconv.FormatUint(v, 10)
		}
		fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_slab (time, host, cache, active_objs, limit_objs) VALUES (%s, %s, %s, %d, %s)%s",
			ts, s.hostLit, sqlQuote(name), k.Slab[name], lim, sqlEnd)
	}
	for _, d := range k.Disk {
		fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_disk (time, host, device, reads, read_sectors, writes, write_sectors, io_s, inflight) VALUES (%s, %s, %s, %d, %d, %d, %d, %s, %d)%s",
			ts, s.hostLit, sqlQuote(d.Name), d.ReadsCompleted, d.ReadSectors, d.WritesCompleted, d.WriteSectors, sqlFloat(float64(d.IOTicks)/1000, 3), d.IOInProgress, sqlEnd)
	}
	for _, fl := range k.Flash {
		fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_flash (time, host, device, page_writes, page_reads, erasures, gc_copies, gcs, bad_blocks, free_chunks) VALUES (%s, %s, %s, %d, %d, %d, %d, %d, %d, %d)%s",
			ts, s.hostLit, sqlQuote(fl.Device), fl.PageWrites, fl.PageReads, fl.Erasures, fl.GCCopies, fl.GCs, fl.BadBlocks, fl.FreeChunks, sqlEnd)
	}
	// Kernel-log markers, not metrics: the message text stays in a TEXT
	// column (never an identifying column — it is unbounded free text) and
	// TimeUsec is kept as the kernel wrote it, us since boot and monotonic,
	// which is not the same clock as `time`.
	for _, ev := range k.Events {
		port := ev.ROSIface
		if port == "" {
			port = ev.Iface
		}
		kind := ev.Kind
		if port != "" && kind == "" {
			kind = procfs.KmsgKind(ev.Message)
		}
		fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_event (time, host, level, facility, kernel_seq, time_usec, message, port, kind) VALUES (%s, %s, %d, %d, %d, %d, %s, %s, %s)%s",
			ts, s.hostLit, ev.Level, ev.Facility, ev.Seq, ev.TimeUsec, sqlQuote(ev.Message), sqlLabel(port), sqlLabel(kind), sqlEnd)
	}
}

// apiRows renders one API-tier read. Every part is optional, because a failed
// command leaves its own part nil and the rest of the sample still stands.
func (s *SQL) apiRows(a *apitier.Sample) {
	ts := sqlStamp(a.WallNS)
	if a.System != nil {
		fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_api_system (time, host, cpu_load, free_memory, total_memory, free_hdd, uptime_s, version) VALUES (%s, %s, %d, %d, %d, %d, %d, %s)%s",
			ts, s.hostLit, a.System.CPULoad, a.System.FreeMemory, a.System.TotalMemory, a.System.FreeHDD, a.System.UptimeS, sqlQuote(a.System.Version), sqlEnd)
	}
	for i, c := range a.Cores {
		fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_api_core (time, host, cpu, load, irq, disk) VALUES (%s, %s, %d, %d, %d, %d)%s",
			ts, s.hostLit, i, c.Load, c.IRQ, c.Disk, sqlEnd)
	}
	for _, name := range sqlSortedKeys(a.Health) {
		fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_api_health (time, host, name, value) VALUES (%s, %s, %s, %s)%s",
			ts, s.hostLit, sqlQuote(name), sqlFloat(a.Health[name], -1), sqlEnd)
	}
	for _, c := range a.IfaceCounters {
		for _, k := range sqlSortedKeys(c.Counters) {
			fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_api_ifcounter (time, host, interface, counter, value) VALUES (%s, %s, %s, %s, %d)%s",
				ts, s.hostLit, sqlQuote(c.Name), sqlQuote(k), c.Counters[k], sqlEnd)
		}
	}
	for _, i := range a.Inventory {
		mtu := "NULL"
		if i.MTU > 0 {
			mtu = strconv.FormatUint(i.MTU, 10)
		}
		fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_api_ifinfo (time, host, interface, default_name, type, role, bridge, label, mtu) VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s)%s",
			ts, s.hostLit, sqlQuote(i.Name), sqlLabel(i.DefaultName), sqlLabel(i.Type), sqlLabel(i.Role), sqlLabel(i.Bridge), sqlLabel(i.Comment), mtu, sqlEnd)
	}
	for _, f := range a.Ifaces {
		// The five loss columns are NULL for a key the router did not return:
		// NULL and not 0, because RouterOS 7.24.2 returns no error keys at all
		// and a 0 there claimed a measurement that was never made.
		loss := make([]string, 0, len(apitier.LossKeys))
		for _, k := range apitier.LossKeys {
			if v, ok := f.Losses[k]; ok {
				loss = append(loss, strconv.FormatUint(v, 10))
			} else {
				loss = append(loss, "NULL")
			}
		}
		fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_api_iface (time, host, interface, label, rx_bps, tx_bps, rx_pps, tx_pps, rx_drops, tx_drops, tx_queue_drops, rx_errors, tx_errors) VALUES (%s, %s, %s, %s, %d, %d, %d, %d, %s)%s",
			ts, s.hostLit, sqlQuote(f.Name), sqlLabel(f.Comment), f.RxBps, f.TxBps, f.RxPps, f.TxPps, strings.Join(loss, ", "), sqlEnd)
	}
	// Conntrack is a table scan, so the reader asks for it only every N
	// seconds. The last value is held (by value, never by keeping the
	// caller's pointer) so the column does not alternate between a number
	// and no row at all.
	if a.Conntrack != nil {
		s.ct, s.haveCT = *a.Conntrack, true
	}
	if s.haveCT {
		fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_api_conntrack (time, host, entries) VALUES (%s, %s, %d)%s", ts, s.hostLit, s.ct, sqlEnd)
	}
	// Per-command failures are records, never values: they say which part of
	// the sample is missing and why.
	for _, msg := range a.Errors {
		fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_api_error (time, host, message) VALUES (%s, %s, %s)%s", ts, s.hostLit, sqlQuote(msg), sqlEnd)
	}
}

// gapRow records a lost sequence range. It is a row of its own rather than a
// silence, so a query can see the hole instead of interpolating across it:
// every consumer sees the cadence each source really had.
func (s *SQL) gapRow(ts string, g *transport.Gap) {
	fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_gap (time, host, seq_from, seq_to) VALUES (%s, %s, %d, %d)%s", ts, s.hostLit, g.From, g.To, sqlEnd)
}

// sqlEnd closes every statement. DO NOTHING without a conflict target covers
// the primary key and any index the operator adds later.
const sqlEnd = " ON CONFLICT DO NOTHING;\n"

// sqlStamp renders a nanosecond wall clock as a PostgreSQL timestamptz
// literal. PostgreSQL stores microseconds, so the last three digits are
// rounded away on the way in; at 10 Hz that is invisible, and RFC 3339 text
// is used rather than epoch arithmetic so the file stays readable in psql.
func sqlStamp(ns int64) string {
	return "'" + time.Unix(0, ns).UTC().Format(time.RFC3339Nano) + "'::timestamptz"
}

// sqlLiteral is the escaping every quoted value goes through. It is a
// package-level Replacer rather than one per call because sqlQuote runs once
// per IRQ, thermal zone, slab cache, disk, flash device, kmsg record,
// interface and health name — on the order of 250 calls a second at 10 Hz
// with the privileged sources on — and strings.NewReplacer builds its trie
// on every construction.
var sqlLiteral = strings.NewReplacer("\x00", "", "'", "''")

// sqlQuote renders a PostgreSQL string literal. Doubling the single quote is
// the whole of the escaping needed with standard_conforming_strings on, which
// the header SETs rather than assumes.
//
// Two byte classes are coerced instead of quoted, because PostgreSQL will not
// take them at all and both have a real source here — kmsg messages are raw
// kernel bytes (procfs.ParseKmsgRecord cuts at the first newline and strips
// quotes, it does not validate) and RouterOS interface names are whatever the
// operator typed. A NUL cannot be stored in a text column, so it is dropped;
// a byte sequence that is not valid UTF-8 makes the server reject the whole
// statement ("invalid byte sequence for encoding UTF8"), so it becomes U+FFFD
// — visible in the row, rather than a row psql skipped.
func sqlQuote(v string) string {
	return "'" + sqlLiteral.Replace(strings.ToValidUTF8(v, "\uFFFD")) + "'"
}

// derivedRow writes the derive stage's values beside the sample's own rows.
func (s *SQL) derivedRow(ts string, d *derive.Derived) {
	if d == nil {
		return
	}
	fmt.Fprintf(&s.buf, "INSERT INTO mikroscope_derived (time, host, seq, mem_pressure, burst, suspect, cycles_per_packet, instructions_per_packet, cache_misses_per_packet, packets_per_irq) VALUES (%s, %s, %d, %d, %t, %t, %s, %s, %s, %s)%s",
		ts, s.hostLit, d.Seq, d.MemPressure, d.Burst, d.Suspect, sqlFloatPtr(d.CyclesPerPacket), sqlFloatPtr(d.InstructionsPerPkt), sqlFloatPtr(d.CacheMissesPerPacket), sqlFloatPtr(d.PacketsPerIRQ), sqlEnd)
}

// sqlFloatPtr renders an optional float: NULL when it could not be computed.
func sqlFloatPtr(v *float64) string {
	if v == nil {
		return "NULL"
	}
	return sqlFloat(*v, -1)
}

// sqlNullIfZero renders a ceiling the kernel may not publish: NULL for
// "none published", the number otherwise. A ceiling of zero is not a thing.
func sqlNullIfZero(v uint64) string {
	if v == 0 {
		return "NULL"
	}
	return strconv.FormatUint(v, 10)
}

// sqlLabel renders an optional text label: NULL when empty, quoted otherwise.
// A port with no RouterOS comment gets NULL rather than an empty string, so
// "no label" and "a label that is the empty string" stay distinguishable.
func sqlLabel(v string) string {
	if v == "" {
		return "NULL"
	}
	return sqlQuote(v)
}

// sqlFloat renders a DOUBLE PRECISION literal at prec decimals, or the
// shortest exact form when prec is -1.
//
// NaN and ±Inf become NULL. PostgreSQL does accept the *quoted* 'NaN' as a
// float, but unquoted — which is how a number appears in a VALUES list — NaN
// parses as a column reference and aborts the statement with `column "nan"
// does not exist`. Every float this sink writes goes through here for that
// reason, not just the health readings: a reading that is not a number means
// the source did not answer, and NULL is the column that says so.
func sqlFloat(v float64, prec int) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "NULL"
	}
	return strconv.FormatFloat(v, 'f', prec, 64)
}

// sqlSortedKeys orders map keys so the same sample always renders to the same
// bytes; Go's map order is not an order.
func sqlSortedKeys[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Stats implements Sink.
func (s *SQL) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// Close implements Sink: flush, then release the file. Stdout is flushed but
// not closed — it is not this sink's to close.
func (s *SQL) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.w.Flush()
	if s.f != nil {
		// Closed even when the flush failed, so a full filesystem does not
		// also leak the descriptor; the flush error is the one reported.
		if cerr := s.f.Close(); err == nil {
			err = cerr
		}
	}
	return err
}
