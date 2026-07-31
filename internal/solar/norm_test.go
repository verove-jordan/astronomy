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
}
