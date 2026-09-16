//go:build dockere2e

package docker

import "testing"

// TestFileSinkIsUsableAsTheOracle checks the one sink that is not a store:
// every other test in this package compares what a store holds against this
// file, so a file that is wrong would make the rest of the suite agree with
// itself and prove nothing.
func TestFileSinkIsUsableAsTheOracle(t *testing.T) {
	s := Sweep(t)
	samples := s.Samples(t)
	if len(samples) < 50 {
		t.Fatalf("the run produced %d samples in %s, which is too few to compare a store against", len(samples), sweepFor)
	}
	dev := s.Device(t)
	for _, key := range []string{"board", "kernel", "cores", "ports"} {
		if _, ok := dev[key]; !ok {
			t.Errorf("the device record has no %q, and the sinks carry it", key)
		}
	}
	// Sequence numbers are what every store's row count is compared against,
	// so they have to be present and strictly increasing.
	var last float64 = -1
	for i, sm := range samples {
		seq, ok := sm["seq"].(float64)
		if !ok {
			t.Fatalf("sample %d has no numeric seq", i)
		}
		if seq <= last {
			t.Fatalf("sample %d has seq %v after %v: the oracle is not ordered", i, seq, last)
		}
		last = seq
	}
	if len(s.Events(t)) == 0 {
		t.Error("the run carried no kernel-log records, so the Loki and Elasticsearch tests would assert nothing")
	}
}
