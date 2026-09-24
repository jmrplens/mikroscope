package sinks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmrplens/mikroscope/internal/apitier"
	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/sample"
	"github.com/jmrplens/mikroscope/internal/transport"
)

// Elasticsearch writes the merged timeline to Elasticsearch or OpenSearch
// through the bulk API, which both products share: `POST <endpoint>/_bulk`,
// `Content-Type: application/x-ndjson`, one action line and one document
// line per document, and a trailing newline on the last one — a body whose
// final newline is missing is rejected whole, which is why every line this
// sink renders carries its own.
//
// One document per timeline event, plus one further document for each
// kernel-log record a kernel sample carried, because a marker is not a
// metric and Elasticsearch is one of the few destinations that has a place
// for it. `kind` separates them: "kernel" (one agent tick), "event" (one
// /dev/kmsg record), "api" (one 1 Hz RouterOS read), "gap" (a seq range the
// collector lost). Every document is stamped `@timestamp` in RFC3339Nano
// from the agent's own skew-corrected clock, never from delivery time, so a
// batch that waited in the queue still lands in the day it was sampled.
//
// Index names come from Index with %Y, %m and %d expanded against that same
// timestamp, so the default "mikroscope-%Y.%m.%d" rolls daily, and are
// lower-cased because an index name may not carry an upper-case letter and
// the cluster refuses the whole bulk request rather than naming the line.
//
// `_id` is the document's identity — kind, host, the document's own
// timestamp in ns, and the sequence that identifies it within that instant
// — and the action is `index`, so a batch the cluster applied but whose
// response was lost is re-sent without duplicating a document. This is the
// convergence InfluxDB gives for free through series plus timestamp. The
// timestamp is part of the identity and not decoration: the agent's `seq`
// is a per-process counter (internal/agent/sampler.go — an atomic that
// starts at 0 every launch), so an id of kind, host and seq alone would
// make a restarted agent's sample 1 overwrite the sample 1 already indexed
// for that day, silently and with a 201 for each overwrite. The same holds
// for /dev/kmsg's own seq across a router reboot and for a gap's seq range
// after a restart.
//
// One batch per second, or earlier when the batch reaches 1 MiB (a bulk
// request has no protocol size limit but every deployment has an HTTP body
// one; 1 MiB is far below the usual defaults and, at the 64 KiB/s budget
// below, is a safety valve against a burst rather than the normal path). A
// bounded queue of QueueSeconds seconds, drop-oldest on full with the
// newest batch never dropped, exponential backoff from 2 s to 60 s, and at
// most one log line per minute for delivery failures and one per minute for
// cluster refusals.
//
// Stats are in batch units, with one deliberate addition: Written is one
// per bulk request the cluster accepted (2xx), Errors is one per failed
// delivery attempt, and Dropped is one per batch evicted by the byte budget
// PLUS one per document the cluster refused. Both kinds of Dropped are
// telemetry that will never be searchable, which is what the operator needs
// that number to mean. The refusals are the reason this counter is not
// purely batch-unit: a bulk request answers 200 even when every one of its
// items failed, so the verdict is per item inside the body and no
// transport-level counter would ever show it.
//
// The byte budget is influx.go's, unchanged: QueueSeconds × 64 KiB. The
// only sizes established here are from this package's own fixtures, not
// from a device (2026-09-12): the two-core `kernel` fixture with no
// optional source renders to 1077 B of ndjson (action lines included), and
// the same sample with PSI, sched, softirq, thermal, freq, flash, disk,
// slab and one kmsg record to 1952 B across its two documents. Against the
// ~1.2 KiB influx.go measured for a 10 Hz kernel sample as line protocol on
// the RB5009UG+S+ (RouterOS 7.24.2, kernel 5.6.3 aarch64, 2026-09-12) that
// is the same order, so 64 KiB/s holds a comparable backlog — but a real
// four-core sample from the reference device has not been rendered in this
// format at all, and the fixture has two cores and a shorter interface
// list, so treat the ~50 s influx.go claims as an upper bound here rather
// than a figure. Not measured above 10 Hz and not on the hEX S, which has
// not arrived.
//
// The API tier's own `Errors` (per-command RouterOS failures) are
// deliberately not emitted: they are the reader's failures, not this sink's,
// and the sink contract keeps them out of the data. Whatever parts of that
// sample did arrive are still written.
type Elasticsearch struct {
	Endpoint string // full bulk URL, e.g. http://opensearch:9200/_bulk
	Auth     string // an Elasticsearch API key, or "user:password" for basic auth; may be empty
	Index    string // index pattern with %Y/%m/%d, e.g. mikroscope-%Y.%m.%d
	Host     string // host field added to every document
	Client   *http.Client
	Log      func(string)

	mu      sync.Mutex
	queue   [][]byte // batches waiting, oldest first
	queued  int      // bytes queued
	stats   Stats
	backoff time.Duration
	lastLog time.Time
	// lastRej gates the refusal log separately from lastLog: an unreachable
	// cluster and a mapping conflict call for different operator actions,
	// and a minute of one must not hide the other.
	lastRej  time.Time
	ct       *uint64 // last conntrack count: it arrives every N seconds, not every sample
	stop     chan struct{}
	done     chan struct{}
	cur      bytes.Buffer
	maxQ     int
	maxBatch int
}

// NewElasticsearch starts the flusher. endpoint is the cluster root
// (http://host:9200) or a full bulk URL — "/_bulk" is appended when it is
// missing. auth is an API key, or "user:password" for the basic auth
// OpenSearch deployments usually want; empty sends no credential. index is
// the pattern, defaulting to "mikroscope-%Y.%m.%d". queueSeconds <= 0
// means 60.
func NewElasticsearch(endpoint, index, auth, host string, queueSeconds int, log func(string)) *Elasticsearch {
	s := newElasticsearch(endpoint, index, auth, host, queueSeconds, log)
	go s.loop()
	return s
}

// newElasticsearch builds the sink without starting its flusher, so a test
// can drive rotate and the queue policy by hand. With the flusher running,
// its once-a-second rotate lands between a test's writes whenever it likes:
// a round split in two became 41 batches instead of 40 and one drop too many,
// which is how TestElasticsearchSinkDropsOldestWhenQueueIsFull failed under
// -race, where everything is slow enough for the tick to land mid-round.
func newElasticsearch(endpoint, index, auth, host string, queueSeconds int, log func(string)) *Elasticsearch {
	if queueSeconds <= 0 {
		queueSeconds = 60
	}
	if index == "" {
		index = "mikroscope-%Y.%m.%d"
	}
	s := &Elasticsearch{
		Endpoint: esBulkURL(endpoint), Auth: auth, Index: index, Host: host,
		Client: &http.Client{Timeout: 10 * time.Second}, Log: log,
		stop: make(chan struct{}), done: make(chan struct{}),
		maxQ: queueSeconds * 64 << 10, maxBatch: 1 << 20,
	}
	if s.Log == nil {
		s.Log = func(string) {}
	}
	return s
}

// esBulkURL appends the bulk path when the endpoint names only the cluster
// root. It works on the parsed path, not on the string: an endpoint that
// already carries a query — ".../_bulk?refresh=false" is the one an operator
// reaches for — would otherwise get "/_bulk" appended after the query and
// become a URL the cluster answers 404 to, with the failure showing up only
// as a rising Errors count. An endpoint that will not parse is left alone;
// repairing it here would only guess, and post reports what the cluster says.
func esBulkURL(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return endpoint
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/_bulk") {
		u.Path += "/_bulk"
	}
	return u.String()
}

// Name implements Sink: the endpoint with any URL credential redacted.
// Basic auth in the URL — https://elastic:pw@es:9200 — is the idiomatic way
// to reach a secured cluster, and Name is printed to the operator's terminal
// at the end of every forward run, so it is one of the three places the
// house rule says a credential may never appear. The Auth parameter never
// appears here at all.
func (s *Elasticsearch) Name() string {
	if u, err := url.Parse(s.Endpoint); err == nil {
		return "elasticsearch " + u.Redacted()
	}
	return "elasticsearch " + s.Endpoint
}

// Write implements Sink: it renders the event's documents into the current
// batch and closes that batch early once it reaches the size bound, so the
// size bound costs a memcpy rather than any I/O on the collector's loop.
func (s *Elasticsearch) Write(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case e.Kernel != nil:
		k := e.Kernel
		doc := s.kernelDoc(k)
		if e.Derived != nil {
			doc["derived"] = e.Derived
		}
		s.emit(k.WallNS, "k", strconv.FormatUint(k.Seq, 10), doc)
		for _, r := range k.Events {
			s.emit(k.WallNS, "e", strconv.FormatUint(r.Seq, 10), esEventDoc(r))
		}
	case e.API != nil:
		a := e.API
		if a.Conntrack != nil {
			v := *a.Conntrack // copied: the Event and everything under it belongs to the caller
			s.ct = &v
		}
		adoc := s.apiDoc(a)
		if len(e.Shares) > 0 {
			adoc["fastpath"] = e.Shares
		}
		s.emit(a.WallNS, "a", "", adoc)
	case e.Sampler != nil:
		s.emit(e.At, "sampler", "", map[string]any{"kind": "sampler", "sampler": e.Sampler})
	case e.Device != nil:
		s.emit(e.At, "device", e.Device.Hash, map[string]any{"kind": "device", "device": e.Device})
	case e.Detection != nil:
		d := e.Detection
		s.emit(d.WallNS, "detection", d.Rule+"-"+strconv.FormatUint(d.Seq, 10), map[string]any{
			"kind": "detection", "rule": d.Rule, "key": d.Key, "seq": d.Seq, "value": d.Value, "threshold": d.Threshold, "message": d.Message,
		})
	case e.Trigger != nil:
		t := e.Trigger
		s.emit(t.WallNS, "trigger", strconv.FormatUint(t.ID, 10), map[string]any{
			"kind": "trigger", "id": t.ID, "cause": t.Cause, "field": t.Field, "value": t.Value, "threshold": t.Threshold, "seq": t.Seq,
		})
	case e.Gap != nil:
		// A gap carries no timestamp of its own, so it carries the collector's:
		// e.At, read once by the forwarder for every sink. Interpolating it
		// into the sampled timeline would hide the very thing it records.
		s.emit(e.At, "g", strconv.FormatUint(e.Gap.From, 10)+"-"+strconv.FormatUint(e.Gap.To, 10), esGapDoc(e.Gap))
	default:
		return
	}
	if s.cur.Len() >= s.maxBatch {
		s.rotateLocked()
	}
}

// esAction is the bulk action line. "index" rather than "create" is what
// makes a re-sent batch converge on the same documents.
type esAction struct {
	Index struct {
		Index string `json:"_index"`
		ID    string `json:"_id"`
	} `json:"index"`
}

// emit appends one action/document pair to the current batch. The id is
// kind, host, the document's own timestamp in ns, and tail — the sequence
// that distinguishes documents sharing that instant, empty when the
// timestamp alone identifies the document. Including the timestamp is what
// keeps a restarted agent, whose seq begins again at 1, from overwriting
// the day's earlier documents (see the type comment).
//
// A document that will not marshal (a NaN out of /proc/loadavg or a health
// sensor would do it) is counted as dropped rather than dropped silently:
// it is telemetry that will never be searchable, which is what Dropped
// means here.
func (s *Elasticsearch) emit(ns int64, kind, tail string, doc map[string]any) {
	doc["@timestamp"] = time.Unix(0, ns).UTC().Format(time.RFC3339Nano)
	if s.Host != "" {
		doc["host"] = s.Host
	}
	d, err := json.Marshal(doc)
	if err != nil {
		s.stats.Dropped++
		return
	}
	id := kind + "." + s.Host + "." + strconv.FormatInt(ns, 10)
	if tail != "" {
		id += "." + tail
	}
	var a esAction
	a.Index.Index = s.indexFor(ns)
	a.Index.ID = id
	line, err := json.Marshal(&a)
	if err != nil {
		s.stats.Dropped++
		return
	}
	s.cur.Write(line)
	s.cur.WriteByte('\n')
	s.cur.Write(d)
	s.cur.WriteByte('\n')
}

// indexFor expands the date pattern against the document's own timestamp,
// so a batch that sat in the queue across midnight still lands in the index
// for the day it was sampled. Only %Y, %m and %d: daily and monthly
// rollover is the whole of what an index pattern is used for here, and a
// fuller strftime would be either a dependency or a parser.
func (s *Elasticsearch) indexFor(ns int64) string {
	t := time.Unix(0, ns).UTC()
	return strings.ToLower(strings.NewReplacer(
		"%Y", strconv.Itoa(t.Year()),
		"%m", fmt.Sprintf("%02d", int(t.Month())),
		"%d", fmt.Sprintf("%02d", t.Day()),
	).Replace(s.Index))
}

// kernelDoc is one agent tick: the tick deltas the agent ships raw — it
// never ships percentages, and the one ratio below is computed with the only
// sanctioned helper, alongside the ticks, never instead of them — and the
// few absolute levels. The fields
// named here are always read; everything a kernel or container may not
// expose is added by kernelOptional.
func (s *Elasticsearch) kernelDoc(k *sample.Sample) map[string]any {
	cpus := make([]map[string]any, 0, len(k.CPU))
	for i, c := range k.CPU {
		cpus = append(cpus, map[string]any{
			"cpu": i, "user": c.User, "nice": c.Nice, "system": c.System,
			"idle": c.Idle, "iowait": c.IOWait, "irq": c.IRQ, "softirq": c.SoftIRQ,
			"steal": c.Steal, "busy": c.Busy(), "busy_ratio": c.BusyRatio(k.DtNS),
		})
	}
	doc := map[string]any{
		"kind": "kernel", "seq": k.Seq, "mono_ns": k.MonoNS, "dt_ns": k.DtNS,
		"cpu": cpus, "cpu_total_busy": k.CPUTotal.Busy(),
		"stat": map[string]any{"ctxt": k.Ctxt, "intr": k.Intr, "forks": k.Forks, "irq_total": k.IRQTotal, "irq_err": k.IRQErr},
		"mem": map[string]any{
			"total_kb": k.Mem.MemTotal, "free_kb": k.Mem.MemFree, "available_kb": k.Mem.MemAvailable,
			"buffers_kb": k.Mem.Buffers, "cached_kb": k.Mem.Cached, "dirty_kb": k.Mem.Dirty,
			"writeback_kb": k.Mem.Writeback, "shmem_kb": k.Mem.Shmem, "slab_kb": k.Mem.Slab,
			"sreclaimable_kb": k.Mem.SReclaimable, "sunreclaim_kb": k.Mem.SUnreclaim,
			"anon_kb": k.Mem.AnonPages, "mapped_kb": k.Mem.Mapped,
			"kernel_stack_kb": k.Mem.KernelStack, "page_tables_kb": k.Mem.PageTables,
			"active_kb": k.Mem.Active, "inactive_kb": k.Mem.Inactive,
			"commit_limit_kb": k.Mem.CommitLimit, "committed_as_kb": k.Mem.CommittedAS,
		},
		"load": map[string]any{
			"load1": k.Load.Load1, "load5": k.Load.Load5, "load15": k.Load.Load15,
			"running": k.Load.Running, "threads": k.Load.Total, "procs_blocked": k.ProcsBlocked,
		},
		"vm": k.VM, "self": k.Self,
	}
	s.kernelOptional(doc, k)
	return doc
}

// kernelOptional adds only the sources this kernel and this container
// actually read. An absent source means "cannot be read here", not zero:
// PSI does not exist on the reference RB5009's 5.6.3 kernel (/proc/pressure
// is absent, RouterOS 7.24.2, arm64, 2026-09-11), Slab and Events need
// privileged=yes, which drops the user namespace and makes /proc/slabinfo
// and /dev/kmsg readable (same device, 2026-09-12), and Thermal, FreqKHz,
// Flash and Disk are empty on a device that has none. A zero is
// indistinguishable in Kibana from a real measurement of zero, so an absent
// source contributes no key at all.
func (s *Elasticsearch) kernelOptional(doc map[string]any, k *sample.Sample) {
	if k.PSI != nil {
		doc["psi"] = k.PSI
	}
	if k.VMG != (sample.VMGauge{}) {
		doc["vmg"] = k.VMG
	}
	if len(k.Softnet) > 0 {
		doc["softnet"] = esSoftnet(k.Softnet)
	}
	if len(k.Sched) > 0 {
		doc["sched"] = esSched(k.Sched)
	}
	if len(k.IRQ) > 0 {
		doc["irq"] = esIRQ(k.IRQ)
	}
	if len(k.Softirq) > 0 {
		doc["softirq"] = esSoftirq(k.Softirq)
	}
	if len(k.Thermal) > 0 {
		doc["thermal"] = esThermal(k.Thermal, k.ThermalCritical)
	}
	if len(k.FreqKHz) > 0 {
		doc["freq_khz"] = k.FreqKHz
	}
	if len(k.Flash) > 0 {
		doc["flash"] = esFlash(k.Flash)
	}
	if len(k.Disk) > 0 {
		doc["disk"] = esDisk(k.Disk)
	}
	if len(k.Slab) > 0 {
		// Active objects per cache, absolute. encoding/json sorts map keys,
		// which is the determinism the house requires, so no manual sort.
		doc["slab"] = k.Slab
		if len(k.SlabLimit) > 0 {
			doc["slab_limit"] = k.SlabLimit
		}
	}
	// Both carry their own JSON tags (procfs.BuddyZone, procfs.MTDHealth);
	// both are levels the agent emits on change, so absent is "unchanged
	// since the last document", not "unreadable".
	if len(k.Buddy) > 0 {
		doc["buddy"] = k.Buddy
	}
	if len(k.MTD) > 0 {
		doc["mtd"] = k.MTD
	}
}

// esSoftnet renders the per-CPU softnet deltas; the index is the CPU number.
func esSoftnet(in []sample.SoftnetDelta) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for i, n := range in {
		out = append(out, map[string]any{"cpu": i, "processed": n.Processed, "dropped": n.Dropped, "time_squeeze": n.TimeSqueeze})
	}
	return out
}

// esSched renders the per-CPU run and wait ns gained; the index is the CPU.
func esSched(in []sample.SchedDelta) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for i, c := range in {
		out = append(out, map[string]any{"cpu": i, "run_ns": c.RunNS, "wait_ns": c.WaitNS})
	}
	return out
}

// esIRQ renders the top-K interrupt sources. The per-CPU array is kept as
// well as the total: which core services an interrupt is the question the
// kernel tier exists to answer, and a sum throws it away.
func esIRQ(in []sample.IRQDelta) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, q := range in {
		var total uint64
		for _, v := range q.PerCPU {
			total += v
		}
		out = append(out, map[string]any{"irq": q.ID, "name": q.Name, "count": total, "per_cpu": q.PerCPU})
	}
	return out
}

// esSoftirq sums each softirq kind across CPUs. The per-CPU breakdown is
// dropped here deliberately: it would be a variable-width object per kind
// and Elasticsearch would map every width separately.
func esSoftirq(in map[string][]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(in))
	for name, per := range in {
		var total uint64
		for _, v := range per {
			total += v
		}
		out[name] = total
	}
	return out
}

// esThermal renders the thermal zones in Celsius, the zone's own critical
// trip converted the same way beside the reading where the board declares one.
func esThermal(in []procfs.Thermal, critical map[string]int64) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, t := range in {
		doc := map[string]any{"type": t.Type, "celsius": float64(t.MilliC) / 1000}
		if crit := critical[t.Type]; crit > 0 {
			doc["critical_celsius"] = float64(crit) / 1000
		}
		out = append(out, doc)
	}
	return out
}

// esFlash renders the NAND wear per YAFFS device. Erasures is the counter
// that maps to flash lifetime; bad_blocks and free_chunks are levels.
func esFlash(in []sample.FlashDelta) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, f := range in {
		out = append(out, map[string]any{
			"device": f.Device, "page_writes": f.PageWrites, "page_reads": f.PageReads,
			"erasures": f.Erasures, "gc_copies": f.GCCopies, "gcs": f.GCs,
			"bad_blocks": f.BadBlocks, "free_chunks": f.FreeChunks,
		})
	}
	return out
}

// esDisk renders the block device I/O deltas; in_progress is a level.
func esDisk(in []sample.DiskDelta) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, d := range in {
		out = append(out, map[string]any{
			"name": d.Name, "reads_completed": d.ReadsCompleted, "read_sectors": d.ReadSectors,
			"writes_completed": d.WritesCompleted, "write_sectors": d.WriteSectors,
			"io_s": float64(d.IOTicks) / 1000, "in_progress": d.IOInProgress,
		})
	}
	return out
}

// apiDoc is one 1 Hz RouterOS read. Every part is optional because a failed
// command leaves its part nil, and every value is a level as RouterOS
// reports it — the per-core percentages are a cross-check against the kernel
// tier's tick deltas, never a substitute, which is why they sit under their
// own key.
func (s *Elasticsearch) apiDoc(a *apitier.Sample) map[string]any {
	doc := map[string]any{"kind": "api"}
	if a.System != nil {
		doc["system"] = map[string]any{
			"cpu_load": a.System.CPULoad, "free_memory": a.System.FreeMemory,
			"total_memory": a.System.TotalMemory, "free_hdd": a.System.FreeHDD,
			"uptime_s": a.System.UptimeS, "version": a.System.Version,
		}
	}
	if len(a.Cores) > 0 {
		cores := make([]map[string]any, 0, len(a.Cores))
		for i, c := range a.Cores {
			cores = append(cores, map[string]any{"cpu": i, "load": c.Load, "irq": c.IRQ, "disk": c.Disk})
		}
		doc["cores"] = cores
	}
	if len(a.Health) > 0 {
		doc["health"] = a.Health
	}
	if len(a.Ifaces) > 0 {
		doc["ifaces"] = esIfaces(a.Ifaces)
	}
	if len(a.IfaceCounters) > 0 {
		doc["iface_counters"] = a.IfaceCounters
	}
	if len(a.Inventory) > 0 {
		doc["inventory"] = a.Inventory
	}
	if s.ct != nil {
		// The held value, not a.Conntrack: it arrives only every N seconds
		// because it is a table scan, and a field that blinks between a
		// number and nothing is worse than a slightly stale one.
		doc["conntrack"] = *s.ct
	}
	return doc
}

// esIfaces renders monitor-traffic's instantaneous rates. They are levels,
// not counters: what the router reports right now, never a total.
func esIfaces(in []apitier.Iface) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, f := range in {
		doc := map[string]any{
			"name": f.Name, "rx_bps": f.RxBps, "tx_bps": f.TxBps,
			"rx_pps": f.RxPps, "tx_pps": f.TxPps,
		}
		// Loss rates only as the router returned them; an absent key is absent.
		for k, v := range f.Losses {
			doc[fieldKey(k)] = v
		}
		// What the port is (label, type, role, bridge) is added only where the
		// inventory has it, so a port without one does not carry an empty
		// string a query would have to filter out.
		for k, v := range map[string]string{"label": f.Comment, "type": f.Type, "role": f.Role, "bridge": f.Bridge} {
			if v != "" {
				doc[k] = v
			}
		}
		out = append(out, doc)
	}
	return out
}

// esEventDoc is one /dev/kmsg marker. It is not a metric: the message stays
// free text in its own field and never becomes a label or a value, and the
// kernel's own microsecond stamp is kept as time_usec because it is
// monotonic since boot and so is not comparable with @timestamp.
func esEventDoc(r procfs.KmsgRecord) map[string]any {
	doc := map[string]any{
		"kind": "event", "priority": r.Priority, "level": r.Level,
		"facility": r.Facility, "seq": r.Seq, "time_usec": r.TimeUsec, "message": r.Message,
	}
	// The port the record names, what happened to it, and what the port is,
	// each only when known.
	for k, v := range map[string]string{"iface": r.Iface, "ros_iface": r.ROSIface, "port_event": r.Kind, "label": r.Label, "role": r.Role} {
		if v != "" {
			doc[k] = v
		}
	}
	return doc
}

// esGapDoc is the seq range the collector lost. From..To is inclusive
// (agent/http.go emits from=since+1, to=oldest-1), so lost is the count of
// samples that will never arrive.
func esGapDoc(g *transport.Gap) map[string]any {
	var lost uint64
	if g.To >= g.From {
		lost = g.To - g.From + 1
	}
	return map[string]any{"kind": "gap", "from": g.From, "to": g.To, "lost": lost}
}

// rotate moves the current batch to the queue, then drops the oldest
// batches past the byte budget. Eviction lives here and not in
// rotateLocked because rotate runs only on the flusher goroutine, and there
// only immediately before flush: never while a post is in flight. flush
// releases mu around its post and then pops the head it delivered, so a
// concurrent eviction of that same head — which Write could cause, since
// Write closes an oversized batch itself — would have flush remove an
// undelivered batch instead and subtract its bytes twice, leaving s.queued
// below the truth and eventually negative, at which point the byte budget
// stops bounding anything.
func (s *Elasticsearch) rotate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rotateLocked()
	for s.queued > s.maxQ && len(s.queue) > 1 {
		s.queued -= len(s.queue[0])
		s.queue = s.queue[1:]
		s.stats.Dropped++
	}
}

// rotateLocked closes the current batch with s.mu already held, so Write can
// close an oversized batch without releasing and retaking the lock. It only
// appends, so the queue can sit over its byte budget until the flusher
// goroutine next reaches rotate: one ticker period normally, and up to
// post's 10 s timeout when a delivery is hanging. What that overshoot comes
// to in bytes has not been measured — it is whatever the collector's pull
// loop renders in that window, and a pull that drains the agent's ring after
// a gap renders far more than a steady 10 Hz second. The bound is loose but
// it exists; evicting from Write, by contrast, corrupts the accounting that
// makes any bound hold at all.
func (s *Elasticsearch) rotateLocked() {
	if s.cur.Len() == 0 {
		return
	}
	b := make([]byte, s.cur.Len())
	copy(b, s.cur.Bytes())
	s.cur.Reset()
	s.queue = append(s.queue, b)
	s.queued += len(b)
}

func (s *Elasticsearch) loop() {
	defer close(s.done)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			s.rotate()
			s.flush()
			return
		case <-t.C:
			s.rotate()
			s.flush()
		}
	}
}

// flush posts queued batches in order until one fails, then backs off.
func (s *Elasticsearch) flush() {
	for {
		s.mu.Lock()
		if len(s.queue) == 0 || s.backoff > 0 {
			if s.backoff > 0 {
				s.backoff -= time.Second
			}
			s.mu.Unlock()
			return
		}
		b := s.queue[0]
		s.mu.Unlock()
		refused, reason, err := s.post(b)
		if err != nil {
			s.fail(err)
			return
		}
		s.mu.Lock()
		s.queue = s.queue[1:]
		s.queued -= len(b)
		s.stats.Written++
		s.backoff = 0
		if refused > 0 {
			s.stats.Dropped += refused
			if time.Since(s.lastRej) > time.Minute {
				s.lastRej = time.Now()
				s.Log("elasticsearch: " + strconv.FormatUint(refused, 10) + " document(s) refused: " + reason)
			}
		}
		s.mu.Unlock()
	}
}

// fail records a delivery failure and lengthens the wait. The wait itself is
// realized by the per-second decrement in flush's early return, not a timer.
func (s *Elasticsearch) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.Errors++
	if s.backoff == 0 {
		s.backoff = 2 * time.Second
	} else {
		s.backoff = min(2*s.backoff, 60*time.Second)
	}
	if time.Since(s.lastLog) > time.Minute {
		s.lastLog = time.Now()
		s.Log("elasticsearch: " + err.Error() + " (retrying with backoff)")
	}
}

// post sends one bulk request and returns how many of its documents the
// cluster refused, the first reason, and the delivery error if the request
// itself failed. The credential never reaches the error or the log.
func (s *Elasticsearch) post(b []byte) (refused uint64, reason string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Endpoint, bytes.NewReader(b))
	if err != nil {
		return 0, "", err
	}
	// A bytes.Reader body makes the request replayable (GetBody is set), and
	// net/http then re-sends it on its own when a pooled connection turns
	// out dead — with no error to the caller, so a batch the server had
	// already committed is written twice. That is the leading explanation for
	// the duplicate rows of the 2026-09-13 overnight run against InfluxDB 3,
	// never reproduced and so never confirmed. Without GetBody a dead
	// connection is an error, the batch is retried here, and Errors counts
	// it.
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/x-ndjson")
	switch user, pass, ok := strings.Cut(s.Auth, ":"); {
	case ok:
		req.SetBasicAuth(user, pass)
	case s.Auth != "":
		req.Header.Set("Authorization", "ApiKey "+s.Auth)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, "", fmt.Errorf("%s: %s", resp.Status, bytes.TrimSpace(body))
	}
	refused, reason = esRefused(resp.Body)
	return refused, reason, nil
}

// esBulkResponse is the part of a bulk response that says what went wrong.
// The per-item key is the action name, "index" here; a refused item carries
// an error object and a successful one does not.
type esBulkResponse struct {
	Errors bool `json:"errors"`
	Items  []struct {
		Index struct {
			Index  string `json:"_index"`
			Status int    `json:"status"`
			Error  *struct {
				Type   string `json:"type"`
				Reason string `json:"reason"`
			} `json:"error"`
		} `json:"index"`
	} `json:"items"`
}

// esRefused counts the documents the cluster would not index and returns the
// first reason. This is the trap the bulk API sets: the request answers 200
// even when every item failed, and `errors` in the body is the only place
// the failure appears.
//
// Only the first reason is kept. A bulk rejection is almost always one
// mapping conflict repeated across every document of a kind, and the log is
// rate-limited to one line a minute anyway.
//
// A body that will not decode returns no refusals rather than a delivery
// error: the cluster answered 2xx, so the batch is gone either way, and
// re-sending it would rest on a guess about what it did with it.
func esRefused(body io.Reader) (refused uint64, reason string) {
	var out esBulkResponse
	// Bounded because nothing here should trust a remote's content length.
	// 8 MiB holds the per-item verdict for a 1 MiB request many times over.
	if json.NewDecoder(io.LimitReader(body, 8<<20)).Decode(&out) != nil || !out.Errors {
		return 0, ""
	}
	var n uint64
	var first string
	for _, it := range out.Items {
		if it.Index.Error == nil {
			continue
		}
		n++
		if first == "" {
			first = fmt.Sprintf("%s: %s: %s", it.Index.Index, it.Index.Error.Type, it.Index.Error.Reason)
		}
	}
	return n, first
}

// Stats implements Sink.
func (s *Elasticsearch) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// Close implements Sink: a last flush, then stop.
func (s *Elasticsearch) Close() error {
	close(s.stop)
	<-s.done
	return nil
}
