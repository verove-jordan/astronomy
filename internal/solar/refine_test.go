package solar

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// constantField is a distortion field with the same displacement everywhere — the field form of a
// pure translation, which is what shiftCanonical claims to be equivalent to.
func constantField(dx, dy float64, side int) apField {
	return apField{n: 2, side: side, valid: 4,
		dx: []float64{dx, dx, dx, dx}, dy: []float64{dy, dy, dy, dy},
		ok: []bool{true, true, true, true}}
}

// TestShiftCanonical_IsExactlyAConstantDistortionField pins the sign and the geometry in one go.
//
// The correction has to be folded into the transform rather than applied as a second warp, or it
// costs more resolution in resampling than it wins in alignment. That means it is expressed in the
// frame's own source coordinates, through the inverse of a rotation and a scale, while what was
// MEASURED is a displacement in canonical output pixels — three chances to drop a sign or invert a
// term, none of which announce themselves. A wrong sign in particular fails silently in the worst
// way: it doubles every frame's misalignment, and a doubly-misaligned stack is SMOOTHER than a
// correct one, so it reads as an improvement on any simple sharpness metric.
//
// The distortion field already has its sign pinned against ground truth
// (TestAPField_CorrectsAKnownShift), so stating the equivalence against it settles all of that at
// once — including at a scale and rotation where the two coordinate systems genuinely differ.
func TestShiftCanonical_IsExactlyAConstantDistortionField(t *testing.T) {
	s := defaultSun()
	s.w, s.h, s.cx, s.cy, s.r, s.features = 700, 700, 350.3, 350.7, 250, 12
	im := drawSun(s)
	l, ok := FitLimb(im)
	require.True(t, ok)
	const side = 700

	for _, tc := range []struct {
		name   string
		t      Transform
		dx, dy float64
	}{
		{"pure translation", Transform{Scale: 1, CX: l.CX, CY: l.CY}, 3, -2},
		{"under a scale", Transform{Scale: 1.4, CX: l.CX, CY: l.CY}, -4, 1.5},
		{"under a rotation", Transform{Scale: 1, RotDeg: 7, CX: l.CX, CY: l.CY}, 2.5, 3},
		{"under both", Transform{Scale: 0.8, RotDeg: -5, CX: l.CX, CY: l.CY}, -1.5, -2.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := constantField(tc.dx, tc.dy, side)
			viaField := warpWithField(im, tc.t, side, 1, &f)
			viaTransform := Warp(im, tc.t.shiftCanonical(tc.dx, tc.dy, 1), side, 1)

			var worst float64
			for i := range viaField.Pix[0] {
				worst = math.Max(worst, math.Abs(float64(viaField.Pix[0][i]-viaTransform.Pix[0][i])))
			}
			t.Logf("worst pixel difference %.2g", worst)
			assert.Less(t, worst, 1e-5,
				"folding the shift into the transform must land exactly where the field puts it")
		})
	}
}

// TestRegRefiner_MeasuresAKnownDisplacement checks the measurement on its own, before any of it is
// folded anywhere.
func TestRegRefiner_MeasuresAKnownDisplacement(t *testing.T) {
	s := defaultSun()
	// A canvas size that is NOT a round multiple of the ladder's reductions, because CanonicalSide
	// produces even numbers and nothing more: 1402/8 is not an integer, and the box reduction rounds
	// its factor up rather than reducing by exactly what it was asked for. Sizing this fixture in
	// round numbers is how the refiner came to return nil on every real raster while this test passed.
	s.w, s.h, s.cx, s.cy, s.r, s.features = 1402, 1402, 701.3, 701.7, 560, 20
	im := drawSun(s)
	l, ok := FitLimb(im)
	require.True(t, ok)
	const side = 1402
	canonical := Limb{CX: float64(side-1) / 2, CY: float64(side-1) / 2, R: l.R}
	ref := Warp(im, SolveTransform(l, canonical), side, 1)
	r := newRegRefiner(ref)
	require.NotNil(t, r, "the refiner must handle a canvas whose size is not a multiple of the reductions")

	for _, sh := range []struct{ dx, dy float64 }{{0, 0}, {3, -2}, {-6.5, 1.5}, {11, 9}} {
		t.Run(fmt.Sprintf("%+.1f,%+.1f", sh.dx, sh.dy), func(t *testing.T) {
			// Moving the assumed centre by (dx,dy) moves the disc in the warped raster by (-dx,-dy),
			// so the refiner must report the displacement that puts it back.
			moved := Warp(im, Transform{Scale: 1, CX: l.CX + sh.dx, CY: l.CY + sh.dy}, side, 1)
			gx, gy := r.measure(moved, canonical)
			t.Logf("centre moved by (%+.1f,%+.1f) -> measured (%+.2f,%+.2f)", sh.dx, sh.dy, gx, gy)
			assert.InDelta(t, sh.dx, gx, 0.3, "measured dx")
			assert.InDelta(t, sh.dy, gy, 0.3, "measured dy")
		})
	}
}

// TestStack_RefinesAwayACentringError is the end-to-end claim, on frames whose true alignment is
// known because they were drawn from one scene.
//
// The injected error is deliberately asymmetric — several pixels in x against a fraction of a pixel
// in y — because that is what the real fit does. On a 1.06"/px clip the fitted centre deviates from
// its own trend by 3.83 px in x and 0.50 px in y, and the stack's limb agrees: 4.1 px of point spread
// where the edge normal lies along x against 3.4 px where it lies along y. Nothing physical can do
// that; the Sun does not know how the sensor is turned.
func TestStack_RefinesAwayACentringError(t *testing.T) {
	const n = 16
	spec := defaultSun()
	spec.w, spec.h, spec.cx, spec.cy, spec.r = 1000, 1000, 500, 500, 400
	spec.features, spec.ringAmp, spec.psfSigma = 20, 0, 1.6

	dir := t.TempDir()
	var frames []Frame
	for i := 0; i < n; i++ {
		s := spec
		// One scene, independent noise: the frames really are identical, so any softening in the stack
		// was introduced by registration and nothing else.
		s.noiseSeed = uint64(1000 + i)
		im := drawSun(s)
		p := filepath.Join(dir, fmt.Sprintf("f_%03d.fits", i))
		require.NoError(t, im.WriteFITS(p))
		l, ok := FitLimb(im)
		require.True(t, ok)
		score := FrameSharpness(im, l)
		// The fixture's error is in what the stacker is TOLD, not in the pixels: a deterministic
		// per-frame perturbation of the fitted centre, which is exactly what a noisy circle fit hands
		// over.
		l.CX += 3.5 * math.Cos(float64(i)*2.399)
		l.CY += 0.5 * math.Sin(float64(i)*2.399)
		frames = append(frames, Frame{Path: p, Index: i, Limb: l, Score: score})
	}

	ctx := context.Background()
	measure := func(o StackOptions) float64 {
		st, err := Stack(ctx, frames, o)
		require.NoError(t, err)
		l, ok := FitLimb(st.Master)
		require.True(t, ok)
		psf := MeasurePSF(st.Master, l)
		require.True(t, psf.OK)
		return psf.SigmaPx
	}
	blurred := measure(StackOptions{NoRefine: true})
	refined := measure(StackOptions{})
	t.Logf("stack point spread: fitted centre %.2f px -> refined %.2f px (frames were drawn at %.2f)",
		blurred, refined, spec.psfSigma)
	assert.Less(t, refined, blurred-0.3,
		"refining the centre must recover resolution the fitted centre threw away")
}

// TestWarp_DoesNotWidenTheLimb bounds what the single resample costs, at the scale a real capture
// actually runs at.
//
// This is the floor under every stack: whatever a stack of one frame measures, no stack of many can
// beat. It is tested here rather than inferred from the synthetic stacking fixture because that
// fixture runs at a 330 px radius and a real phone clip runs at 900, and a resample kernel's cost is
// a property of how many pixels the edge spans — which is the same one or two at either radius,
// while everything else about the frame is three times larger.
//
// Sub-pixel placements are swept deliberately: a Catmull-Rom kernel reproduces the source exactly at
// zero phase and is at its softest near half a pixel, so testing one offset can pass while the
// typical case does not.
func TestWarp_DoesNotWidenTheLimb(t *testing.T) {
	s := defaultSun()
	s.w, s.h, s.cx, s.cy, s.r = 2160, 2160, 1080.0, 1080.0, 900
	s.features, s.ringAmp, s.gradAmp, s.proms = 24, 0, 0, 3
	s.psfSigma = 1.3 // what the real frames measure

	im := drawSun(s)
	l, ok := FitLimb(im)
	require.True(t, ok)
	before := MeasurePSF(im, l)
	require.True(t, before.OK)

	side := CanonicalSide(l.R, defaultCropMargin, 1)
	canonical := Limb{CX: float64(side-1) / 2, CY: float64(side-1) / 2, R: l.R}
	worst := before.SigmaPx
	for _, off := range []float64{0, 0.25, 0.5, 0.75} {
		moved := l
		moved.CX += off
		moved.CY += off * 0.5
		w := Warp(im, SolveTransform(moved, canonical), side, 1)
		psf := MeasurePSF(w, canonical)
		require.True(t, psf.OK)
		t.Logf("sub-pixel offset %.2f px: limb %.2f -> %.2f px", off, before.SigmaPx, psf.SigmaPx)
		worst = math.Max(worst, psf.SigmaPx)
	}
	assert.Less(t, worst, before.SigmaPx+0.25,
		"one Catmull-Rom pass must cost a fraction of a pixel, not a pixel")
}
