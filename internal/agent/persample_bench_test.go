package agent

import (
	"path/filepath"
	"testing"

	"github.com/jmrplens/mikroscope/internal/sample"
)

// benchSource reads the captured RB5009 /proc tree, so the per-sample cost
// below is measured over the real shape of that device — four cores, its
// interrupt lines, its slab table — rather than over an invented one. The
// values do not change between reads, which the folding does not care about:
// it walks the same slices either way.
func benchSource(b *testing.B) (sample.Raw, sample.Raw) {
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
	return prev, cur
}

// BenchmarkPerSample splits the three things a tick does, so the project's
// headline cost — 2 685 µs a sample at 10 Hz on the RB5009, from
// site/src/data/measurements.ts — has a decomposition rather than one number.
// It runs on the development machine over the captured tree, so it says where
// the work is, not what an A72 charges for it.
//
// The agent does exactly these three: read the sources, fold the pair into a
// delta, push the encoded line into the ring. Rendering a Prometheus
// exposition is not among them since 1.0.5, which removed it from the agent;
// its cost is benchmarked where it now lives, in internal/expo.
//
// It does NOT reproduce the 27 µs / 239 allocations of
// site/src/content/docs/cost/index.mdx. That figure is a different boundary —
// "the seven global /proc files plus one delta" — while `read` here walks the
// whole captured tree and `delta` is the fold alone. The two are not
// comparable, and neither supersedes the other.
func BenchmarkPerSample(b *testing.B) {
	prev, cur := benchSource(b)

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
