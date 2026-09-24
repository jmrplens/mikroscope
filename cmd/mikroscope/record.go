package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/jmrplens/mikroscope/internal/chart"
	"github.com/jmrplens/mikroscope/internal/record"
	rosapi "github.com/jmrplens/mikroscope/internal/rosapi"
	"github.com/jmrplens/mikroscope/internal/transport"
)

// recordOptions are the flags of record, mark and plot.
type recordOptions struct {
	prefix     string
	forDur     time.Duration
	fromStart  bool
	transport  string
	logMarkers bool
	poll       time.Duration
	batch      int
	topics     string
	tz         string
	title      string
	in         string
	out        string
	apiAddr    string
	apiUser    string
	apiPass    string
}

func parseRecordFlags(verb string, args []string, c *cli, fs *flag.FlagSet) (recordOptions, []string, error) {
	var ro recordOptions
	if fs == nil {
		fs = flag.NewFlagSet("mikroscope "+verb, flag.ContinueOnError)
	}
	fs.StringVar(&ro.prefix, "out", "", "output prefix: <out>.jsonl, .csv, .markers.csv, .meta.json (default capture-<UTC time>)")
	fs.DurationVar(&ro.forDur, "for", 0, "record for this long, then stop (0 = until Ctrl-C)")
	// Only record reads it. forward, mark and plot were offered it too, and
	// forward's --help listed it while internal/forward always starts at the
	// agent's newest sample: a flag accepted and ignored. Unregistered, it is
	// refused as an unknown flag instead.
	if verb == "record" {
		fs.BoolVar(&ro.fromStart, "from-start", false, "backfill everything the agent's ring holds before going live")
	}
	fs.DurationVar(&ro.poll, "poll", 500*time.Millisecond, "how often to pull the agent's ring")
	fs.IntVar(&ro.batch, "batch", 0, fmt.Sprintf("samples per pull (0 = twice what one --poll interval produces at the agent's rate, at least 20; the relay caps a pull at %d). The ring is drained until a pull comes back short", transport.RelayMaxBatch()))
	fs.StringVar(&ro.transport, "transport", "auto", "auto, direct (HTTP to the veth) or relay (/tool fetch over the RouterOS API)")
	fs.BoolVar(&ro.logMarkers, "log-markers", false, "record: after recording, pull the router log over the API and add matching lines as markers; mark: add the log lines of the recording's window")
	fs.StringVar(&ro.topics, "topics", "system,interface,container", "log topics to keep as markers (--log-markers); add firewall or script when their lines are the story")
	fs.StringVar(&ro.tz, "router-tz", "Local", "IANA zone the router's clock shows (log times carry no zone)")
	fs.StringVar(&ro.title, "title", "", "plot: chart title (default the prefix)")
	fs.StringVar(&ro.in, "in", "", "plot: recording prefix or .jsonl path")
	fs.StringVar(&ro.out, "svg", "", "plot: output SVG (default <in>.svg)")
	fs.StringVar(&ro.apiAddr, "api", env("API_ADDR", ""), "RouterOS API host:port for relay and --log-markers (MIKROSCOPE_API_ADDR)")
	fs.StringVar(&ro.apiUser, "api-user", env("API_USER", ""), "API user (MIKROSCOPE_API_USER)")
	ro.apiPass = env("API_PASSWORD", "")
	fs.StringVar(&c.opts.Token, "token", env("TOKEN", ""), "agent bearer token (MIKROSCOPE_TOKEN)")
	fs.IntVar(&c.opts.Port, "port", c.opts.Port, "agent HTTP port")
	fs.StringVar(&c.opts.Subnet, "subnet", env("SUBNET", c.opts.Subnet), "the agent's /30 (its address is .2)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: mikroscope %s [flags] [text…]\n", verb)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return ro, nil, err
	}
	if err := c.opts.Finish(); err != nil {
		return ro, nil, err
	}
	if ro.prefix == "" {
		ro.prefix = "capture-" + time.Now().UTC().Format("20060102T150405Z")
	}
	return ro, fs.Args(), nil
}

func (ro recordOptions) apiClient(ctx context.Context) (*rosapi.Client, error) {
	if ro.apiAddr == "" || ro.apiUser == "" {
		return nil, errors.New("the RouterOS API needs --api, --api-user and MIKROSCOPE_API_PASSWORD")
	}
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c, err := rosapi.DialContext(dialCtx, ro.apiAddr, ro.apiUser, ro.apiPass)
	if err != nil {
		return nil, fmt.Errorf("api %s: %w", ro.apiAddr, err)
	}
	c.SetCommandTimeout(15 * time.Second)
	return c, nil
}

// choosePuller implements the auto order: direct, then relay, else an error
// that names --expose.
func choosePuller(ctx context.Context, ro recordOptions, c cli) (transport.Puller, func(), error) {
	base := "http://" + net.JoinHostPort(c.opts.ContainerIP, strconv.Itoa(c.opts.Port))
	noop := func() {}
	if ro.transport == "auto" || ro.transport == "direct" {
		d := transport.NewDirect(base, c.opts.Token)
		if _, err := d.Health(ctx); err == nil {
			return d, noop, nil
		} else if ro.transport == "direct" {
			return nil, noop, fmt.Errorf("direct transport: %w", err)
		}
	}
	api, err := ro.apiClient(ctx)
	if err != nil {
		return nil, noop, fmt.Errorf("direct transport did not answer and the relay is not configured: %w (or `install --expose`)", err)
	}
	r := &transport.Relay{Fetcher: transport.APIFetcher{Client: api}, Base: base}
	if _, healthErr := r.Health(ctx); healthErr != nil {
		_ = api.Close()
		return nil, noop, fmt.Errorf("relay transport: %w", healthErr)
	}
	return r, func() { _ = api.Close() }, nil
}

func runRecord(args []string, c cli) error {
	ro, _, err := parseRecordFlags("record", args, &c, nil)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	puller, closer, err := choosePuller(ctx, ro, c)
	if err != nil {
		return err
	}
	defer closer()
	started := time.Now()
	var notes *os.File
	if term.IsTerminal(int(os.Stdin.Fd())) {
		notes = os.Stdin // a terminal: lines typed become markers
		fmt.Fprintln(os.Stderr, "type a line and press Enter to add a marker; Ctrl-C stops")
	}
	rc := &record.Recorder{Puller: puller, Opts: record.Options{Prefix: ro.prefix, For: ro.forDur, FromStart: ro.fromStart, Poll: ro.poll, Batch: ro.batch}, Log: func(s string) { fmt.Fprintln(os.Stderr, s) }}
	if notes != nil {
		rc.Notes = notes
	}
	sum, err := rc.Run(ctx)
	if err != nil {
		return err
	}
	if ro.logMarkers {
		if n, logErr := addLogMarkers(ro, started, time.Now(), sum.SkewNS); logErr != nil {
			fmt.Fprintln(os.Stderr, "log markers:", logErr)
		} else {
			sum.Markers += n
		}
	}
	fmt.Printf("recorded %d samples (seq %d..%d), %d gap(s), %d marker(s), via %s\n", sum.Samples, sum.FirstSeq, sum.LastSeq, len(sum.Gaps), sum.Markers, sum.Transport)
	for _, g := range sum.Gaps {
		fmt.Printf("  gap: samples %d..%d were no longer in the agent's ring\n", g.From, g.To)
	}
	for _, f := range sum.Files {
		fmt.Println("  " + f)
	}
	return nil
}

// addLogMarkers adds the router log lines of the window just recorded.
func addLogMarkers(ro recordOptions, from, to time.Time, skewNS int64) (int, error) {
	ms, err := fetchLogMarkers(ro, from.Add(time.Duration(skewNS)), to.Add(time.Duration(skewNS)))
	if err != nil {
		return 0, err
	}
	// AppendMarkers, not the recorder: Run has closed its files by now, and
	// writing through the recorder's own csv.Writer put these rows nowhere at
	// all — silently, because a write to a closed file through a flushed
	// bufio writer reports its error only to the writer. This is the same path
	// `mark` uses from another shell.
	if appendErr := record.AppendMarkers(ro.prefix, ms); appendErr != nil {
		return 0, appendErr
	}
	return len(ms), nil
}

func runMark(args []string, c cli) error {
	ro, rest, err := parseRecordFlags("mark", args, &c, nil)
	if err != nil {
		return err
	}
	meta, err := record.ReadMeta(ro.prefix)
	if err != nil {
		return fmt.Errorf("mark needs a recording's --out prefix: %w", err)
	}
	if ro.logMarkers {
		return markFromLog(ro, meta)
	}
	label := strings.TrimSpace(strings.Join(rest, " "))
	if label == "" {
		return errors.New("mark needs the text of the marker, or --log-markers")
	}
	m := record.Marker{WallNS: time.Now().UnixNano() + meta.SkewNS, Kind: "note", Label: label}
	if appendErr := record.AppendMarkers(ro.prefix, []record.Marker{m}); appendErr != nil {
		return appendErr
	}
	fmt.Printf("marker added to %s.markers.csv at %s\n", ro.prefix, time.Unix(0, m.WallNS).UTC().Format(time.RFC3339Nano))
	return nil
}

// markFromLog adds the router log lines of a finished recording's window:
// from the recording's start to its last sample, both in the agent's clock.
func markFromLog(ro recordOptions, meta record.Meta) error {
	samples, err := record.ReadJSONL(ro.prefix + ".jsonl")
	if err != nil {
		return err
	}
	from, err := time.Parse(time.RFC3339, meta.StartedUTC)
	if err != nil {
		return fmt.Errorf("meta started_utc: %w", err)
	}
	from = from.Add(time.Duration(meta.SkewNS))
	to := time.Unix(0, samples[len(samples)-1].WallNS)
	ms, err := fetchLogMarkers(ro, from, to)
	if err != nil {
		return err
	}
	if appendErr := record.AppendMarkers(ro.prefix, ms); appendErr != nil {
		return appendErr
	}
	fmt.Printf("%d log marker(s) added to %s.markers.csv (window %s → %s, topics %s)\n", len(ms), ro.prefix, from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339), ro.topics)
	return nil
}

// fetchLogMarkers pulls /log/print over the API and keeps the lines inside
// [from, to] (agent clock) whose topics match.
func fetchLogMarkers(ro recordOptions, from, to time.Time) ([]record.Marker, error) {
	api, err := ro.apiClient(context.Background())
	if err != nil {
		return nil, err
	}
	defer api.Close()
	loc, err := time.LoadLocation(ro.tz)
	if err != nil {
		return nil, fmt.Errorf("--router-tz: %w", err)
	}
	// The RB5009 held 66 217 log rows (dns to disk); ask the router for the
	// window only, in its own local time, and for the three fields used.
	reply, err := api.RunArgs([]string{"/log/print", "=.proplist=time,topics,message", "?>time=" + from.In(loc).Add(-time.Second).Format(record.APITimeLayout)})
	if err != nil {
		return nil, err
	}
	entries := make([]record.LogEntry, 0, len(reply.Re))
	for _, sen := range reply.Re {
		entries = append(entries, record.LogEntry{Time: sen.Map["time"], Topics: sen.Map["topics"], Message: sen.Map["message"]})
	}
	var wanted []string
	for t := range strings.SplitSeq(ro.topics, ",") {
		if t = strings.TrimSpace(t); t != "" {
			wanted = append(wanted, t)
		}
	}
	return record.LogMarkers(entries, from, to, wanted, loc), nil
}

func runPlot(args []string, c cli) error {
	ro, _, err := parseRecordFlags("plot", args, &c, nil)
	if err != nil {
		return err
	}
	if ro.in == "" {
		return errors.New("plot needs --in <recording prefix>")
	}
	prefix := strings.TrimSuffix(ro.in, ".jsonl")
	samples, err := record.ReadJSONL(prefix + ".jsonl")
	if err != nil {
		return err
	}
	var marks []chart.Marker
	if ms, mErr := record.ReadMarkers(prefix + ".markers.csv"); mErr == nil {
		for _, m := range ms {
			marks = append(marks, chart.Marker{WallNS: m.WallNS, Kind: m.Kind, Label: m.Label})
		}
	}
	title := ro.title
	if title == "" {
		title = prefix
	}
	out := ro.out
	if out == "" {
		out = prefix + ".svg"
	}
	if writeErr := os.WriteFile(out, []byte(chart.Render(samples, marks, title)), 0o600); writeErr != nil { // #nosec G304 -- the operator's own path
		return writeErr
	}
	fmt.Printf("%s: %d samples, %d markers\n", out, len(samples), len(marks))
	return nil
}
