package solar

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAPField_CorrectsAKnownShift is the round-trip that pins the sign of the correction.
//
// It exists because getting that sign wrong fails silently in the worst possible way: the stack
// comes out SMOOTHER, which every naive sharpness metric reads as an improvement, while the frames
// are actually twice as misaligned as they were before.
func TestAPField_CorrectsAKnownShift(t *testing.T) {
	s := defaultSun()
	s.w, s.h, s.cx, s.cy, s.r, s.proms = 700, 700, 350.3, 350.7, 250, 2
	im := drawSun(s)
	l, ok := FitLimb(im)
	require.True(t, ok)

	side := 700
	canonical := Limb{CX: float64(side-1) / 2, CY: float64(side-1) / 2, R: l.R}
	ref := Warp(im, SolveTransform(l, canonical), side, 1)

	for _, sh := range []struct{ dx, dy float64 }{{3, 2}, {-4, 1}, {0, -3}} {
		t.Run("shift", func(t *testing.T) {
			moved := Warp(im, Transform{Scale: 1, CX: l.CX + sh.dx, CY: l.CY + sh.dy}, side, 1)

			f := measureAPField(ref, moved, canonical, 16)
			require.Greater(t, f.valid, 20, "the field must actually measure something")
			dx, dy := f.at(canonical.CX, canonical.CY)
			assert.InDelta(t, sh.dx, dx, 0.6, "measured dx")
			assert.InDelta(t, sh.dy, dy, 0.6, "measured dy")

			// The correction must bring it back, not push it further away.
			before := rmsDiff(moved.Pix[0], ref.Pix[0], side, side, canonical, 0.8)
			fixed := warpWithField(im, Transform{Scale: 1, CX: l.CX + sh.dx, CY: l.CY + sh.dy}, side, 1, &f)
			after := rmsDiff(fixed.Pix[0], ref.Pix[0], side, side, canonical, 0.8)
			t.Logf("shift (%.0f,%.0f): on-disc RMS vs reference %.5f -> %.5f", sh.dx, sh.dy, before, after)
			assert.Less(t, after, before/2, "the field correction must undo the shift")
		})
	}
}

// TestAPField_IgnoresFeaturelessRegions pins that points with nothing to lock onto are dropped
// rather than allowed to contribute a noise measurement.
func TestAPField_IgnoresFeaturelessRegions(t *testing.T) {
	flat := defaultSun()
	flat.w, flat.h, flat.cx, flat.cy, flat.r = 700, 700, 350, 350, 250
	// A perfectly uniform disc with no noise: there is genuinely nothing on it to correlate against.
	flat.u1, flat.u2, flat.noise, flat.psfSigma, flat.ringAmp, flat.gradAmp = 0, 0, 0, 1, 0, 0
	im := drawSun(flat)
	l, ok := FitLimb(im)
	require.True(t, ok)
	canonical := Limb{CX: 349.5, CY: 349.5, R: l.R}
	ref := Warp(im, SolveTransform(l, canonical), 700, 1)

	f := measureAPField(ref, ref, canonical, 16)
	assert.Zero(t, f.valid, "a featureless disc must yield no alignment points, not noisy ones")
	dx, dy := f.at(canonical.CX, canonical.CY)
	assert.InDelta(t, 0.0, math.Hypot(dx, dy), 1e-9, "and the field must then be the identity")
}

// TestWarpCovered_MarksOutOfFrame pins that a warp never fabricates data outside the source.
//
// The sampler clamps at the border, so without a coverage mask an output pixel that falls off the
// source comes back as a smear of the outermost row. That is not a cosmetic problem: it stacks like
// real data and shows up as a dark speckled arc along whichever edge cut the disc, which is exactly
// what a partial-disc capture produces.
func TestWarpCovered_MarksOutOfFrame(t *testing.T) {
	s := defaultSun()
	s.w, s.h, s.cx, s.cy, s.r = 400, 400, 200, 200, 150
	im := drawSun(s)

	t.Run("a fully contained warp is entirely covered", func(t *testing.T) {
		_, cov := warpCovered(im, Transform{Scale: 1, CX: 200, CY: 200}, 300, 1, nil)
		for _, c := range cov {
			require.True(t, c)
		}
	})

	t.Run("a warp reaching past the source marks the gap", func(t *testing.T) {
		// A canvas far larger than the source: the corners cannot be sampled from anywhere.
		out, cov := warpCovered(im, Transform{Scale: 1, CX: 200, CY: 200}, 700, 1, nil)
		missing := 0
		for k, c := range cov {
			if !c {
				missing++
				assert.Zero(t, out.Pix[0][k], "an uncovered pixel must be empty, not a clamped edge value")
			}
		}
		assert.Greater(t, missing, 0, "the canvas overruns the source, so some of it is uncovered")
		assert.Less(t, missing, len(cov), "and some of it is covered")
	})

	t.Run("an off-centre disc marks only the side that ran off", func(t *testing.T) {
		_, cov := warpCovered(im, Transform{Scale: 1, CX: 40, CY: 200}, 400, 1, nil)
		side := 400
		leftMissing, rightMissing := 0, 0
		for y := 0; y < side; y++ {
			for x := 0; x < side; x++ {
				if !cov[y*side+x] {
					if x < side/2 {
						leftMissing++
					} else {
						rightMissing++
					}
				}
			}
		}
		assert.Greater(t, leftMissing, rightMissing,
			"the disc ran off the left of the source, so that is the side left uncovered")
	})
}
