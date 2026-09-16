package e2e

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGraphiteSinkWritesPlaintextOverTCP(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")
	rx := newLineListener(t)

	// No --graphite-prefix, so the default "mikroscope" is what is under
	// test, and no --host-tag either, so the default host node is too.
	p := startForward(t, a, 5*time.Second, "--graphite", rx.Addr())
	rx.Await(t, 200, 60*time.Second)
	p.Wait(t, 90*time.Second)
	lines := rx.Lines()

	for _, line := range lines {
		if err := graphiteLineProblem(line); err != nil {
			t.Errorf("%v", err)
			break
		}
	}

	// Graphite has no labels, so every dimension is a path node. The per-core
	// and per-IRQ nodes are what a dashboard selects on, and a sink that
	// flattened them would produce a tree that cannot be graphed per core.
	paths := map[string]bool{}
	for _, line := range lines {
		paths[strings.Fields(line)[0]] = true
	}
	for _, want := range []string{
		"mikroscope.router.cpu.0.user", "mikroscope.router.cpu.3.busy_ratio",
		"mikroscope.router.softnet.0.processed", "mikroscope.router.mem.total_kb",
		"mikroscope.router.load.load1", "mikroscope.router.sample.dt_ns",
		"mikroscope.router.self.rss_bytes",
		"mikroscope.router.slab.nf_conntrack.active_objs",
		// The thermal node is the zone's INDEX, not its name, on purpose: a
		// zone's `type` is not unique across zones and a firmware upgrade
		// that renamed one would silently fork the series
		// (internal/sinks/graphite.go). The leaf is `celsius`, the unit the
		// sink converts to once, beside `critical_celsius`.
		"mikroscope.router.thermal.0.celsius",
	} {
		if !paths[want] {
			t.Errorf("no %s path arrived; a sample of what did:\n%s", want, strings.Join(firstN(sortedKeys(paths), 15), "\n"))
		}
	}
	// The gap is the one collector-side event this sink carries.
	if !anyPathPrefix(paths, "mikroscope.router.collector.gap.") {
		t.Errorf("the sequence gap did not reach the tree")
	}
	// cpu.total is deliberately absent: Graphite's own sumSeries over
	// cpu.*.user gives it, and a second copy of the same ticks in the same
	// tree invites double counting (internal/sinks/graphite.go).
	if anyPathPrefix(paths, "mikroscope.router.cpu.total") {
		t.Errorf("cpu.total was emitted, which double-counts against cpu.*")
	}
	// Kernel-log markers are numbers nowhere, so they must not appear.
	if anyPathPrefix(paths, "mikroscope.router.kmsg") || anyPathPrefix(paths, "mikroscope.router.event") {
		t.Errorf("a kernel-log marker was turned into a metric path")
	}
}

func anyPathPrefix(paths map[string]bool, prefix string) bool {
	for p := range paths {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

func firstN(s []string, n int) []string {
	if len(s) < n {
		return s
	}
	return s[:n]
}

// graphiteLineProblem is the first thing wrong with one carbon plaintext
// line, or nil when it is a line a Graphite dashboard can use.
func graphiteLineProblem(line string) error {
	parts := strings.Fields(line)
	if len(parts) != 3 {
		return fmt.Errorf("%q is not <path> <value> <timestamp>", line)
	}
	path, value, stamp := parts[0], parts[1], parts[2]
	if !strings.HasPrefix(path, "mikroscope.") {
		return fmt.Errorf("path %q does not start with the default prefix", path)
	}
	nodes := strings.Split(path, ".")
	if len(nodes) < 4 {
		return fmt.Errorf("path %q has no room for prefix, host, measurement and field", path)
	}
	for _, n := range nodes {
		if n == "" {
			return fmt.Errorf("path %q has an empty node, which collapses two levels of the tree", path)
		}
		if strings.ContainsAny(n, " ,/\t") {
			return fmt.Errorf("path %q has a node that would split or nest: %q", path, n)
		}
	}
	if _, err := strconv.ParseFloat(value, 64); err != nil {
		return fmt.Errorf("%q: the value does not parse: %w", line, err)
	}
	// Whole Unix seconds, not the nanoseconds the line protocol uses:
	// whisper's finest retention is one second and anything else lands the
	// point tens of thousands of years away.
	sec, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		return fmt.Errorf("%q: the timestamp does not parse: %w", line, err)
	}
	if sec < 1_577_836_800 || sec > 4_102_444_800 {
		return fmt.Errorf("%q: timestamp %d is not Unix seconds in this century", line, sec)
	}
	return nil
}

func TestGraphiteSinkHonoursItsPrefixAndHostNodes(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")
	rx := newLineListener(t)

	p := startForward(t, a, 4*time.Second,
		"--graphite", rx.Addr(),
		"--graphite-prefix", "routers",
		"--host-tag", "rb5009-lab")
	rx.Await(t, 50, 60*time.Second)
	p.Wait(t, 90*time.Second)

	for _, line := range rx.Lines() {
		if !strings.HasPrefix(line, "routers.rb5009-lab.") {
			t.Fatalf("%q does not start with <prefix>.<host>.", line)
		}
	}
}

// TestGraphiteSinkSurvivesAListenerThatGoesAway: carbon restarting is
// ordinary, and the sink must reconnect rather than end the run.
func TestGraphiteSinkSurvivesAListenerThatGoesAway(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")
	rx := newLineListener(t)

	p := startForward(t, a, 6*time.Second, "--graphite", rx.Addr())
	rx.Await(t, 50, 60*time.Second)
	// The sink's backoff after a failed send is 2 s doubling to 60 s, so a
	// six-second run reconnects at most once; what is asserted is that the
	// collector finishes its run and reports, not that delivery resumed.
	p.Wait(t, 90*time.Second)
	out := p.Stdout() + p.Stderr()
	if !strings.Contains(out, "forwarded ") {
		t.Errorf("the collector did not finish its run:\n%s", out)
	}
}
