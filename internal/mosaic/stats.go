package mosaic

// Small robust-statistics helpers shared by the pair fits, the graph islands and the seam metric.

import (
	"math"
	"sort"
)

// sortedF64 returns the float32 samples as a sorted float64 slice (the base for quantile lookups).
func sortedF64(vals []float32) []float64 {
	out := make([]float64, len(vals))
	for i, v := range vals {
		out[i] = float64(v)
	}
	sort.Float64s(out)
	return out
}

// quantileSorted reads the p-th percentile (linear interpolation, numpy-style) from a pre-sorted
// slice.
func quantileSorted(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[n-1]
	}
	rank := p / 100 * float64(n-1)
	lo := int(rank)
	if lo+1 >= n {
		return sorted[n-1]
	}
	return sorted[lo] + (rank-float64(lo))*(sorted[lo+1]-sorted[lo])
}

// medianF64 returns the median of vals (sorts a copy; 0 for empty input).
func medianF64(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	buf := append([]float64(nil), vals...)
	sort.Float64s(buf)
	return quantileSorted(buf, 50)
}

// madSigmaF64 is the Gaussian-equivalent sigma from the median absolute deviation about med.
func madSigmaF64(vals []float64, med float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	dev := make([]float64, len(vals))
	for i, v := range vals {
		dev[i] = math.Abs(v - med)
	}
	return 1.4826 * medianF64(dev)
}
