package pec

import "math"

// The number that actually matters: how long an exposure can be before the stars trail.
//
// internal/tracking answers this by modelling the whole error as one sinusoid at the worm period and
// taking its steepest slope. That is a fair description of an UNCORRECTED mount, where the
// fundamental dominates. It is a bad description of a corrected one: what survives PEC is the fast
// harmonics, and a harmonic k moves the star k times faster per arcsecond of amplitude. Judging a
// before-and-after with that formula would credit the correction with an improvement it did not make.
//
// So this measures the curve instead of modelling it — the worst excursion over any window of the
// given length, wherever in the worm cycle the shutter happens to open. No assumption about shape,
// and both sides of a comparison are computed the same way.

// TrailBudgetPixels is how far a star may move before the trailing shows. Anything under about one
// and a half pixels is lost in the seeing disc and the pixel grid.
const TrailBudgetPixels = 1.5

// excursionGridPerBin is how finely the curve is sampled when measuring trailing. The worst case
// rarely lands on a bin boundary.
const excursionGridPerBin = 8

// WorstExcursionArcsec is how far the star smears during the worst-placed exposure of the given
// length.
//
// It is the RANGE the star covers during the exposure — its maximum minus its minimum — not the
// distance between where it starts and where it ends. Those differ, and the difference matters: over
// exactly one worm revolution the star returns to where it began, so an endpoint measure would call
// that a perfectly round star when in fact it wandered the full peak-to-peak and back.
func WorstExcursionArcsec(curve []float64, g Geometry, driftArcsecPerSec, seconds float64) float64 {
	if !g.valid() || len(curve) != g.Bins+1 || seconds <= 0 {
		return 0
	}
	grid, gridSec := excursionGrid(curve, g, driftArcsecPerSec)
	window := int(math.Ceil(seconds/gridSec)) + 1
	if window > len(grid) {
		window = len(grid)
	}
	return worstWindowRange(grid, window)
}

// excursionGrid samples two whole revolutions, with the drift ramp folded in, so any start phase has
// a full window ahead of it without wrapping.
func excursionGrid(curve []float64, g Geometry, driftArcsecPerSec float64) ([]float64, float64) {
	n := g.Bins * excursionGridPerBin
	gridSec := g.WormPeriodSec / float64(n)
	grid := make([]float64, 2*n)
	for i := range grid {
		phase := float64(i) / excursionGridPerBin
		grid[i] = evalCurve(curve, g, phase) + driftArcsecPerSec*float64(i)*gridSec
	}
	return grid, gridSec
}

// worstWindowRange is the largest max-minus-min over any window of the given width, found with
// monotonic deques so the whole scan is linear rather than quadratic.
func worstWindowRange(grid []float64, window int) float64 {
	if window <= 1 || len(grid) == 0 {
		return 0
	}
	var maxDeque, minDeque []int
	var worst float64
	for i, v := range grid {
		for len(maxDeque) > 0 && grid[maxDeque[len(maxDeque)-1]] <= v {
			maxDeque = maxDeque[:len(maxDeque)-1]
		}
		maxDeque = append(maxDeque, i)
		for len(minDeque) > 0 && grid[minDeque[len(minDeque)-1]] >= v {
			minDeque = minDeque[:len(minDeque)-1]
		}
		minDeque = append(minDeque, i)

		if start := i - window + 1; start >= 0 {
			for maxDeque[0] < start {
				maxDeque = maxDeque[1:]
			}
			for minDeque[0] < start {
				minDeque = minDeque[1:]
			}
			if r := grid[maxDeque[0]] - grid[minDeque[0]]; r > worst {
				worst = r
			}
		}
	}
	return worst
}

// MaxUnguidedSec is the longest exposure whose worst-case trailing stays within budget.
//
// It is capped at one worm revolution: beyond that the periodic term has repeated and the answer is
// governed by drift alone, which is a different conversation (polar alignment, not PEC).
func MaxUnguidedSec(curve []float64, g Geometry, driftArcsecPerSec, budgetArcsec float64) float64 {
	if !g.valid() || len(curve) != g.Bins+1 || budgetArcsec <= 0 {
		return 0
	}
	if WorstExcursionArcsec(curve, g, driftArcsecPerSec, g.WormPeriodSec) <= budgetArcsec {
		return g.WormPeriodSec
	}
	// A longer window can only ever contain more of the curve, so the excursion is monotonic in
	// exposure length and bisection cannot land on a later, luckier answer.
	lo, hi := 0.0, g.WormPeriodSec
	for i := 0; i < 40; i++ {
		mid := (lo + hi) / 2
		if WorstExcursionArcsec(curve, g, driftArcsecPerSec, mid) > budgetArcsec {
			hi = mid
		} else {
			lo = mid
		}
	}
	return lo
}

// BudgetArcsec is the trailing budget for a given image scale.
func BudgetArcsec(arcsecPerPixel float64) float64 { return TrailBudgetPixels * arcsecPerPixel }

// Improvement compares two curves the same way, so a before-and-after means something.
type Improvement struct {
	BeforePPArcsec    float64
	AfterPPArcsec     float64
	BeforeMaxUnguided float64
	AfterMaxUnguided  float64
}

// AmplitudeRatio is how much smaller the error got. Below 1 means the correction made it worse —
// which, on a first run, almost always means the sign or the phase is inverted.
func (i Improvement) AmplitudeRatio() float64 {
	if i.AfterPPArcsec <= 0 {
		return 0
	}
	return i.BeforePPArcsec / i.AfterPPArcsec
}

// Worsened reports whether the mount tracks measurably worse with the curve than without it.
func (i Improvement) Worsened() bool {
	return i.AfterPPArcsec > i.BeforePPArcsec*1.1
}

// Compare measures both curves against the same budget.
func Compare(before, after []float64, g Geometry, driftBefore, driftAfter, budgetArcsec float64) Improvement {
	return Improvement{
		BeforePPArcsec:    PeakToPeak(before),
		AfterPPArcsec:     PeakToPeak(after),
		BeforeMaxUnguided: MaxUnguidedSec(before, g, driftBefore, budgetArcsec),
		AfterMaxUnguided:  MaxUnguidedSec(after, g, driftAfter, budgetArcsec),
	}
}
