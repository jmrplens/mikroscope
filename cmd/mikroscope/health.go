package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/jmrplens/mikroscope/internal/health"
	"github.com/jmrplens/mikroscope/internal/router"
	"github.com/jmrplens/mikroscope/internal/sample"
	"github.com/jmrplens/mikroscope/internal/transport"
)

// healthTimeout bounds the whole agent half of doctor. Doctor is what an
// operator runs when something is wrong, and an agent that is not installed,
// or not reachable from this host, must cost a few seconds, not a hang.
const healthTimeout = 3 * time.Second

// ringMax is the most samples one snapshot request may ask the agent for.
const ringMax = 10000

// doctorHealth reads the agent's ring and prints what it shows. It never
// fails doctor: prerequisites decide the exit status, and what the running
// agent sees is advice. An agent this host cannot reach is said so, once.
func doctorHealth(ctx context.Context, w io.Writer, host string, port int, token string) {
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	fmt.Fprintln(w, "health (what the running agent's ring shows now):")
	h, _, err := router.Probe(ctx, host, port, healthTimeout)
	if err != nil {
		fmt.Fprintf(w, "  skipped: no agent answered at %s from this host (%v)\n", net.JoinHostPort(host, strconv.Itoa(port)), err)
		return
	}
	since := uint64(0)
	if h.OldestSeq > 0 {
		since = h.OldestSeq - 1
	}
	lines, _, err := transport.NewDirect("http://"+net.JoinHostPort(host, strconv.Itoa(port)), token).Pull(ctx, since, ringMax)
	if err != nil {
		fmt.Fprintf(w, "  skipped: the agent answered /healthz but its ring could not be read (%v)\n", err)
		return
	}
	samples := make([]sample.Sample, 0, len(lines))
	for _, l := range lines {
		if bytes.HasPrefix(l, []byte(`{"trigger":`)) {
			continue // a capture marker, not a sample
		}
		var s sample.Sample
		if json.Unmarshal(l, &s) == nil {
			samples = append(samples, s)
		}
	}
	printHealth(w, health.Analyze(h.Board, samples))
}

func printHealth(w io.Writer, rep health.Report) {
	fmt.Fprintf(w, "  read %d samples covering %.0f s\n", rep.Samples, rep.Seconds)
	if len(rep.Findings) == 0 {
		fmt.Fprintln(w, "  ok    no loop signature, STP churn, link flap or softnet drop in the window")
		return
	}
	for _, f := range rep.Findings {
		fmt.Fprintf(w, "  WARN  %s: %s\n", f.Check, f.Detail)
		fmt.Fprintf(w, "        fix: %s\n", f.Fix)
	}
}
