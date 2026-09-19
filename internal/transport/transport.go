// Package transport reaches the agent's HTTP from the operator host. Two
// ways: Direct, plain HTTP to the veth address when the router forwards
// LAN → veth (measured on the reference RB5009, RouterOS 7.24.2, kernel
// 5.6.3 arm64, 2026-09-11: ping 0.24–0.55 ms, 0 % loss, HTTP 200 in 2 ms,
// once the veth is up — it is up only while its container runs); and Relay,
// `/tool fetch output=user` executed on the router over the binary API, for
// routers that do not. The relay is bounded by what the same device
// measured: the reply truncates silently at 64 512 bytes, and per-call
// latency is bimodal — n=40 at 1 Hz gave min 3.2 ms, median 1 001.5 ms,
// p90 1 003.7 ms, max 1 012.7 ms, with 22 of 40 calls at or above 900 ms.
// Both pull the same `/snapshot?since=&max=` so record and forward do not
// care which one they got.
package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/jmrplens/mikroscope/internal/agent"
	rosapi "github.com/jmrplens/mikroscope/internal/rosapi"
)

// Health is the agent's /healthz.
type Health struct {
	OK               bool    `json:"ok"`
	Seq              uint64  `json:"seq"`
	OldestSeq        uint64  `json:"oldest_seq"`
	WallNS           int64   `json:"wall_ns"`
	MonoNS           int64   `json:"mono_ns"`
	UptimeS          float64 `json:"uptime_s"`
	RateHz           int     `json:"rate_hz"`
	Slipped          uint64  `json:"slipped"`
	CapabilitiesHash string  `json:"capabilities_hash"`
	Version          string  `json:"version"`
}

// Gap is the agent's gap line: samples From..To are gone from the ring.
type Gap struct {
	From, To uint64
}

// Trigger is the agent's {"trigger":…} marker line: a capture condition
// fired on sample Seq (agent/capture.go). It is a line kind of its own, like
// the gap line, and Pull returns it among the lines in sequence order; the
// forwarder tells the two apart by prefix (IsTrigger).
type Trigger struct {
	ID        uint64  `json:"id"`
	Cause     string  `json:"cause"`
	Field     string  `json:"field,omitempty"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
	Seq       uint64  `json:"seq"`
	WallNS    int64   `json:"wall_ns"`
}

// triggerPrefix is how a marker line starts.
var triggerPrefix = []byte(`{"trigger":`)

// ParseTrigger decodes a marker line, or returns false for any other line.
func ParseTrigger(line []byte) (Trigger, bool) {
	if !bytes.HasPrefix(line, triggerPrefix) {
		return Trigger{}, false
	}
	var t struct {
		Trigger Trigger `json:"trigger"`
	}
	if err := json.Unmarshal(line, &t); err != nil {
		return Trigger{}, false
	}
	return t.Trigger, true
}

// CapabilityFetcher is the optional side of a Puller that can read the
// agent's /capabilities: the board facts the collector hands every sink as
// a device event (the device-info stream). Both real transports have it;
// test fakes need not.
type CapabilityFetcher interface {
	// Capabilities returns the agent's /capabilities.
	Capabilities(ctx context.Context) (agent.Capabilities, error)
}

// SamplerStatsFetcher is the optional side of a Puller that can read the
// agent's /sampler: what only the agent can count about itself — ticks taken,
// ticks slipped, and what the trigger evaluator has fired, suppressed,
// refused and is holding. The collector reads it on its health cadence and
// hands it to every sink, which is what keeps those figures from being
// readable through a Prometheus scrape alone.
type SamplerStatsFetcher interface {
	// SamplerStats returns the agent's /sampler.
	SamplerStats(ctx context.Context) (agent.SamplerStats, error)
}

// Puller fetches samples in order.
type Puller interface {
	// Health returns /healthz.
	Health(ctx context.Context) (Health, error)
	// Pull returns up to limit NDJSON lines after seq, oldest first, and the
	// gap the agent reported, if any. Lines are newline-free.
	Pull(ctx context.Context, since uint64, limit int) (lines [][]byte, gap *Gap, err error)
	// Name says which transport this is, for the operator.
	Name() string
}

// splitBody turns an NDJSON body into lines and pulls out the gap line.
func splitBody(body []byte) ([][]byte, *Gap, error) {
	var lines [][]byte
	var gap *Gap
	for l := range bytes.SplitSeq(bytes.TrimSpace(body), []byte{'\n'}) {
		if len(l) == 0 || l[0] == '#' {
			continue
		}
		if bytes.HasPrefix(l, []byte(`{"gap":`)) {
			var g struct {
				Gap Gap `json:"gap"`
			}
			if err := json.Unmarshal(l, &g); err != nil {
				return nil, nil, fmt.Errorf("gap line %q: %w", l, err)
			}
			gap = &g.Gap
			continue
		}
		lines = append(lines, l)
	}
	return lines, gap, nil
}

// Direct is HTTP to the agent's address.
type Direct struct {
	Base   string // http://172.30.10.2:9123
	Token  string
	Client *http.Client
}

// NewDirect makes a Direct with a bounded client.
func NewDirect(base, token string) *Direct {
	return &Direct{Base: base, Token: token, Client: &http.Client{Timeout: 10 * time.Second}}
}

// Name implements Puller.
func (d *Direct) Name() string { return "direct " + d.Base }

func (d *Direct) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.Base+path, http.NoBody)
	if err != nil {
		return nil, err
	}
	if d.Token != "" {
		req.Header.Set("Authorization", "Bearer "+d.Token)
	}
	resp, err := d.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s: %s", path, resp.Status, bytes.TrimSpace(body))
	}
	return body, nil
}

// Health implements Puller.
func (d *Direct) Health(ctx context.Context) (Health, error) {
	body, err := d.get(ctx, "/healthz")
	if err != nil {
		return Health{}, err
	}
	var h Health
	if decErr := json.Unmarshal(body, &h); decErr != nil {
		return Health{}, fmt.Errorf("healthz: %w", decErr)
	}
	return h, nil
}

// Capabilities implements CapabilityFetcher.
func (d *Direct) Capabilities(ctx context.Context) (agent.Capabilities, error) {
	body, err := d.get(ctx, "/capabilities")
	if err != nil {
		return agent.Capabilities{}, err
	}
	var c agent.Capabilities
	if decErr := json.Unmarshal(body, &c); decErr != nil {
		return agent.Capabilities{}, fmt.Errorf("capabilities: %w", decErr)
	}
	return c, nil
}

// SamplerStats implements SamplerStatsFetcher.
func (d *Direct) SamplerStats(ctx context.Context) (agent.SamplerStats, error) {
	body, err := d.get(ctx, "/sampler")
	if err != nil {
		return agent.SamplerStats{}, err
	}
	var st agent.SamplerStats
	if decErr := json.Unmarshal(body, &st); decErr != nil {
		return agent.SamplerStats{}, fmt.Errorf("sampler: %w", decErr)
	}
	return st, nil
}

// Pull implements Puller.
func (d *Direct) Pull(ctx context.Context, since uint64, limit int) ([][]byte, *Gap, error) {
	body, err := d.get(ctx, "/snapshot?since="+strconv.FormatUint(since, 10)+"&max="+strconv.Itoa(limit))
	if err != nil {
		return nil, nil, err
	}
	return splitBody(body)
}

// Fetcher runs `/tool fetch` on the router; the binary API client satisfies
// it, and tests substitute a fake.
type Fetcher interface {
	// Fetch returns the body `/tool fetch url=… output=user` produced.
	Fetch(ctx context.Context, url string) (string, error)
}

// APIFetcher is the Fetcher over the vendored RouterOS API client. The user
// needs the `test` policy: on the reference RB5009 (RouterOS 7.24.2,
// 2026-09-11) a user with policy `read,api` is refused `/tool fetch` with
// "not enough permissions (9)", and `read,api,test` works.
type APIFetcher struct{ Client *rosapi.Client }

// Fetch implements Fetcher.
func (a APIFetcher) Fetch(ctx context.Context, url string) (string, error) {
	reply, err := a.Client.RunArgsContext(ctx, []string{"/tool/fetch", "=url=" + url, "=output=user"})
	if err != nil {
		return "", err
	}
	for _, sen := range slices.Backward(reply.Re) {
		if data, ok := sen.Map["data"]; ok {
			return data, nil
		}
	}
	return "", errors.New("fetch returned no data")
}

// RelayMax is the largest reply `/tool fetch output=user` returns intact;
// anything longer is truncated silently (64 512 bytes on 7.24.2).
const RelayMax = 64512

// Relay pulls through the router. The agent bounds each reply with max and
// the relay caps max at relayMaxBatch, which is computed from RelayMax and
// the mean line so the two cannot drift apart again: see relayMaxBatch.
type Relay struct {
	Fetcher Fetcher
	Base    string // http://<veth ip>:port as the router sees it
	Token   string // unused: the router-side fetch carries no header; a token
	// requires the direct transport or a token-free deployment.
}

// Name implements Puller.
func (r *Relay) Name() string { return "relay via /tool fetch to " + r.Base }

// Health implements Puller.
func (r *Relay) Health(ctx context.Context) (Health, error) {
	data, err := r.Fetcher.Fetch(ctx, r.Base+"/healthz")
	if err != nil {
		return Health{}, err
	}
	var h Health
	if decErr := json.Unmarshal([]byte(data), &h); decErr != nil {
		return Health{}, fmt.Errorf("healthz via relay: %w", decErr)
	}
	return h, nil
}

// Capabilities implements CapabilityFetcher.
func (r *Relay) Capabilities(ctx context.Context) (agent.Capabilities, error) {
	data, err := r.Fetcher.Fetch(ctx, r.Base+"/capabilities")
	if err != nil {
		return agent.Capabilities{}, err
	}
	var c agent.Capabilities
	if decErr := json.Unmarshal([]byte(data), &c); decErr != nil {
		return agent.Capabilities{}, fmt.Errorf("capabilities via relay: %w", decErr)
	}
	return c, nil
}

// SamplerStats implements SamplerStatsFetcher.
func (r *Relay) SamplerStats(ctx context.Context) (agent.SamplerStats, error) {
	data, err := r.Fetcher.Fetch(ctx, r.Base+"/sampler")
	if err != nil {
		return agent.SamplerStats{}, err
	}
	var st agent.SamplerStats
	if decErr := json.Unmarshal([]byte(data), &st); decErr != nil {
		return agent.SamplerStats{}, fmt.Errorf("sampler via relay: %w", decErr)
	}
	return st, nil
}

// relayHeadroomPercent is how much more than the mean line the cap allows
// for, because a mean is not a maximum: a batch of above-average lines has to
// fit too. 134 % is the margin the first cap happened to have — 30 lines of
// the ~1.6 kB line of Phase 3 is 48 kB against RelayMax's 64 512 — and it is
// kept because nothing has measured a better one.
const relayHeadroomPercent = 134

// relayMaxBatch is the most lines one relayed pull asks for, computed rather
// than chosen so it cannot fall out of step with either number it depends on.
//
// It was the literal 30 until 2026-09-15, sized when a line was ~1.6 kB. The
// discovery sources took the line to 2 560 B, so 30 of them is about 73 kB —
// over the 64 512-byte cap, and a relayed pull that filled its batch got the
// "relay reply hit the fetch limit" error instead of data. A full batch is
// only asked for when the ring has that much to give, which at a 500 ms poll
// means BatchFor asks for min(rate, cap): the error needed an agent above
// ~27 Hz, so the default 10 Hz install never saw it and the 50 and 100 Hz runs
// of 2026-09-15 were pulled directly, not relayed.
//
// Computing it is what keeps it honest. agent.ApproxLineBytes went to 3 456 B
// on 2026-09-17 and the cap fell from 18 to 13 with no edit here — while six
// documentation pages went on saying 18 until the 2026-09-19 audit, which is
// why site/src/data/measurements.ts now carries the cap behind a build-time
// assertion that recomputes it from the same line size.
//
// The cost of the smaller cap is more round trips through `/tool fetch` at
// high rates, and a fetch is the expensive part of the relay.
const relayMaxBatch = RelayMax * 100 / (agent.ApproxLineBytes * relayHeadroomPercent)

// RelayMaxBatch is the relay's per-pull cap as a package-level value, for the
// CLI's own help text: the flag that documents the cap has to read it rather
// than repeat it, which is how it came to advertise 30 after the cap had
// already moved.
func RelayMaxBatch() int { return relayMaxBatch }

// MaxBatch reports the relay's per-pull cap, so a puller draining the ring
// knows when a reply was short because the ring was, not because of the cap.
func (r *Relay) MaxBatch() int { return relayMaxBatch }

// EffectiveBatch is the batch a pull can actually return: want, or the
// transport's own cap when it has one (the relay's RelayMaxBatch).
func EffectiveBatch(p Puller, want int) int {
	if c, ok := p.(interface{ MaxBatch() int }); ok {
		return min(want, c.MaxBatch())
	}
	return want
}

// BatchFor sizes a batch from the agent's rate and the poll interval: what
// one interval produces, twice over, and never under 20. Until 2026-09-15
// both forward and record used a constant 20 at a 500 ms poll — a 40 Hz
// ceiling that lost exactly 1 − 40/50 = 19.7 % of a 50 Hz overnight run
// (RESEARCH-scrape.md part 3.3), one sample at a time, with a gap event
// per pull to say so.
func BatchFor(rateHz int, poll time.Duration) int {
	if rateHz < 1 || poll <= 0 {
		return 20
	}
	return max(20, int(math.Ceil(float64(rateHz)*poll.Seconds()*2)))
}

// Pull implements Puller. limit is capped at relayMaxBatch.
func (r *Relay) Pull(ctx context.Context, since uint64, limit int) ([][]byte, *Gap, error) {
	limit = min(limit, relayMaxBatch)
	data, err := r.Fetcher.Fetch(ctx, r.Base+"/snapshot?since="+strconv.FormatUint(since, 10)+"&max="+strconv.Itoa(limit))
	if err != nil {
		return nil, nil, err
	}
	if len(data) >= RelayMax {
		return nil, nil, fmt.Errorf("relay reply hit the %d-byte fetch limit; lower the batch", RelayMax)
	}
	return splitBody([]byte(data))
}
