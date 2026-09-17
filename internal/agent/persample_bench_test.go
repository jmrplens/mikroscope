package agent

import (
	"io"
	"path/filepath"
	"testing"

	"github.com/jmrplens/mikroscope/internal/sample"
)

// benchSource reads the captured RB5009 /proc tree, so the per-sample cost
// below is measured over the real shape of that device — four cores, its
// interrupt lines, its slab table — rather than over an invented one. The
// values do not change between reads, which the folding does not care about:
// it walks the same slices either way.
func benchSource(b *testing.B) (*ProcSource, sample.Raw, sample.Raw) {
	b.Helper()
	root := filepath.Join("..", "..", "testdata", "proc", "rb5009")
	src, err := NewProcSource(root, filepath.Join(root, "class"), nil)
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
	return src, prev, cur
}

// BenchmarkPerSample splits the work one tick does, so the question "what
// does the Prometheus exposition cost the agent when nothing scrapes it" has
// a number rather than an opinion. Totals.Add is exposition-only: it folds
// every source into the cumulative counters, histograms and trailing windows
// /metrics renders from, and it runs on every tick whether or not anybody
// ever reads them.
func BenchmarkPerSample(b *testing.B) {
	_, prev, cur := benchSource(b)

	b.Run("read", func(b *testing.B) {
		root := filepath.Join("..", "..", "testdata", "proc", "rb5009")
		src, err := NewProcSource(root, filepath.Join(root, "class"), nil)
		if err != nil {
			b.Skip(err)
		}
		var raw sample.Raw
		b.ReportAllocs()
		for b.Loop() {
			_ = src.Read(&raw)
		}
	})

	b.Run("delta", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; b.Loop(); i++ {
			_ = sample.Delta(&prev, &cur, uint64(i+1), 8)
		}
	})

	b.Run("totals", func(b *testing.B) {
		smp := sample.Delta(&prev, &cur, 1, 8)
		tot := NewTotals()
		tot.SetRateHz(10)
		b.ReportAllocs()
		for i := 0; b.Loop(); i++ {
			smp.Seq = uint64(i + 1)
			tot.Add(smp)
			tot.AddTiming(smp.DtNS, 1_000_000, 2_000_000)
		}
	})

	// What one scrape costs, for the other half of the question: the folding
	// above runs every tick, this runs once per scrape.
	b.Run("render", func(b *testing.B) {
		smp := sample.Delta(&prev, &cur, 1, 8)
		tot := NewTotals()
		tot.SetRateHz(10)
		ring := NewRing(3000)
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

	b.Run("ring", func(b *testing.B) {
		smp := sample.Delta(&prev, &cur, 1, 8)
		ring := NewRing(3000)
		b.ReportAllocs()
		for i := 0; b.Loop(); i++ {
			smp.Seq = uint64(i + 1)
			_ = ring.Push(smp)
		}
	})
}
