// Package record is the diagnostician's path: pull samples from the agent
// for a window, write them as JSONL (verbatim) and CSV (wide), and keep a
// marker file where every line typed on stdin, every gap and — opt-in —
// every router log line lands with the agent's timestamp, so "what happened
// at 14:03" has an answer.
package record

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmrplens/mikroscope/internal/agent"
	"github.com/jmrplens/mikroscope/internal/sample"
	"github.com/jmrplens/mikroscope/internal/transport"
)

// Options configure one recording.
type Options struct {
	Prefix    string        // output files: <Prefix>.jsonl, .csv, .markers.csv
	For       time.Duration // 0 = until ctx is done
	FromStart bool          // backfill everything the ring holds instead of starting live
	Batch     int           // samples per pull
	Poll      time.Duration // pull interval
}

// Summary is what a recording produced.
type Summary struct {
	Samples   int
	FirstSeq  uint64
	LastSeq   uint64
	Gaps      []transport.Gap
	Markers   int
	SkewNS    int64 // agent wall − host wall at start
	Transport string
	Files     []string
}

// Marker is one row of the markers file.
type Marker struct {
	WallNS int64
	Seq    uint64
	Kind   string // note, gap, log
	Label  string
}

// Meta is written as <Prefix>.meta.json at the start of a recording so
// tools that run later (mark, plot) know the clock skew and the agent.
type Meta struct {
	StartedUTC string `json:"started_utc"`
	SkewNS     int64  `json:"skew_ns"` // agent wall − host wall
	Agent      string `json:"agent"`
	RateHz     int    `json:"rate_hz"`
	Transport  string `json:"transport"`
	// Capabilities is what the agent established about the board, fetched
	// from /capabilities when the transport can; absent otherwise.
	Capabilities *agent.Capabilities `json:"capabilities,omitempty"`
}

// ReadMeta loads <prefix>.meta.json.
func ReadMeta(prefix string) (Meta, error) {
	b, err := os.ReadFile(prefix + ".meta.json") // #nosec G304 -- the operator's own prefix
	if err != nil {
		return Meta{}, err
	}
	var m Meta
	if decErr := json.Unmarshal(b, &m); decErr != nil {
		return Meta{}, decErr
	}
	return m, nil
}

// Recorder pulls from a transport and writes the three files.
type Recorder struct {
	Puller  transport.Puller
	Opts    Options
	Notes   io.Reader // stdin: one marker per line; nil for none
	Log     func(string)
	skewNS  int64
	lastSeq uint64
	mu      sync.Mutex
	nMark   int
}

// Run records until ctx is done or Opts.For elapses.
func (rc *Recorder) Run(ctx context.Context) (Summary, error) {
	if rc.Opts.Poll <= 0 {
		rc.Opts.Poll = 500 * time.Millisecond
	}
	if rc.Log == nil {
		rc.Log = func(string) {}
	}
	if rc.Opts.For > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, rc.Opts.For)
		defer cancel()
	}
	h, err := rc.Puller.Health(ctx)
	if err != nil {
		return Summary{}, fmt.Errorf("agent health: %w", err)
	}
	rc.skewNS = h.WallNS - time.Now().UnixNano()
	if rc.Opts.Batch <= 0 {
		rc.Opts.Batch = transport.BatchFor(h.RateHz, rc.Opts.Poll)
	}
	files, closeAll, err := rc.open()
	if err != nil {
		return Summary{}, err
	}
	defer closeAll()
	sum := Summary{SkewNS: rc.skewNS, Transport: rc.Puller.Name(), Files: files.names}
	m := Meta{StartedUTC: time.Now().UTC().Format(time.RFC3339), SkewNS: rc.skewNS, Agent: h.Version, RateHz: h.RateHz, Transport: sum.Transport}
	if cf, ok := rc.Puller.(transport.CapabilityFetcher); ok {
		if caps, capsErr := cf.Capabilities(ctx); capsErr == nil {
			m.Capabilities = &caps
		}
	}
	meta, metaErr := json.MarshalIndent(m, "", "  ")
	if metaErr == nil {
		metaErr = os.WriteFile(rc.Opts.Prefix+".meta.json", meta, 0o600)
	}
	if metaErr != nil {
		return Summary{}, metaErr
	}
	sum.Files = append(sum.Files, rc.Opts.Prefix+".meta.json")
	rc.Log(fmt.Sprintf("agent %s, %d Hz, seq %d (ring from %d), clock skew %s, via %s", h.Version, h.RateHz, h.Seq, h.OldestSeq, time.Duration(rc.skewNS).Round(time.Millisecond), sum.Transport))
	since := h.Seq
	if rc.Opts.FromStart {
		since = 0
	}
	if rc.Notes != nil {
		go rc.readNotes(ctx)
	}
	ticker := time.NewTicker(rc.Opts.Poll)
	defer ticker.Stop()
	for {
		if pullErr := rc.pull(ctx, files, &sum, &since); pullErr != nil {
			return sum, pullErr
		}
		select {
		case <-ctx.Done():
			// One last pull drains what arrived while we were waiting.
			_ = rc.pull(context.WithoutCancel(ctx), files, &sum, &since)
			sum.Markers = rc.nMark
			return sum, files.flush()
		case <-ticker.C:
		}
	}
}

// pull drains the ring: it asks again while the agent answered with a full
// batch (see forward.Forwarder.pull), at most maxDrain times per poll.
func (rc *Recorder) pull(ctx context.Context, files *outputs, sum *Summary, since *uint64) error {
	eff := transport.EffectiveBatch(rc.Puller, rc.Opts.Batch)
	for range maxDrain {
		n, err := rc.pullOnce(ctx, files, sum, since)
		if err != nil || n < eff {
			return err
		}
	}
	return nil
}

const maxDrain = 100

// pullOnce fetches one batch, records its gap and writes its samples. It
// returns how many lines came back, so the caller can tell a full batch
// from the ring's edge.
func (rc *Recorder) pullOnce(ctx context.Context, files *outputs, sum *Summary, since *uint64) (int, error) {
	lines, gap, err := rc.Puller.Pull(ctx, *since, rc.Opts.Batch)
	if err != nil {
		if ctx.Err() == nil {
			rc.Log("pull: " + err.Error())
		}
		return 0, nil
	}
	if gap != nil {
		sum.Gaps = append(sum.Gaps, *gap)
		rc.mark(Marker{WallNS: time.Now().UnixNano() + rc.skewNS, Seq: gap.To, Kind: "gap", Label: fmt.Sprintf("samples %d..%d lost", gap.From, gap.To)})
	}
	for _, line := range lines {
		var s sample.Sample
		if decErr := json.Unmarshal(line, &s); decErr != nil {
			rc.Log("bad sample line: " + decErr.Error())
			continue
		}
		if writeErr := files.write(line, &s); writeErr != nil {
			return 0, writeErr
		}
		if sum.Samples == 0 {
			sum.FirstSeq = s.Seq
		}
		sum.Samples++
		sum.LastSeq = s.Seq
		*since = s.Seq
		rc.lastSeq = s.Seq
	}
	return len(lines), nil
}

func (rc *Recorder) readNotes(ctx context.Context) {
	sc := bufio.NewScanner(rc.Notes)
	for sc.Scan() {
		if ctx.Err() != nil {
			return
		}
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		rc.mark(Marker{WallNS: time.Now().UnixNano() + rc.skewNS, Seq: rc.lastSeq, Kind: "note", Label: text})
	}
}

// mark writes one marker by opening the file, appending and closing it, the
// same path `mark` in another shell takes (AppendMarkers).
//
// NOT a held-open writer, which is what this was: two processes appending to
// one file is POSIX's guarantee, not Windows'. There, a second handle keeps
// its own offset, and a recorder that held the file open wrote its next row
// over the marker `mark` had just appended — 270 tests pass on Linux and macOS
// and that one fails on windows-latest, which is the whole reason the CI
// matrix runs three operating systems. A marker is rare enough that an open
// and a close per marker cost nothing measurable.
func (rc *Recorder) mark(m Marker) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.Opts.Prefix == "" {
		return
	}
	if err := AppendMarkers(rc.Opts.Prefix, []Marker{m}); err != nil {
		return
	}
	rc.nMark++
}

// openOutput opens one of the recording's files for writing, emptying whatever
// was there.
//
// O_APPEND is on every one of them, and it is what makes marking a RUNNING
// recording work: `mark` in another shell appends a row to the markers file
// while this process holds the recording open. O_TRUNC still empties each file
// at open, so a re-run does not append to the previous recording.
//
// The markers file is the one two processes write, and the recorder does not
// hold it open at all — it opens, appends and closes per marker, because a
// second handle on Windows keeps its own offset and would write over a row
// `mark` had just appended.
func openOutput(name string) (*os.File, error) {
	return os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_APPEND, 0o600) // #nosec G304 -- the operator's own --out prefix
}

// AppendMarkers appends markers to an existing <prefix>.markers.csv.
func AppendMarkers(prefix string, ms []Marker) error {
	f, err := os.OpenFile(prefix+".markers.csv", os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- the operator's own prefix
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	for _, m := range ms {
		if writeErr := w.Write(markerRow(m)); writeErr != nil {
			return writeErr
		}
	}
	w.Flush()
	return w.Error()
}

func markerRow(m Marker) []string {
	return []string{strconv.FormatInt(m.WallNS, 10), time.Unix(0, m.WallNS).UTC().Format(time.RFC3339Nano), strconv.FormatUint(m.Seq, 10), m.Kind, m.Label}
}

// MarkerHeader is the first row of the markers file.
var MarkerHeader = []string{"wall_ns", "wall_utc", "seq", "kind", "label"}

type outputs struct {
	names  []string
	jsonl  *bufio.Writer
	csvw   *csv.Writer
	header bool
	closer []io.Closer
}

func (rc *Recorder) open() (*outputs, func(), error) {
	o := &outputs{}
	closeAll := func() {
		for _, c := range o.closer {
			_ = c.Close()
		}
	}
	create := func(name string) (*os.File, error) {
		f, err := openOutput(name)
		if err != nil {
			closeAll()
			return nil, err
		}
		o.closer = append(o.closer, f)
		o.names = append(o.names, name)
		return f, nil
	}
	jf, err := create(rc.Opts.Prefix + ".jsonl")
	if err != nil {
		return nil, nil, err
	}
	o.jsonl = bufio.NewWriterSize(jf, 64<<10)
	cf, err := create(rc.Opts.Prefix + ".csv")
	if err != nil {
		return nil, nil, err
	}
	o.csvw = csv.NewWriter(cf)
	// The markers file is created with its header and closed again: every
	// later write to it, from this process or from `mark` in another shell,
	// opens it, appends and closes (see mark).
	mf, err := create(rc.Opts.Prefix + ".markers.csv")
	if err != nil {
		return nil, nil, err
	}
	mw := csv.NewWriter(mf)
	_ = mw.Write(MarkerHeader)
	mw.Flush()
	if closeErr := mf.Close(); closeErr != nil {
		return nil, nil, closeErr
	}
	return o, closeAll, nil
}

func (o *outputs) write(line []byte, s *sample.Sample) error {
	if _, err := o.jsonl.Write(line); err != nil {
		return err
	}
	if err := o.jsonl.WriteByte('\n'); err != nil {
		return err
	}
	if !o.header {
		if err := o.csvw.Write(CSVHeader(len(s.CPU), len(s.Softnet))); err != nil {
			return err
		}
		o.header = true
	}
	return o.csvw.Write(CSVRow(s))
}

func (o *outputs) flush() error {
	o.csvw.Flush()
	if err := o.csvw.Error(); err != nil {
		return err
	}
	return o.jsonl.Flush()
}

// CSVHeader is the wide header for a device with cores cores and softnet
// rows. Two groups size themselves to the device — one block per core and one
// per softnet queue — and a source that is absent there is an absent column,
// never an empty one. The rest of the header is fixed: mem, load, vm and self
// always have their columns, and CSVRow writes 0 in them when the sample has
// no such source.
func CSVHeader(cores, softnet int) []string {
	h := []string{"seq", "wall_ns", "wall_utc", "dt_ns", "busy_total"}
	for i := range cores {
		c := "c" + strconv.Itoa(i)
		h = append(h, c+"_busy", c+"_user", c+"_nice", c+"_system", c+"_idle", c+"_iowait", c+"_irq", c+"_softirq")
	}
	h = append(h, "ctxt", "intr", "irq_total")
	for i := range softnet {
		s := "softnet" + strconv.Itoa(i)
		h = append(h, s+"_processed", s+"_dropped", s+"_time_squeeze")
	}
	return append(h, "mem_free_kb", "mem_available_kb", "mem_cached_kb", "mem_slab_kb", "load1", "threads_running", "threads_total", "pgfault", "pgmajfault", "self_cpu_us", "self_rss_bytes")
}

// CSVRow renders one sample for the header above.
func CSVRow(s *sample.Sample) []string {
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }
	u := func(v uint64) string { return strconv.FormatUint(v, 10) }
	r := []string{u(s.Seq), strconv.FormatInt(s.WallNS, 10), time.Unix(0, s.WallNS).UTC().Format(time.RFC3339Nano), strconv.FormatInt(s.DtNS, 10)}
	var busy float64
	for _, c := range s.CPU {
		busy += c.BusyRatio(s.DtNS)
	}
	if len(s.CPU) > 0 {
		busy /= float64(len(s.CPU))
	}
	r = append(r, f(busy))
	for _, c := range s.CPU {
		r = append(r, f(c.BusyRatio(s.DtNS)), u(c.User), u(c.Nice), u(c.System), u(c.Idle), u(c.IOWait), u(c.IRQ), u(c.SoftIRQ))
	}
	r = append(r, u(s.Ctxt), u(s.Intr), u(s.IRQTotal))
	for _, n := range s.Softnet {
		r = append(r, u(n.Processed), u(n.Dropped), u(n.TimeSqueeze))
	}
	return append(r, u(s.Mem.MemFree), u(s.Mem.MemAvailable), u(s.Mem.Cached), u(s.Mem.Slab), f(s.Load.Load1), u(s.Load.Running), u(s.Load.Total), u(s.VM.PgFault), u(s.VM.PgMajFault), u(s.Self.CPUUsec), u(s.Self.RSSBytes))
}

// ErrNoSamples is returned by readers when a file holds nothing usable.
var ErrNoSamples = errors.New("record: no samples")

// ReadJSONL loads a recording's samples back.
func ReadJSONL(path string) ([]sample.Sample, error) {
	f, err := os.Open(path) // #nosec G304 -- the operator's own file
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []sample.Sample
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var s sample.Sample
		if json.Unmarshal(sc.Bytes(), &s) == nil && s.Seq > 0 {
			out = append(out, s)
		}
	}
	if scanErr := sc.Err(); scanErr != nil {
		return nil, scanErr
	}
	if len(out) == 0 {
		return nil, ErrNoSamples
	}
	return out, nil
}

// ReadMarkers loads a markers file.
func ReadMarkers(path string) ([]Marker, error) {
	f, err := os.Open(path) // #nosec G304 -- the operator's own file
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, err
	}
	var out []Marker
	for i, r := range rows {
		if i == 0 || len(r) < 5 {
			continue
		}
		wall, _ := strconv.ParseInt(r[0], 10, 64)
		seq, _ := strconv.ParseUint(r[2], 10, 64)
		out = append(out, Marker{WallNS: wall, Seq: seq, Kind: r[3], Label: r[4]})
	}
	return out, nil
}
