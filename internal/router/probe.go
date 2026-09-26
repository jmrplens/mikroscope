package router

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
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

// Upgrade replaces the container with a new image: the install manifest is
// written first, which gives an install made before it existed one, then the
// container step is removed (waiting for the asynchronous removal) and
// created again; the network objects stay. UpgradePreflight has already
// refused a file at the manifest's path that is not this install's. It
// returns nothing until the new agent answers.
func Upgrade(r Runner, o Options, image []byte, w io.Writer) error {
	plan := Plan(o)
	m, c := plan[0], plan[len(plan)-1]
	out, err := r.Run(m.Create)
	if err == nil && trimSpace(out) != "" {
		err = fmt.Errorf("router said %q", trimSpace(out))
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", m.Name, err)
	}
	fmt.Fprintf(w, "  wrote %s\n", m.Name)
	out, err = r.Run(c.Remove)
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

// UpgradeState is what upgrade reads before it writes anything.
type UpgradeState struct {
	// Installed is true when every step of the plan is this install's.
	Installed bool
	// Arch is the router's architecture-name, for --arch auto; empty when
	// the router did not say.
	Arch string
}

// UpgradeRead is what upgrade asks the router before it writes anything,
// in one connect: whether every step of this install is owned, the router's
// architecture, and, with --remote-image, the two /container/config answers
// doctor's credential check reads. upgrade runs no doctor, and it removes the
// old container before the router pulls the new image, so a pull that fails
// leaves the router without an agent. When the install is there, this
// prints that credential check and, when registry-url names a host other
// than the one the pull goes to, a note with the reference that keeps that
// host (registryURLNote), so the operator reads both before the
// confirmation. It prints nothing for a tar upgrade or when the install is
// not there, and writes nothing.
func UpgradeRead(r Runner, o Options, w io.Writer) (UpgradeState, error) {
	plan := Plan(o)
	qs := make([]query, 0, len(plan)+3)
	qs = append(qs, query{key: qArch, text: `:put [/system/resource/get architecture-name]`})
	for i, s := range plan {
		qs = append(qs, query{key: "owned." + strconv.Itoa(i), text: s.Owned})
	}
	if o.UsesRemoteImage() {
		qs = append(qs, query{key: qRegistryURL, text: registryURLQuery}, query{key: qRegistryUser, text: registryUserQuery})
	}
	a, stray, err := readKeyed(r, qs)
	if err != nil {
		return UpgradeState{}, err
	}
	st := UpgradeState{Installed: true}
	if a.has(qArch) {
		st.Arch = a.get(qArch)
	}
	for i, s := range plan {
		owned, ok := a["owned."+strconv.Itoa(i)]
		if !ok {
			return UpgradeState{}, fmt.Errorf("the router gave no answer to %q; it printed %q", s.Owned, strings.Join(stray, " / "))
		}
		if owned == "0" {
			st.Installed = false
		}
	}
	if !st.Installed || !o.UsesRemoteImage() {
		return st, nil
	}
	registryURL := a.get(qRegistryURL)
	var rep Report
	addRegistryCredential(&rep, o, registryURL, isYes(a.get(qRegistryUser)))
	for _, it := range rep.Items {
		it.print(w)
	}
	if note := registryURLNote(o, registryURL); note != "" {
		fmt.Fprintf(w, "  %-7s %s\n", "note", note)
	}
	return st, nil
}
