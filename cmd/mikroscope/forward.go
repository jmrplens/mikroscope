package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jmrplens/mikroscope/internal/apitier"
	"github.com/jmrplens/mikroscope/internal/forward"
)

func runForward(args []string, c cli) error {
	var (
		ifaces                                                 string
		conntrackEvery, apiEvery, commentsEvery, countersEvery time.Duration
		noHealth                                               bool
		apiMode                                                string
		sf                                                     sinkFlags
	)
	fs := flag.NewFlagSet("mikroscope forward", flag.ContinueOnError)
	sf.register(fs)
	fs.StringVar(&ifaces, "interfaces", env("INTERFACES", ""), "API tier: comma-separated interfaces for monitor-traffic (MIKROSCOPE_INTERFACES)")
	fs.DurationVar(&apiEvery, "api-every", time.Second, "API tier cadence; 0 disables the API tier")
	fs.DurationVar(&conntrackEvery, "conntrack-every", 0, "API tier: ask the conntrack count this often (0 = never; it is a table scan)")
	fs.DurationVar(&countersEvery, "counters-every", 10*time.Second, "API tier: read every port's cumulative counters (typed errors, fast-path split, link-downs, frame sizes) this often; 0 = never")
	fs.DurationVar(&commentsEvery, "labels-every", 5*time.Minute, "API tier: re-read what each interface is (comment, type, interface lists, bridge) this often (0 = default 5m); it changes only when an operator edits the configuration")
	fs.BoolVar(&noHealth, "no-health", false, "API tier: skip /system/health")
	fs.StringVar(&apiMode, "api-mode", "full", "API tier preset: off (agent only), slow (what the agent cannot read, at 10 s), or full (everything, for experiments)")
	ro, _, err := parseRecordFlags("forward", args, &c, fs)
	if err != nil {
		return err
	}
	if modeErr := applyAPIMode(apiMode, fs, &apiEvery, &conntrackEvery, &noHealth); modeErr != nil {
		return modeErr
	}
	if !sf.any() {
		return errors.New("forward needs at least one sink: --file, --prom, --influx, --loki, --otlp, --graphite, --elastic, --sql, --telegraf or --stdout")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logf := func(s string) { fmt.Fprintln(os.Stderr, s) }
	puller, closer, err := choosePuller(ctx, ro, c)
	if err != nil {
		return err
	}
	defer closer()
	out, err := sf.build(ctx, promHistogramRateHz, logf)
	if err != nil {
		return err
	}
	for _, s := range out {
		logf("sink: " + s.Name())
	}
	fw := &forward.Forwarder{Puller: puller, Sinks: out, Log: logf, Opts: forward.Options{For: ro.forDur, APIEvery: apiEvery, Poll: ro.poll, Batch: ro.batch}}
	if apiEvery > 0 {
		if closeAPI := attachAPITier(ctx, fw, ro, ifaces, apiEvery, conntrackEvery, commentsEvery, countersEvery, noHealth, logf); closeAPI != nil {
			defer closeAPI()
		}
	}
	st, err := fw.Run(ctx)
	closeAll(out, logf)
	fmt.Printf("forwarded %d kernel samples, %d api samples, %d gap(s), %d skew jump(s)\n", st.Kernel, st.API, st.Gaps, st.SkewJumps)
	for _, s := range out {
		ss := s.Stats()
		fmt.Printf("  %s: %d written, %d dropped, %d errors\n", s.Name(), ss.Written, ss.Dropped, ss.Errors)
	}
	return err
}

// applyAPIMode turns the --api-mode preset into the individual API-tier settings,
// leaving any of them that the operator set explicitly alone.
//
// The presets exist because what the 2026-09-12 discovery run found readable
// from inside a container on the reference RB5009 (RouterOS 7.24.2, kernel
// 5.6.3 arm64) moved most of the API tier's job into the agent:
//
//   - `/system/health` is redundant: the two thermal zones are readable from
//     the container, at the sampler's own rate instead of once a second.
//   - `/system/resource` and `/system/resource/cpu` were always redundant —
//     they are RouterOS's one-second average of the same /proc/stat jiffies
//     the agent already differences at 10 Hz.
//   - the conntrack count is redundant under privileged=yes: the global slab
//     allocator's nf_conntrack cache is the same population, and reading a
//     file beats a table scan over the API.
//
// What is left is `monitor-traffic`: per-interface bytes and packets live in
// the router's network namespace, and privileged=yes does not open it —
// measured on the same device and date, /proc/net/dev and /sys/class/net
// inside a privileged container still show only lo and the veth.
// That is the one thing `off` gives up, and `slow` keeps.
func applyAPIMode(mode string, fs *flag.FlagSet, apiEvery, conntrackEvery *time.Duration, noHealth *bool) error {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	assign := func(name string, apply func()) {
		if !set[name] {
			apply()
		}
	}
	switch mode {
	case "full":
		// The flag defaults already are `full`; nothing to impose.
	case "off":
		assign("api-every", func() { *apiEvery = 0 })
	case "slow":
		// Only what the agent genuinely cannot read, and slowly: interface
		// rates do not need a one-second cadence to be useful in a dashboard.
		assign("api-every", func() { *apiEvery = 10 * time.Second })
		assign("no-health", func() { *noHealth = true })
		assign("conntrack-every", func() { *conntrackEvery = 0 })
	default:
		return fmt.Errorf("--api-mode must be off, slow or full, got %q", mode)
	}
	return nil
}

// attachAPITier builds the API reader and hangs it on the forwarder. It
// returns the client's closer, or nil when the tier could not be opened —
// a warning, not a failure: the kernel tier is the point, and since the
// 2026-09-12 discovery the API tier only adds per-interface traffic.
func attachAPITier(ctx context.Context, fw *forward.Forwarder, ro recordOptions, ifaces string,
	apiEvery, conntrackEvery, commentsEvery, countersEvery time.Duration, noHealth bool, logf func(string),
) func() {
	// A failed first dial is a warning, not a disabled tier: the reader
	// carries a Redial, so a router that is rebooting when the collector
	// starts is connected on the first round it answers rather than never.
	api, apiErr := ro.apiClient(ctx)
	if apiErr != nil {
		logf("api tier: not connected yet, will keep trying: " + apiErr.Error())
	}
	var list []string
	for i := range strings.SplitSeq(ifaces, ",") {
		if i = strings.TrimSpace(i); i != "" {
			list = append(list, i)
		}
	}
	reader := &apitier.Reader{Opts: apitier.Options{Interfaces: list, Health: !noHealth, ConntrackEvery: conntrackEvery, LabelsEvery: commentsEvery, CountersEvery: countersEvery}}
	if apiErr == nil {
		reader.Client = api
	}
	reader.Redial = func(ctx context.Context) (apitier.Client, error) { return ro.apiClient(ctx) }
	fw.API = reader
	logf(fmt.Sprintf("api tier: every %s, interfaces %v, health %v, conntrack every %s, port counters every %s", apiEvery, list, !noHealth, conntrackEvery, countersEvery))
	return func() {
		if closer, ok := reader.Client.(io.Closer); ok {
			_ = closer.Close()
		}
	}
}
