package solar

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNormalize_ConvergesExposures is the property a bracketed session depends on: frames shot at
// wildly different exposures must end up on one photometric scale, so the stack averages signal
// rather than fighting a brightness ramp.
func TestNormalize_ConvergesExposures(t *testing.T) {
	dir := t.TempDir()
	scales := []float64{0.25, 0.5, 1.0, 2.0, 4.0} // a 16x bracket, close to a real session's range
	var frames []Frame
	for i, k := range scales {
		s := defaultSun()
		s.w, s.h, s.cx, s.cy, s.r = 600, 600, 300.4, 300.6, 200
		s.seed = uint64(i)
		im := drawSun(s)
		for j := range im.Pix[0] {
			im.Pix[0][j] *= float32(k)
		}
		path := filepath.Join(dir, "f"+string(rune('a'+i))+".fits")
		require.NoError(t, im.WriteFITS(path))
		l, ok := FitLimb(im)
		require.True(t, ok)
		frames = append(frames, Frame{Path: path, Limb: l})
	}

	spread := func() float64 {
		lo, hi := math.Inf(1), math.Inf(-1)
		for _, f := range frames {
			im, err := readFrame(f.Path)
			require.NoError(t, err)
			m := discStats(im, f.Limb.CX, f.Limb.CY, f.Limb.R).median
			lo, hi = math.Min(lo, m), math.Max(hi, m)
		}
		return hi / lo
	}

	before := spread()
	warns, err := Normalize(frames)
	require.NoError(t, err)
	require.Empty(t, warns)
	after := spread()
	t.Logf("on-disc brightness spread: %.2fx before, %.2fx after", before, after)

	assert.InDelta(t, 16.0, before, 0.5, "the fixture really is a 16x bracket")
	assert.Less(t, after, 1.02, "normalisation must bring every frame onto one scale")

	// The curve is measured ON THE DISC, so every pixel outside the limb is under-range and gets
	// extrapolated rather than fitted. That extrapolation runs through the origin, not along the
	// lowest segment's slope: continuing the slope over a range several times wider than the one it
	// was fitted on drove real skies to minus eight percent of the disc — a floor no stretch recovers
	// from, sitting exactly where the prominences are.
	for _, f := range frames {
		im, err := readFrame(f.Path)
		require.NoError(t, err)
		lo := math.Inf(1)
		for _, v := range im.Pix[0] {
			lo = math.Min(lo, float64(v))
		}
		assert.GreaterOrEqual(t, lo, 0.0, "%s: normalisation must never drive the sky negative", f.Path)
	}
}

// applyLUT is what rewrites every pixel, including the off-limb sky the curve was never fitted on.
func TestApplyLUT_ExtrapolatesBelowTheFittedRangeThroughTheOrigin(t *testing.T) {
	// A curve fitted between 0.10 and 0.50 that also LIFTS the level: continuing its slope downward
	// is what used to push the sky below zero.
	pts := []lutPoint{{from: 0.10, to: 0.02}, {from: 0.50, to: 0.60}}
	p := []float32{0, 0.001, 0.02, 0.10, 0.30, 0.50, 0.80}

	applyLUT(p, pts)

	assert.Equal(t, float32(0), p[0], "no light must map to no light")
	for i, v := range p {
		assert.GreaterOrEqual(t, v, float32(0), "index %d went negative", i)
	}
	for i := 1; i < len(p); i++ {
		assert.GreaterOrEqual(t, p[i], p[i-1], "the mapping must stay monotone across the join at %d", i)
	}
	assert.Greater(t, p[6], p[5], "above the fitted range the last segment's slope still carries highlights")
}
