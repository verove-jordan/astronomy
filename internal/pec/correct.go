package pec

import (
	"math"
	"sort"
)

// Turning a measured position error into the rates a mount replays.
//
// # Why the difference is taken at bin EDGES
//
// The mount holds one rate constant ACROSS each bin, so the correction it accumulates over bin b is
// c_b·T, and what that has to cancel is the error's change from the START of bin b to its end:
//
//	c_b = −( E(b+1) − E(b) ) / T_bin
//
// Differencing per-bin MEANS instead would be differencing values that sit at bin CENTRES, which
// introduces half a bin of phase error. That is harmless at the worm frequency (3.6 % residual) and
// ruinous higher up: the residual after a phase-shifted correction is 2A·sin(θ/2), which reaches 100 %
// — the correction achieving nothing — around harmonic B/3, and exceeds it beyond, at which point the
// mount tracks worse than with no table at all. Evaluating a fitted curve at edges sidesteps the
// question entirely, and is why Fit.Curve is defined on edges.
//
// A second property comes free: a Fourier series is periodic, so E(B) = E(0) exactly, and the rates
// therefore sum to zero. A table with a net rate makes the mount walk a little further every
// revolution, and that failure is designed out rather than corrected for.

// Correction turns a fitted curve into per-bin rate corrections in arcsec/s.
//
// indexOffsetBins shifts the curve against the mount's bin numbering. It is measured by the probe
// rather than assumed: a whole-bin error alone leaves about a quarter of the original amplitude
// behind.
func Correction(fit *Fit, g Geometry, indexOffsetBins float64) []float64 {
	if fit == nil || !g.valid() || len(fit.Curve) != g.Bins+1 {
		return nil
	}
	binSec := g.BinSec()
	rates := make([]float64, g.Bins)
	for b := 0; b < g.Bins; b++ {
		start := evalCurve(fit.Curve, g, float64(b)+indexOffsetBins)
		end := evalCurve(fit.Curve, g, float64(b+1)+indexOffsetBins)
		rates[b] = -(end - start) / binSec
	}
	return rates
}

// evalCurve reads the edge-sampled curve at any phase, interpolating within a bin.
func evalCurve(curve []float64, g Geometry, phase float64) float64 {
	p := wrapPhase(phase, g.Bins)
	i := int(p)
	if i >= g.Bins {
		i = g.Bins - 1
	}
	return curve[i] + (curve[i+1]-curve[i])*(p-float64(i))
}

// Quantised is a correction expressed in the mount's own table units.
type Quantised struct {
	Bins []int8
	// Clipped counts bins that hit the ±127 rail. On any sane mount this is zero — a 15″ worm needs
	// about 7 units of 127 — so a non-zero count means the fit has gone wrong, not that the mount is
	// unusually bad.
	Clipped int
	// MaxAbs is the largest unit written, for the same sanity check.
	MaxAbs int
}

// tableLimit is the usable range of a table entry. −128 is representable but deliberately unused: it
// makes the range asymmetric, and some firmware treats 0x80 specially.
const tableLimit = 127

// Quantise converts rate corrections into signed table units.
//
// Rounding each bin independently would be wrong in a way that is easy to miss: these are RATES, and
// their errors INTEGRATE into position. With a peak correction of only about seven units, independent
// rounding random-walks the accumulated position by a few tenths of an arcsecond across a revolution
// and leaves a net rate behind. Carrying the rounding residual forward instead — choosing each unit
// against the running total rather than in isolation — bounds the accumulated error at half a unit
// for every bin, and the final pass forces the net rate to exactly zero.
func Quantise(rates []float64, g Geometry) *Quantised {
	if !g.valid() || len(rates) != g.Bins {
		return nil
	}
	out := &Quantised{Bins: make([]int8, g.Bins)}
	residual := make([]float64, g.Bins)

	var acc float64
	for b, r := range rates {
		want := r/g.LSBArcsecPerSec + acc
		q := math.Round(want)
		switch {
		case q > tableLimit:
			q, out.Clipped = tableLimit, out.Clipped+1
			// Do not carry a clipped deficit forward: it is a correction the mount cannot make, and
			// feeding it back would drag every following bin off to chase something unreachable.
			acc = 0
		case q < -tableLimit:
			q, out.Clipped = -tableLimit, out.Clipped+1
			acc = 0
		default:
			acc = want - q
		}
		residual[b] = want - q
		out.Bins[b] = int8(q)
	}
	forceZeroSum(out, residual)

	for _, v := range out.Bins {
		if a := int(v); a > out.MaxAbs {
			out.MaxAbs = a
		} else if -a > out.MaxAbs {
			out.MaxAbs = -a
		}
	}
	return out
}

// forceZeroSum nudges individual bins until the table has no net rate.
//
// The bins adjusted are the ones whose rounding was most marginal — where a unit either way was
// nearly a coin toss — so the curve's shape is disturbed as little as possible.
func forceZeroSum(q *Quantised, residual []float64) {
	sum := 0
	for _, v := range q.Bins {
		sum += int(v)
	}
	if sum == 0 {
		return
	}
	step := int8(-1)
	if sum < 0 {
		step = 1
	}
	order := make([]int, len(q.Bins))
	for i := range order {
		order[i] = i
	}
	// Most marginal first: a residual near ±0.5 was nearly rounded the other way already.
	sort.SliceStable(order, func(a, b int) bool {
		return math.Abs(residual[order[a]]) > math.Abs(residual[order[b]])
	})

	// Error feedback normally leaves |sum| at one or two, but a clipped table can leave more than
	// there are marginal bins, so keep going until it is flat or nothing more can move.
	for sum != 0 {
		moved := false
		for _, i := range order {
			if sum == 0 {
				break
			}
			next := int(q.Bins[i]) + int(step)
			if next > tableLimit || next < -tableLimit {
				continue
			}
			q.Bins[i] = int8(next)
			sum += int(step)
			moved = true
		}
		if !moved {
			return
		}
	}
}
