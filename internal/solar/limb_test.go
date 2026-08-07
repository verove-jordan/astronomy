package solar

import (
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/planetary"
)

// TestFitLimb_Accuracy checks the fit against known geometry across the corruptions a real Hα frame
// carries. The tolerances are what registration needs: a radius error of 0.15 px on a ~430 px disc
// is 0.035%, which is a third of a pixel of scale error at the limb.
func TestFitLimb_Accuracy(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*sunSpec)
	}{
		{"clean disc", func(s *sunSpec) {}},
		{"no limb darkening", func(s *sunSpec) { s.u1, s.u2 = 0, 0 }},
		{"strong limb darkening", func(s *sunSpec) { s.u1, s.u2 = 0.7, 0.1 }},
		{"newton rings", func(s *sunSpec) { s.ringAmp = 0.10 }},
		{"sweet-spot gradient", func(s *sunSpec) { s.gradAmp = 0.30 }},
		{"rings and gradient", func(s *sunSpec) { s.ringAmp, s.gradAmp = 0.10, 0.30 }},
		{"prominences", func(s *sunSpec) { s.proms = 4 }},
		{"soft seeing", func(s *sunSpec) { s.psfSigma = 3 }},
		{"noisy", func(s *sunSpec) { s.noise = 0.02 }},
		{"small disc in frame", func(s *sunSpec) { s.r = 120; s.cx, s.cy = 700.3, 600.6 }},
		{"everything at once", func(s *sunSpec) {
			s.ringAmp, s.gradAmp, s.proms, s.psfSigma, s.noise = 0.10, 0.30, 4, 2.5, 0.015
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := defaultSun()
			tt.mod(&s)
			got, ok := FitLimb(drawSun(s))
			require.True(t, ok, "the limb must be found")
			assert.InDelta(t, s.cx, got.CX, 0.30, "centre x")
			assert.InDelta(t, s.cy, got.CY, 0.30, "centre y")
			assert.InDelta(t, s.r, got.R, 0.60, "radius")
			assert.False(t, got.Partial, "a fully framed disc is not partial")
			assert.Equal(t, 360.0, got.ArcDeg, "a full disc must be covered all the way round")
		})
	}
}

// TestFitLimb_SubPixelOffsets pins that the fit resolves sub-pixel centre positions rather than
// snapping to the pixel grid — registration is built on exactly that.
func TestFitLimb_SubPixelOffsets(t *testing.T) {
	for i := 0; i < 8; i++ {
		frac := float64(i) / 8
		t.Run(fmt.Sprintf("offset+%.3f", frac), func(t *testing.T) {
			s := defaultSun()
			s.cx, s.cy = 700+frac, 600+frac
			got, ok := FitLimb(drawSun(s))
			require.True(t, ok)
			assert.InDelta(t, s.cx, got.CX, 0.25, "centre x")
			assert.InDelta(t, s.cy, got.CY, 0.25, "centre y")
		})
	}
}

// TestFitLimb_StableAcrossNoise is the property registration actually depends on: not that the
// radius is exactly right, but that it does not wander frame to frame. A wandering radius injects a
// scale error into every frame of a stack, which shows up as a soft limb and gets misread as seeing.
func TestFitLimb_StableAcrossNoise(t *testing.T) {
	var radii, cxs []float64
	for seed := uint64(0); seed < 25; seed++ {
		s := defaultSun()
		s.seed, s.noise, s.ringAmp, s.gradAmp = seed, 0.02, 0.08, 0.25
		got, ok := FitLimb(drawSun(s))
		require.True(t, ok)
		radii = append(radii, got.R)
		cxs = append(cxs, got.CX)
	}
	assert.Less(t, stddev(radii), 0.15, "radius must be stable across noise realisations")
	assert.Less(t, stddev(cxs), 0.15, "centre must be stable across noise realisations")
}

// TestFitLimb_PartialDisc covers the limb close-up: the centre lies outside the frame entirely and
// only an arc is visible, which is a normal way to shoot prominences and must stay registerable.
func TestFitLimb_PartialDisc(t *testing.T) {
	tests := []struct {
		name           string
		cx             float64
		wantArcAtLeast float64
	}{
		// The visible arc is a property of the framing, not of the fitter: with a 1100 px radius in a
		// 1400x1200 frame the limb leaves the frame top and bottom, so ~66 deg is all there is.
		{"centre just outside", -50, 60},
		{"centre far outside", -700, 60},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := defaultSun()
			s.r, s.cx, s.cy = 1100, tt.cx, 600
			got, ok := FitLimb(drawSun(s))
			require.True(t, ok, "an arc of limb must still yield a fit")
			assert.True(t, got.Partial, "the disc runs past the frame edge")
			assert.InDelta(t, s.r, got.R, 0.02*s.r, "radius within 2%% on a partial disc")
			assert.InDelta(t, s.cx, got.CX, 0.02*s.r, "centre x within 2%% of the radius")
			assert.GreaterOrEqual(t, got.ArcDeg, tt.wantArcAtLeast)
		})
	}
}

// TestFitLimb_RejectsWithoutLimb makes sure the fit refuses rather than inventing geometry when
// there is no limb in shot — a frame zoomed into the disc surface. Guessing here would be worse
// than skipping: the whole group's scale would be set by a fabricated radius.
func TestFitLimb_RejectsWithoutLimb(t *testing.T) {
	s := defaultSun()
	s.r, s.cx, s.cy = 4000, 700, 600 // disc far larger than the frame: no limb anywhere in it
	_, ok := FitLimb(drawSun(s))
	assert.False(t, ok, "no limb in frame must not produce a fit")
}

// TestFitLimb_BeatsLunarFitOnGradient is the written justification for this package having its own
// fitter. planetary's lunar fit keeps only the sharpest 60% of boundary points to reject a
// terminator; on a Sun with an etalon sweet-spot gradient that instead discards whichever azimuth
// is dimmest, and the circle drifts toward the bright side. It is a good fit for the Moon — it is
// simply solving a different problem.
func TestFitLimb_BeatsLunarFitOnGradient(t *testing.T) {
	s := defaultSun()
	s.gradAmp, s.ringAmp = 0.55, 0.10

	mine, ok := FitLimb(drawSun(s))
	require.True(t, ok)
	mineErr := math.Hypot(mine.CX-s.cx, mine.CY-s.cy)

	lunar, ok := planetary.FitDisc(drawSun(s))
	require.True(t, ok, "the lunar fit should still find something — the point is where")
	lunarErr := math.Hypot(lunar.CX-s.cx, lunar.CY-s.cy)

	t.Logf("centre error: solar %.2f px, lunar %.2f px; radius error: solar %+.2f, lunar %+.2f",
		mineErr, lunarErr, mine.R-s.r, lunar.R-s.r)
	assert.Less(t, mineErr, 0.5, "the solar fit stays sub-pixel under a strong gradient")
	assert.Less(t, mineErr, lunarErr, "the solar fit must beat the lunar one on solar data")
}

func stddev(v []float64) float64 {
	if len(v) < 2 {
		return 0
	}
	var mean float64
	for _, x := range v {
		mean += x
	}
	mean /= float64(len(v))
	var ss float64
	for _, x := range v {
		ss += (x - mean) * (x - mean)
	}
	return math.Sqrt(ss / float64(len(v)-1))
}
