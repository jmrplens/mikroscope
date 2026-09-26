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
// written first, then the steps UpgradeRead found missing, in the plan's
// order, then the container step is removed (waiting for the asynchronous
// removal) and created again; the network objects stay. Writing the manifest
// every time is what UpgradeListing lists, and it gives an install made
// before the manifest existed one and keeps it consistent with the plan the
// upgrade leaves; UpgradeRead has already refused a file at its path that is
// not this install's. A missing step — a membership since lost — is made
// whole by the same upgrade that lists it. It returns nothing until the new
// agent answers.
func Upgrade(r Runner, o Options, missing []Step, image []byte, w io.Writer) error {
	plan := Plan(o)
	m, c := plan[0], plan[len(plan)-1]
	if err := createStep(r, o, m, nil, w); err != nil {
		return err
	}
	fmt.Fprintf(w, "  wrote %s\n", m.Name)
	for _, s := range missing {
		if s.Name == c.Name || s.Name == m.Name {
			continue
		}
		if err := createStep(r, o, s, nil, w); err != nil {
			return err
		}
		fmt.Fprintf(w, "  new   %s\n", s.Name)
	}
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

// MissingListing prints the steps upgrade creates besides the container, as
// the plan writes them, after UpgradeListing and before the confirmation.
func MissingListing(o Options, missing []Step, w io.Writer) {
	if len(missing) == 0 {
		return
	}
	fmt.Fprintf(w, "  also:    %d step(s) of the plan are missing on the router, and upgrade creates them first\n", len(missing))
	for _, s := range missing {
		fmt.Fprintf(w, "      %s\n      %s\n", s.Name, mask(s.Create, o.Token))
	}
}

// UpgradeState is what upgrade reads before it writes anything.
type UpgradeState struct {
	// Installed is true when the router holds this install's container.
	Installed bool
	// Missing are the other steps of the plan the router does not hold,
	// which upgrade creates (Upgrade, MissingListing): neither the
	// container, which it replaces anyway, nor the manifest, which it
	// always writes.
	Missing []Step
	// Arch is the router's architecture-name, for --arch auto; empty when
	// the router did not say.
	Arch string
}

// UpgradeRead is what upgrade asks the router before it writes anything,
// in one connect: the install's shape (ReadShape), whether every step of
// the plan is owned, the router's architecture, and, with --remote-image, the
// two /container/config answers doctor's credential check reads.
//
// adopt, when the router holds the install, turns its shape into the
// options upgrade goes on with (the CLI fills the flags that were not given
// and refuses the ones that contradict it). The ownership counts were asked
// for the options as given; when adopting the shape changed what the plan
// reads, they are asked again for the new plan, and that is the one case
// that takes a second connect.
//
// upgrade runs no doctor, and it removes the old container before the router
// pulls the new image, so a pull that fails leaves the router without an
// agent. When the install is there, this prints that credential check and,
// when registry-url names a host other than the one the pull goes to, a note
// with the reference that keeps that host (registryURLNote), so the operator
// reads both before the confirmation. It prints nothing for a tar upgrade or
// when the install is not there, and writes nothing.
func UpgradeRead(r Runner, o Options, adopt func(Shape) (Options, error), w io.Writer) (UpgradeState, Options, error) {
	qs := shapeQueries(o)
	qs = append(qs, query{key: qArch, text: `:put [/system/resource/get architecture-name]`})
	if o.UsesRemoteImage() {
		qs = append(qs, query{key: qRegistryURL, text: registryURLQuery}, query{key: qRegistryUser, text: registryUserQuery})
	}
	plan := Plan(o)
	qs = append(qs, stateQueries(plan)...)
	a, stray, err := readKeyed(r, qs)
	if err != nil {
		return UpgradeState{}, o, err
	}
	st := UpgradeState{Installed: true}
	if arch, ok := a[qArch]; ok {
		st.Arch = arch
	}
	registryURL, userSet := a.get(qRegistryURL), isYes(a.get(qRegistryUser))
	if shape := parseShape(a, o.Name); adopt != nil && shape.Found {
		if o, err = adopt(shape); err != nil {
			return UpgradeState{}, o, err
		}
		if next := Plan(o); !sameQuestions(plan, next) {
			plan = next
			if a, stray, err = readKeyed(r, stateQueries(plan)); err != nil {
				return UpgradeState{}, o, err
			}
		}
	}
	var foreign []string
	st.Installed, st.Missing, foreign, err = classifySteps(plan, a, stray)
	if err != nil {
		return UpgradeState{}, o, err
	}
	if !st.Installed {
		return UpgradeState{Arch: st.Arch}, o, nil
	}
	if len(foreign) > 0 {
		return st, o, fmt.Errorf("%s exist on the router and were not created by mikroscope (no ownership tag), so upgrade would not recreate them; "+
			"remove them by hand if they are yours, or run upgrade with the flags the install was made with", strings.Join(foreign, ", "))
	}
	if o.UsesRemoteImage() {
		printCredentialCheck(o, registryURL, userSet, w)
	}
	return st, o, nil
}

// classifySteps reads the three answers stateQueries asked for each step of
// plan: installed is false when the container step is absent; missing are
// the other steps the router does not hold, but the manifest, which Upgrade
// writes every time, as the upgrade listing says; foreign are the steps
// something else holds.
func classifySteps(plan []Step, a answers, stray []string) (installed bool, missing []Step, foreign []string, err error) {
	installed = true
	for i, s := range plan {
		n := strconv.Itoa(i)
		present, okP := a["present."+n]
		check, okC := a["check."+n]
		owned, okO := a["owned."+n]
		if !okP || !okC || !okO {
			return false, nil, nil, fmt.Errorf("the router gave no answer about %s; it printed %q", s.Name, strings.Join(stray, " / "))
		}
		switch stateOf(s, present, check, owned) {
		case stateForeign:
			foreign = append(foreign, s.Name)
		case stateAbsent:
			switch i {
			case len(plan) - 1:
				installed = false
			case 0:
			default:
				missing = append(missing, s)
			}
		case stateOwned:
		}
	}
	return installed, missing, foreign, nil
}

// sameQuestions says whether two plans ask the router the same questions.
func sameQuestions(a, b []Step) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Owned != b[i].Owned || a[i].Check != b[i].Check || a[i].Present != b[i].Present {
			return false
		}
	}
	return true
}

// printCredentialCheck prints doctor's registry credential item and the
// registry-url note, for upgrade.
func printCredentialCheck(o Options, registryURL string, userSet bool, w io.Writer) {
	var rep Report
	addRegistryCredential(&rep, o, registryURL, userSet)
	for _, it := range rep.Items {
		it.print(w)
	}
	if note := registryURLNote(o, registryURL); note != "" {
		fmt.Fprintf(w, "  %-7s %s\n", "note", note)
	}
}
