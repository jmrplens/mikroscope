package sample

import (
	"sort"
	"time"
)

// WindowBusy is the busy ratio of one core over a run of samples: the sum
// of its busy ticks over the ticks the summed intervals could hold. Summing
// deltas is exact, so a 1 s window from ten 100 ms samples equals the
// direct 1 s computation.
func WindowBusy(samples []Sample, core int) float64 {
	var busy uint64
	var dt int64
	for i := range samples {
		if core < len(samples[i].CPU) {
			busy += samples[i].CPU[core].Busy()
			dt += samples[i].DtNS
		}
	}
	if dt <= 0 {
		return 0
	}
	return float64(busy) / (float64(dt) / 1e9 * 100)
}

// Trailing returns the suffix of samples whose MonoNS lies within d of the
// last one. samples must be in sequence order.
func Trailing(samples []Sample, d time.Duration) []Sample {
	if len(samples) == 0 {
		return nil
	}
	end := samples[len(samples)-1].MonoNS
	start := end - int64(d)
	i := len(samples) - 1
	for i > 0 && samples[i-1].MonoNS > start {
		i--
	}
	return samples[i:]
}

// WindowStats are per-sample busy-ratio statistics of one core over a run.
type WindowStats struct {
	Min, Max, Mean, P95 float64
	N                   int
}

// Stats computes WindowStats for one core.
func Stats(samples []Sample, core int) WindowStats {
	ratios := make([]float64, 0, len(samples))
	for i := range samples {
		if core < len(samples[i].CPU) {
			ratios = append(ratios, samples[i].CPU[core].BusyRatio(samples[i].DtNS))
		}
	}
	return StatsOf(ratios)
}

// StatsOf computes WindowStats over per-sample ratios. P95 is the
// nearest-rank percentile: with 10 Hz samples over 60 s that is the 570th of
// 600 sorted values. ratios is sorted in place.
func StatsOf(ratios []float64) WindowStats {
	if len(ratios) == 0 {
		return WindowStats{}
	}
	sort.Float64s(ratios)
	var sum float64
	for _, r := range ratios {
		sum += r
	}
	rank := (len(ratios)*95 + 99) / 100 // ceil(0.95 n), nearest-rank
	return WindowStats{
		Min: ratios[0], Max: ratios[len(ratios)-1], Mean: sum / float64(len(ratios)),
		P95: ratios[rank-1], N: len(ratios),
	}
}
