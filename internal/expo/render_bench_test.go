package expo

import (
	"io"
	"path/filepath"
	"testing"

	"github.com/jmrplens/mikroscope/internal/agent"
	"github.com/jmrplens/mikroscope/internal/sample"
)

// benchSample folds one delta out of the captured RB5009 /proc tree, so the
// costs below are measured over that device's real shape — four cores, its
// interrupt lines, its slab table — rather than over an invented one.
func benchSample(b *testing.B) sample.Sample {
	b.Helper()
	root := filepath.Join("..", "..", "testdata", "proc", "rb5009")
	src, err := agent.NewProcSource(root, filepath.Join(root, "class"), nil)
	if err != nil {
		b.Skipf("no captured tree: %v", err)
	}
	var prev, cur sample.Raw
	if readErr := src.Read(&prev); readErr != nil {
		b.Skipf("read: %v", readErr)
	}
	if readErr := src.Read(&cur); readErr != nil {
		b.Skipf("read: %v", readErr)
	}
	return sample.Delta(&prev, &cur, 1, 8)
}

// BenchmarkExposition is the other half of the per-sample question, and since
// 1.0.5 it is the COLLECTOR's half: the agent serves no exposition, so folding
// a sample into the cumulative counters and rendering them are costs
// `mikroscope forward --prom` pays, not costs the router pays.
//
// The two differ in how often they run, which is the point of measuring them
// apart: `add` runs once per sample the collector receives — ten times a
// second at the install default — and `render` once per scrape.
func BenchmarkExposition(b *testing.B) {
	smp := benchSample(b)

	b.Run("add", func(b *testing.B) {
		tot := NewTotals()
		tot.SetRateHz(10)
		b.ReportAllocs()
		for i := 0; b.Loop(); i++ {
			smp.Seq = uint64(i + 1)
			tot.Add(smp)
			tot.AddTiming(smp.DtNS, 1_000_000, 2_000_000)
		}
	})

	b.Run("render", func(b *testing.B) {
		tot := NewTotals()
		tot.SetRateHz(10)
		ring := agent.NewRing(3000)
		for i := range 600 {
			smp.Seq = uint64(i + 1)
			tot.Add(smp)
			_ = ring.Push(smp)
		}
		b.ReportAllocs()
		for b.Loop() {
			tot.Render(io.Discard, Exposition{Ring: ring, RateHz: 10, Sampler: true})
		}
	})
}
