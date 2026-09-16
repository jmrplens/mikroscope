package router

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// Health is the subset of the agent's /healthz the CLI uses.
type Health struct {
	OK        bool    `json:"ok"`
	Seq       uint64  `json:"seq"`
	OldestSeq uint64  `json:"oldest_seq"`
	WallNS    int64   `json:"wall_ns"`
	UptimeS   float64 `json:"uptime_s"`
	RateHz    int     `json:"rate_hz"`
	Slipped   uint64  `json:"slipped"`
	Version   string  `json:"version"`
	Board     string  `json:"board,omitempty"`
}

// Probe is the reachability check: a TCP connect to the agent with a short
// timeout, then GET /healthz. It is what decides between the direct
// transport (plain HTTP to the veth) and the `/tool fetch` relay, and it
// must run after the container is started: a veth is `R` only while its
// container runs, so probing a stopped container's address finds an
// invalid address, no connected route, and the packets leave by the default
// route (measured on the reference RB5009, RouterOS 7.24.2, 2026-09-11).
func Probe(ctx context.Context, host string, port int, timeout time.Duration) (Health, time.Duration, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	start := time.Now()
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return Health{}, time.Since(start), fmt.Errorf("tcp connect to %s: %w", addr, err)
	}
	_ = conn.Close()
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", http.NoBody)
	if err != nil {
		return Health{}, time.Since(start), err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Health{}, time.Since(start), fmt.Errorf("GET /healthz: %w", err)
	}
	defer resp.Body.Close()
	var h Health
	if decErr := json.NewDecoder(resp.Body).Decode(&h); decErr != nil {
		return Health{}, time.Since(start), fmt.Errorf("decode /healthz: %w", decErr)
	}
	return h, time.Since(start), nil
}

// WaitReachable probes until the agent answers or the deadline passes.
func WaitReachable(ctx context.Context, host string, port int, deadline time.Duration) (Health, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	var lastErr error
	for {
		h, rtt, err := Probe(ctx, host, port, 2*time.Second)
		if err == nil {
			return h, rtt, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return Health{}, 0, lastErr
		case <-time.After(time.Second):
		}
	}
}

// Upgrade replaces the container with a new image: the container step is
// removed (waiting for the asynchronous removal) and created again; the
// network objects stay. It returns nothing until the new agent answers.
func Upgrade(r Runner, o Options, image []byte, w io.Writer) error {
	plan := Plan(o)
	c := plan[len(plan)-1]
	out, err := r.Run(c.Remove)
	if err != nil {
		return fmt.Errorf("remove old container: %w", err)
	}
	if msg := trimSpace(out); msg != "" {
		return fmt.Errorf("remove old container: router said %q", msg)
	}
	fmt.Fprintf(w, "  gone  %s\n", c.Name)
	if createErr := createStep(r, o, c, image, w); createErr != nil {
		return createErr
	}
	fmt.Fprintf(w, "  new   %s\n", c.Name)
	return nil
}
