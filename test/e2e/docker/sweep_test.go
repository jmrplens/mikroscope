//go:build dockere2e

package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/test/e2e/fakeagent"
)

// One forward, every sink, once per test binary. Each store is then asked
// about the same run, and the file sink's JSONL is the oracle they are
// compared against rather than a table typed by hand here.
const (
	sweepFor      = 12 * time.Second
	sweepDatabase = "mikroscope"
)

// Every run names itself, and every store is asked only about this run. The
// stack is reusable on purpose — `make e2e-docker-up` then a targeted
// `go test -run` is the debugging workflow — and a reused store still holds
// the last run, whose rows would otherwise be counted as this one's. The
// identifier is the host tag, so it travels as a tag, a label, a path
// component and a document field without any sink being told about it.
var (
	runID        = strconv.FormatInt(time.Now().UnixNano()%1e9, 36)
	sweepHostTag = "e2e-rb5009-" + runID
	sweepIndex   = "mikroscope-e2e-" + runID // lowercase: Elasticsearch refuses anything else
	graphitePfx  = "mikroscope_e2e"
	promJob      = "mikroscope-" + runID
)

type sweep struct {
	stack *Stack
	// File is the JSONL the file sink wrote: the oracle.
	File string
	// SQL is the .sql script the SQL sink wrote, loaded into Postgres by the
	// Postgres test rather than by the sweep.
	SQL string
	// Log is the collector's combined output, kept because a sink that could
	// not write still exits zero.
	Log string
	// Exposition is the last body the collector's own /metrics served, read
	// by this process while the run was live. Prometheus scraped the same
	// endpoint over the same window, so this is the oracle its stored series
	// are compared against — the file sink cannot play that part, because the
	// exporter aggregates rather than streams.
	Exposition string
	// Bin is the collector binary this package built, reused by the tests
	// that drive a subcommand rather than a sink.
	Bin string
	// PromAddr is the address the collector's own /metrics served on.
	PromAddr string
	// Start and End bracket the run on the host's clock. Every range query in
	// the suite is asked over this window, widened, rather than over "now
	// minus something": a store that kept the points but placed them
	// elsewhere in time is a bug the widest window would hide.
	Start, End time.Time
}

// Window is the run's window with a margin either side, for a range query.
func (s *sweep) Window() (start, end time.Time) {
	return s.Start.Add(-2 * time.Minute), s.End.Add(2 * time.Minute)
}

// Samples are the oracle's rows that are samples: the ones carrying a
// sequence number, as opposed to the device, trigger, derived, detection and
// gap records the same file interleaves.
func (s *sweep) Samples(tb testing.TB) []map[string]any {
	tb.Helper()
	var out []map[string]any
	for _, row := range s.Oracle(tb) {
		if _, ok := row["seq"]; ok {
			out = append(out, row)
		}
	}
	if len(out) == 0 {
		tb.Fatal("the oracle has no samples in it")
	}
	return out
}

// Events are the kernel-log records the run carried, flattened out of the
// samples that held them. Loki is the sink that is asked for these.
func (s *sweep) Events(tb testing.TB) []map[string]any {
	tb.Helper()
	var out []map[string]any
	for _, row := range s.Oracle(tb) {
		list, ok := row["events"].([]any)
		if !ok {
			continue
		}
		for _, e := range list {
			if ev, isMap := e.(map[string]any); isMap {
				out = append(out, ev)
			}
		}
	}
	return out
}

// Rows is how many rows a per-sample table should hold for this run: the
// samples that carry the field at all, counting a per-core array once per
// element. Not every source is in every sample — the agent reads the slower
// files on a subset of ticks — so a table's row count is a property of the
// run, never of its length.
func (s *sweep) Rows(tb testing.TB, field string) int {
	tb.Helper()
	n := 0
	for _, sm := range s.Samples(tb) {
		switch v := sm[field].(type) {
		case nil:
		case []any:
			n += len(v)
		default:
			n++
		}
	}
	if n == 0 {
		tb.Fatalf("no sample in the run carries %q, so a store holding none of it would pass", field)
	}
	return n
}

// Device is the oracle's one device record: the inventory every sink is
// expected to carry through in its own shape.
func (s *sweep) Device(tb testing.TB) map[string]any {
	tb.Helper()
	for _, row := range s.Oracle(tb) {
		if dev, ok := row["device"].(map[string]any); ok {
			return dev
		}
	}
	tb.Fatal("the oracle carries no device record")
	return nil
}

var (
	sweepOnce sync.Once
	sweepVal  *sweep
	errSweep  error
)

// Sweep runs the collector once against a fake agent, with every sink pointed
// at the stack, and returns what it wrote. Subsequent callers get the same run.
func Sweep(tb testing.TB) *sweep {
	tb.Helper()
	stack := Start(tb)
	sweepOnce.Do(func() { sweepVal, errSweep = runSweep(context.Background(), tb, stack) })
	if errSweep != nil {
		tb.Fatalf("sweep: %v", errSweep)
	}
	return sweepVal
}

func runSweep(ctx context.Context, tb testing.TB, stack *Stack) (*sweep, error) {
	tb.Helper()
	root, err := moduleRoot(ctx)
	if err != nil {
		return nil, err
	}
	outDir := filepath.Join(root, "test", "e2e", "docker", "out", "collector")
	if mkErr := os.MkdirAll(outDir, 0o750); mkErr != nil {
		return nil, mkErr
	}
	bin := filepath.Join(outDir, "mikroscope")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/mikroscope")
	build.Dir = root
	if out, buildErr := build.CombinedOutput(); buildErr != nil {
		return nil, fmt.Errorf("go build: %w\n%s", buildErr, out)
	}

	// The collector derives the agent's address from its /30 — the .2 end —
	// so the fake has to answer there rather than on 127.0.0.1. This /30 is
	// this package's alone; the in-process suite hands out 127.1.x.0/30.
	const subnet, agentIP = "127.60.60.0/30", "127.60.60.2"
	agentPort, err := freePortOn(ctx, agentIP)
	if err != nil {
		return nil, err
	}
	agent, err := fakeagent.New(ctx, fmt.Sprintf("%s:%d", agentIP, agentPort), "")
	if err != nil {
		return nil, fmt.Errorf("fake agent: %w", err)
	}
	defer agent.Close()

	promPort, err := freePort(ctx)
	if err != nil {
		return nil, err
	}
	s := &sweep{
		stack:    stack,
		Bin:      bin,
		File:     filepath.Join(outDir, "sweep.jsonl"),
		SQL:      filepath.Join(outDir, "sweep.sql"),
		PromAddr: fmt.Sprintf("127.0.0.1:%d", promPort),
	}

	args := []string{
		"forward",
		"--subnet", subnet,
		"--port", strconv.Itoa(agentPort),
		"--transport", "direct",
		"--for", sweepFor.String(),
		"--api-mode", "off",
		"--host-tag", sweepHostTag,
		"--file", s.File,
		"--sql", s.SQL,
		"--prom", s.PromAddr,
		"--influx", "http://" + stack.InfluxDB + "/api/v3/write_lp?db=" + sweepDatabase + "&precision=nanosecond",
		"--loki", "http://" + stack.Loki + "/loki/api/v1/push",
		"--otlp", "http://" + stack.OTLP + "/v1/metrics",
		"--elastic", "http://" + stack.Elasticsearch,
		"--elastic-index", sweepIndex,
		"--graphite", stack.GraphitePlain,
		"--graphite-prefix", graphitePfx,
		"--telegraf", "http://" + stack.Telegraf + "/telegraf",
	}
	runCtx, cancel := context.WithTimeout(ctx, sweepFor+2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(runCtx, bin, args...)
	cmd.Dir = root
	// No proxy and no credential anywhere near this.
	cmd.Env = append(os.Environ(), "HTTP_PROXY=", "HTTPS_PROXY=", "NO_PROXY=*")

	// Prometheus is told where to scrape before the collector starts serving,
	// so the first scrape lands inside the run rather than after it.
	if tgtErr := writePromTarget(stack, s.PromAddr); tgtErr != nil {
		return nil, tgtErr
	}

	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	s.Start = time.Now()
	started := s.Start
	if startErr := cmd.Start(); startErr != nil {
		return nil, fmt.Errorf("starting the collector: %w", startErr)
	}
	// Read the exporter while it is still up: after the run it is gone, and
	// what it served is the only honest oracle for what Prometheus stored.
	scrapeCtx, stopScrape := context.WithCancel(runCtx)
	scraped := make(chan string, 1)
	go func() { scraped <- pollExporter(scrapeCtx, s.PromAddr) }()
	runErr := cmd.Wait()
	s.End = time.Now()
	stopScrape()
	s.Exposition = <-scraped
	out := buf.Bytes()
	s.Log = string(out)
	if logErr := os.WriteFile(filepath.Join(outDir, "sweep.log"), out, 0o600); logErr != nil {
		return nil, logErr
	}
	if runErr != nil {
		return nil, fmt.Errorf("forward: %w\n%s", runErr, tail(s.Log))
	}
	tb.Logf("forward ran for %s; %d bytes of log", time.Since(started).Round(time.Second), len(s.Log))
	if strings.Contains(s.Log, "errors") {
		tb.Logf("collector summary:\n%s", tail(s.Log))
	}
	return s, nil
}

// pollExporter keeps the newest body /metrics served, until the run ends. A
// single well-timed read would be a race with the collector's first sample.
func pollExporter(ctx context.Context, addr string) string {
	var last string
	for ctx.Err() == nil {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/metrics", nil)
		if err != nil {
			return last
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
			_ = resp.Body.Close()
			if readErr == nil && resp.StatusCode == http.StatusOK {
				last = string(body)
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(500 * time.Millisecond):
		}
	}
	return last
}

// writePromTarget hands Prometheus the collector's address through the file_sd
// directory it watches.
func writePromTarget(stack *Stack, addr string) error {
	body, err := json.Marshal([]map[string]any{{
		"targets": []string{addr},
		"labels":  map[string]string{"job": promJob},
	}})
	if err != nil {
		return err
	}
	// World-readable, deliberately: the Prometheus image runs as nobody, and a
	// 0600 file in a bind mount is a job with no targets at all. The reason is
	// only in Prometheus's own log ("Error reading file … permission denied"),
	// never in anything the suite can see. The Chmod is not redundant:
	// WriteFile applies its mode when it creates the file and leaves the mode
	// of an existing one alone, so without it the first 0600 file written here
	// would keep breaking every later run.
	path := filepath.Join(stack.PromTargets, "mikroscope.json")
	if writeErr := os.WriteFile(path, body, 0o644); writeErr != nil { // #nosec G306 -- scratch target file under test/
		return writeErr
	}
	return os.Chmod(path, 0o644) // #nosec G302 -- as above
}

// Oracle reads the file sink's JSONL, the run every store is compared against.
func (s *sweep) Oracle(tb testing.TB) []map[string]any {
	tb.Helper()
	body, err := os.ReadFile(s.File)
	if err != nil {
		tb.Fatalf("the file sink wrote nothing: %v", err)
	}
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(string(body)), "\n") {
		if line == "" {
			continue
		}
		var row map[string]any
		if jsonErr := json.Unmarshal([]byte(line), &row); jsonErr != nil {
			tb.Fatalf("the file sink wrote a line that is not JSON: %v", jsonErr)
		}
		out = append(out, row)
	}
	if len(out) == 0 {
		tb.Fatal("the file sink wrote no rows, so there is nothing to compare a store against")
	}
	return out
}

func moduleRoot(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "go", "env", "GOMOD").Output()
	if err != nil {
		return "", err
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull {
		return "", errors.New("no module root")
	}
	return filepath.Dir(gomod), nil
}

func tail(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > 15 {
		lines = lines[len(lines)-15:]
	}
	return strings.Join(lines, "\n")
}

// sameInt64s fails unless the two multisets are equal, sorting both first: a
// store's read-back order is its own business, and what the suite is asking is
// whether the values that went in are the values that are there.
func sameInt64s(tb testing.TB, what string, got, want []int64) {
	tb.Helper()
	if len(want) == 0 {
		tb.Fatalf("%s: the oracle carries no values to compare against", what)
	}
	if len(got) != len(want) {
		tb.Fatalf("%s: the store holds %d values, the run produced %d", what, len(got), len(want))
	}
	g := slices.Clone(got)
	w := slices.Clone(want)
	slices.Sort(g)
	slices.Sort(w)
	for i := range w {
		if g[i] != w[i] {
			tb.Fatalf("%s: value %d is %d, the file sink recorded %d", what, i, g[i], w[i])
		}
	}
}

// Int64s is a numeric field of every sample, as the oracle recorded it: the
// multiset each store's read-back is compared against.
func (s *sweep) Int64s(tb testing.TB, field string) []int64 {
	tb.Helper()
	var out []int64
	for _, sm := range s.Samples(tb) {
		if v, ok := sm[field].(float64); ok {
			out = append(out, int64(v))
		}
	}
	return out
}
