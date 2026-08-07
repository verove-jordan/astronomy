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

// TestAPField_CoarseToFineFindsShiftsBeyondItsRefineWindow is the contract that makes the ladder a
// speed optimisation rather than a range limit.
//
// The second rung only searches apRefineShift pixels, so on its own it could never find a six-pixel
// displacement. It does not have to: the coarse rung locates the node first and hands the fine rung a
// seed. If seeding ever broke, this is what would catch it — and it would break quietly, because a
// field that fails to find a large shift returns a SMALL one, which stacks smoother and reads as an
// improvement on every simple sharpness metric.
func TestAPField_CoarseToFineFindsShiftsBeyondItsRefineWindow(t *testing.T) {
	s := defaultSun()
	s.w, s.h, s.cx, s.cy, s.r, s.proms = 700, 700, 350.3, 350.7, 250, 2
	im := drawSun(s)
	l, ok := FitLimb(im)
	require.True(t, ok)
	side := 700
	canonical := Limb{CX: float64(side-1) / 2, CY: float64(side-1) / 2, R: l.R}
	ref := Warp(im, SolveTransform(l, canonical), side, 1)

	for _, sh := range []struct{ dx, dy float64 }{{6, 0}, {-5, 4}} {
		t.Run("shift", func(t *testing.T) {
			require.Greater(t, math.Hypot(sh.dx, sh.dy), apRefineShift,
				"the fixture must exceed the refine window, or it tests nothing")
			moved := Warp(im, Transform{Scale: 1, CX: l.CX + sh.dx, CY: l.CY + sh.dy}, side, 1)

			ladder := measureAPFieldScaled(ref, moved, canonical, 16, side, 1)

			require.Greater(t, ladder.valid, 20)
			dx, dy := ladder.at(canonical.CX, canonical.CY)
			assert.InDelta(t, sh.dx, dx, 0.6, "the coarse rung must carry the fine rung to the shift")
			assert.InDelta(t, sh.dy, dy, 0.6)
		})
	}
}

// The ladder exists to cost less than a single full-scale search while answering the same. This pins
// the "answering the same" half; the cost half is measured on real frames, not asserted here.
func TestAPField_CoarseToFineAgreesWithASingleFullScaleSearch(t *testing.T) {
	s := defaultSun()
	s.w, s.h, s.cx, s.cy, s.r, s.proms = 700, 700, 350.3, 350.7, 250, 2
	im := drawSun(s)
	l, ok := FitLimb(im)
	require.True(t, ok)
	side := 700
	canonical := Limb{CX: float64(side-1) / 2, CY: float64(side-1) / 2, R: l.R}
	ref := Warp(im, SolveTransform(l, canonical), side, 1)
	moved := Warp(im, Transform{Scale: 1, CX: l.CX + 3, CY: l.CY - 2}, side, 1)

	ladder := measureAPFieldScaled(ref, moved, canonical, 16, side, 1)
	// One pass at full scale, searching the whole range — what the ladder replaces.
	direct := apField{n: 16, side: side, dx: make([]float64, 256), dy: make([]float64, 256), ok: make([]bool, 256)}
	measureAPPass(&direct, ref, moved, canonical, 16, side, apPass{reduce: 1, search: apMaxShift})
	rejectAPOutliers(&direct)
	fillAPGaps(&direct)

	require.Greater(t, ladder.valid, 20)
	require.Greater(t, direct.valid, 20)
	var worst float64
	for k := range ladder.dx {
		worst = math.Max(worst, math.Hypot(ladder.dx[k]-direct.dx[k], ladder.dy[k]-direct.dy[k]))
	}
	assert.Less(t, worst, 0.5, "the ladder must land where the exhaustive search lands, node for node")
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

// TestFillAPGaps_ExtrapolatesTheShearRatherThanAveragingItAway pins what the field does where it
// could not be measured, which is the whole outer rim of the grid — every alignment point whose patch
// would reach across the limb is vetoed.
//
// A hand-held phone with a rolling shutter produces a SHEAR: the top of the disc is read out
// milliseconds before the bottom, so a frame captured mid-shake is skewed rather than displaced. A
// shear averages to exactly zero over the disc and is largest at the rim, so filling the rim with the
// mean of the measured nodes — which is what this used to do — is wrong precisely where the limb is,
// and it is the one place the point spread function gets measured.
func TestFillAPGaps_ExtrapolatesTheShearRatherThanAveragingItAway(t *testing.T) {
	const n, side = 16, 1600
	f := apField{n: n, side: side, dx: make([]float64, n*n), dy: make([]float64, n*n), ok: make([]bool, n*n)}
	half := float64(side-1) / 2
	// A pure shear about the raster centre: dx grows with y, so it is zero on average and ±3 px at the
	// top and bottom edges.
	shear := func(x, y float64) (float64, float64) { return 3 * (y - half) / half, 0 }
	for j := 0; j < n; j++ {
		for i := 0; i < n; i++ {
			x, y := nodeAt(i, j, n, side)
			// Measured only where a patch would fit inside the disc — the same veto measureAPPass
			// applies, which leaves the rim blank.
			if math.Hypot(x-half, y-half) > 0.62*half {
				continue
			}
			k := j*n + i
			f.dx[k], f.dy[k] = shear(x, y)
			f.ok[k] = true
		}
	}
	rejectAPOutliers(&f)
	require.Greater(t, f.valid, 24, "the fixture must leave enough measured nodes to fit six parameters")
	fillAPGaps(&f)

	// Probe the rim, where nothing was measured and the shear is at its largest.
	var worst, worstWant float64
	for _, p := range []struct{ x, y float64 }{
		{half, 0.02 * half}, {half, 1.98 * half}, {0.05 * half, 0.05 * half}, {1.95 * half, 1.95 * half},
	} {
		wantX, _ := shear(p.x, p.y)
		gotX, _ := f.at(p.x, p.y)
		if e := math.Abs(gotX - wantX); e > worst {
			worst, worstWant = e, wantX
		}
	}
	t.Logf("worst rim error %.2f px against a shear reaching %.2f px there", worst, worstWant)
	// Comfortably better than the 3 px a constant fill would have been wrong by, with room for the
	// smoothing pass that follows the fill.
	assert.Less(t, worst, 1.0, "the rim must carry the shear the disc measured, not the disc's average")
}
