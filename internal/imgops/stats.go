// Package imgops holds the pure image-math primitives shared across the engine — percentiles,
// separable blurs, median filtering, and binary morphology (dilation, connected-component
// labeling). They are the NumPy/SciPy equivalents several recipes rely on (nightscape colour
// grading, optics defect maps, photometric normalization). Keeping one implementation here avoids
// the copy-paste the house Go conventions forbid; nightscape's numeric.go delegates to these.
//
// Every function is pure: it allocates its own output and never mutates its inputs (except the
// explicitly documented in-place box helpers, which are unexported).
package imgops

import (
	"math"
	"sort"
)

// Percentile returns the linear-interpolated p-th percentile (0..100) of vals, matching numpy's
// default ("linear" interpolation). vals is not modified. An empty slice returns 0.
func Percentile(vals []float32, p float64) float64 {
	n := len(vals)
	if n == 0 {
		return 0
	}
	buf := make([]float64, n)
	for i, v := range vals {
		buf[i] = float64(v)
	}
	sort.Float64s(buf)
	if p <= 0 {
		return buf[0]
	}
	if p >= 100 {
		return buf[n-1]
	}
	rank := p / 100 * float64(n-1)
	lo := int(math.Floor(rank))
	frac := rank - float64(lo)
	if lo+1 >= n {
		return buf[n-1]
	}
	return buf[lo] + frac*(buf[lo+1]-buf[lo])
}

// Subsample returns at most n evenly-spaced samples of p, for fast percentile/MAD estimates on
// large planes. When len(p) <= n it returns p itself (no copy).
func Subsample(p []float32, n int) []float32 {
	if n <= 0 || len(p) <= n {
		return p
	}
	step := len(p) / n
	out := make([]float32, 0, n+1)
	for i := 0; i < len(p); i += step {
		out = append(out, p[i])
	}
	return out
}
