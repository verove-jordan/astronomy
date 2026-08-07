package solar

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestEstimateRotation_IgnoresTheInstrumentsOwnGradient is the contract that keeps a two-clip session
// from being stacked at the wrong angle.
//
// Rotation is the one registration term the limb cannot supply — a circle is rotation-invariant — so
// it is correlated off disc structure, and that makes it the one place a confident, silent,
// whole-degree error can enter a stack. The error mode is specific: the etalon's sweet spot and the
// eyepiece's vignette lay a large smooth gradient across the annulus the correlation reads, and that
// gradient MOVES when the phone is re-seated between clips. If the profile still carries it, the
// correlation lines up the gradients rather than the plage.
//
// It hides from almost everything downstream. Rotating a disc about its own centre maps the disc onto
// itself, so the limb lands exactly where it was and the point spread function is unchanged; only the
// prominences move. On two real clips this returned 1.62° with a scatter of 0.17° — the picture of a
// good measurement — and it was 25 px of pure error at the limb.
//
// The fixture gives the two frames DIFFERENT gradients, and asks only that the answer match the one
// the same estimator gives on the clean pair. Stating it as agreement rather than against a literal
// angle keeps the test independent of the estimator's sign convention.
func TestEstimateRotation_IgnoresTheInstrumentsOwnGradient(t *testing.T) {
	s := defaultSun()
	s.w, s.h, s.cx, s.cy, s.r = 1400, 1400, 700.0, 700.0, 560
	s.features, s.proms, s.ringAmp, s.gradAmp, s.noise = 26, 0, 0, 0, 0
	base := drawSun(s)
	l, ok := FitLimb(base)
	require.True(t, ok)

	const turn = 1.5 // degrees, the sort of step a re-seated phone produces
	turned := Warp(base, Transform{Scale: 1, RotDeg: turn, CX: l.CX, CY: l.CY}, s.w, 1)
	tl, ok := FitLimb(turned)
	require.True(t, ok)

	clean, ok := EstimateRotation(base, turned, l, tl)
	require.True(t, ok)
	assert.InDelta(t, turn, math.Abs(clean), 0.15, "the estimator must find a known rotation on clean frames")

	// A smooth off-centre brightness gradient, of the kind the etalon's sweet spot makes — and a
	// DIFFERENT one on each frame, because the whole point is that it moves between clips. The
	// amplitude is deliberately large against the disc's own structure: that is the real ratio.
	tilt := func(im *fits.Image, ax, ay, amp float64) *fits.Image {
		out := fits.NewImage(im.W, im.H, 1)
		for y := 0; y < im.H; y++ {
			for x := 0; x < im.W; x++ {
				i := y*im.W + x
				g := 1 + amp*((float64(x)-l.CX)*ax+(float64(y)-l.CY)*ay)/l.R
				out.Pix[0][i] = float32(float64(im.Pix[0][i]) * g)
			}
		}
		return out
	}
	a := tilt(base, 1, 0.2, 0.35)
	b := tilt(turned, -0.3, 1, 0.35)
	al, ok := FitLimb(a)
	require.True(t, ok)
	bl, ok := FitLimb(b)
	require.True(t, ok)

	got, ok := EstimateRotation(a, b, al, bl)
	require.True(t, ok)
	t.Logf("clean %+.3f°, with mismatched gradients %+.3f° (%.1f px apart at the limb)",
		clean, got, math.Abs(got-clean)*math.Pi/180*l.R)
	assert.InDelta(t, clean, got, 0.15,
		"the instrument's own gradient must not move the answer — it moves between clips, the Sun does not")
}
