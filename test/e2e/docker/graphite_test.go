//go:build dockere2e

package docker

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"testing"
	"time"
)

// TestGraphite reads the plaintext points back out of carbon. Carbon answers
// nothing at all: it takes a line over TCP and either files it or drops it,
// silently, when the point is older than the longest archive in
// storage-schemas.conf, when the metric name is one whisper cannot make a
// path of, or when it exceeds MAX_CREATES_PER_MINUTE. Every one of those is
// invisible to the sink, and all of them are visible here.
func TestGraphite(t *testing.T) {
	s := Sweep(t)
	ctx := t.Context()
	root := graphitePfx + "." + sweepHostTag
	start, end := s.Window()

	render := func(target string) ([][2]any, error) {
		q := url.Values{
			"target": {target},
			"from":   {strconv.FormatInt(start.Unix(), 10)},
			"until":  {strconv.FormatInt(end.Unix(), 10)},
			"format": {"json"},
		}
		var out []struct {
			Target     string   `json:"target"`
			Datapoints [][2]any `json:"datapoints"`
			Tags       any      `json:"tags"`
		}
		if err := httpJSON(ctx, "GET", "http://"+s.stack.GraphiteWeb+"/render?"+q.Encode(), "", nil, &out); err != nil {
			return nil, err
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("graphite knows no series %s", target)
		}
		return out[0].Datapoints, nil
	}

	// The branches the sink writes. metrics/find is how a Grafana query
	// browses them, and a missing branch is a whole source carbon dropped.
	var found []struct {
		ID   string `json:"id"`
		Leaf int    `json:"leaf"`
	}
	if err := WaitUntil(ctx, "graphite to hold this run's tree", 2*time.Minute, func(ctx context.Context) error {
		found = found[:0]
		if err := httpJSON(ctx, "GET",
			"http://"+s.stack.GraphiteWeb+"/metrics/find?query="+url.QueryEscape(root+".*"), "", nil, &found); err != nil {
			return err
		}
		have := map[string]bool{}
		for _, f := range found {
			have[f.ID] = true
		}
		for _, want := range []string{"cpu", "mem", "stat", "load", "softnet", "irq", "psi", "self"} {
			if !have[root+"."+want] {
				return fmt.Errorf("%s.%s is not in the tree", root, want)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Logf("graphite holds %d branches under %s", len(found), root)

	// The values. Carbon aggregates to the archive's resolution — 1 s here,
	// against a 10 Hz run — so a point is an average of the samples in its
	// second and cannot be compared one for one. What must hold is that every
	// stored point is inside the range the file sink recorded, and that a
	// counter's points rise.
	lo, hi := fieldRange(t, s, "ctxt")
	points, err := render(root + ".stat.ctxt")
	if err != nil {
		t.Fatal(err)
	}
	vals := datapointValues(t, points)
	if len(vals) < 5 {
		t.Fatalf("graphite stored %d points for %s.stat.ctxt over a %s run", len(vals), root, sweepFor)
	}
	assertRising(t, vals, lo, hi)
	t.Logf("%d points for %s.stat.ctxt, %v..%v", len(vals), root, vals[0], vals[len(vals)-1])

	// A per-core path, because that is where a sink that flattens a label
	// into a path gets it wrong.
	if _, coreErr := render(root + ".cpu.0.busy_ratio"); coreErr != nil {
		t.Errorf("per-core series: %v", coreErr)
	}
}

// datapointValues drops the nulls carbon returns for the archive slots the run
// did not fill, and keeps the values in time order.
func datapointValues(tb testing.TB, points [][2]any) []float64 {
	tb.Helper()
	var vals []float64
	for _, p := range points {
		if p[0] == nil {
			continue
		}
		v, ok := p[0].(float64)
		if !ok {
			tb.Fatalf("graphite returned %T as a value", p[0])
		}
		vals = append(vals, v)
	}
	return vals
}

// assertRising is what a stored kernel counter has to satisfy: inside the
// range the file sink recorded, and never falling. Carbon aggregates to the
// archive's resolution — 1 s here, against a 10 Hz run — so a point is an
// average of the samples in its second and cannot be compared one for one.
func assertRising(tb testing.TB, vals []float64, lo, hi float64) {
	tb.Helper()
	for i, v := range vals {
		if v < lo || v > hi {
			tb.Errorf("point %d is %v, outside the %v..%v the file sink recorded", i, v, lo, hi)
		}
		if i > 0 && v < vals[i-1] {
			tb.Errorf("point %d is %v after %v: a kernel counter does not fall", i, v, vals[i-1])
		}
	}
}

// fieldRange is the range of a numeric field across the run, from the oracle.
func fieldRange(tb testing.TB, s *sweep, field string) (lo, hi float64) {
	tb.Helper()
	lo, hi = 1<<62, -1
	for _, sm := range s.Samples(tb) {
		v, ok := sm[field].(float64)
		if !ok {
			continue
		}
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	if hi < 0 {
		tb.Fatalf("no sample carries %q", field)
	}
	return lo, hi
}
