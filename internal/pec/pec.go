// Package pec turns a measured worm error into the correction table a mount can replay.
//
// This is the half that internal/tracking deliberately stopped short of. That restraint was right at
// the time — "it moves hardware on a model fitted from data, needs on-sky iteration to trust, and the
// honest order is to measure first". What makes it defensible now is that the four things which used
// to be assumptions are measurements:
//
//   - Worm PHASE comes from the mount's own bin counter, not a clock, so any constant offset between
//     what it reports and what playback uses cancels between measuring and writing.
//   - The pixel-to-arcsecond SCALE and its sign come from watching the sky drift with the drive off.
//   - The table's own sign, scale and index offset come from writing a known probe curve and
//     measuring the response, before anything real is written.
//   - Whether the error REPEATS at all is measured first, and a mount whose error does not repeat is
//     refused rather than "corrected" with noise.
//
// Nothing here touches hardware or does I/O. It takes samples in, and hands back a table of signed
// bytes plus an honest account of how much good it will do.
//
// # What PEC can and cannot fix
//
// A table of B bins over one worm revolution can only represent components with a period of at least
// two bins. On a Celestron (B=88, worm 478.7 s) that floor is 10.9 s. Anything faster is not merely
// left alone — attempting it makes matters worse — so the fit is capped well below that limit. Drift,
// wind, seeing and any component not commensurate with the worm are outside PEC's reach by
// construction, and the reported numbers say so rather than implying a fix.
package pec

import "math"

// Geometry is the shape of a mount's PEC table. All of it is read from the mount rather than assumed:
// the bin count and the rate scale are exactly the two values that, if guessed wrong, produce a curve
// of the wrong amplitude with no other symptom.
type Geometry struct {
	Bins int
	// WormPeriodSec is one full revolution of the RA worm.
	WormPeriodSec float64
	// LSBArcsecPerSec is the rate correction one table unit represents. Table entries are signed
	// bytes, so the whole correction is bounded by ±127 of these — about 12 % of sidereal, which is
	// why a PEC correction can never reverse the RA axis and never engages backlash.
	LSBArcsecPerSec float64
}

// BinSec is how long the worm spends in one bin.
func (g Geometry) BinSec() float64 {
	if g.Bins <= 0 {
		return 0
	}
	return g.WormPeriodSec / float64(g.Bins)
}

// NyquistPeriodSec is the fastest component the table can represent: two bins.
func (g Geometry) NyquistPeriodSec() float64 { return 2 * g.BinSec() }

// valid reports whether the geometry describes a usable table.
func (g Geometry) valid() bool {
	return g.Bins > 1 && g.WormPeriodSec > 0 && g.LSBArcsecPerSec > 0
}

// maxHarmonic is the highest worm harmonic the correction will ever try to represent.
//
// The hard limit is B/2 (the two-bin Nyquist), but correcting near it is actively harmful: any phase
// error θ leaves a residual of 2A·sin(θ/2), which passes 100 % — i.e. the "correction" does nothing —
// at θ = 60°, and exceeds it beyond. A half-bin of phase error is θ = k·π/B, so the break-even
// harmonic is around B/3. Staying well under that keeps every harmonic we touch firmly in the
// improving regime, and the ones we drop were mostly noise anyway.
func maxHarmonic(bins int) int {
	k := bins / 6
	if k > 20 {
		k = 20
	}
	if k < 1 {
		k = 1
	}
	return k
}

// wrapPhase folds a phase in bins into [0, bins).
func wrapPhase(phase float64, bins int) float64 {
	b := float64(bins)
	p := math.Mod(phase, b)
	if p < 0 {
		p += b
	}
	return p
}
