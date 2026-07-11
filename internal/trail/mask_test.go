package trail

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// gradient builds a w×h plane rising along y, so perpendicular samples of a horizontal swath have a
// non-zero spread (a real local sigma).
func gradient(w, h int) []float32 {
	p := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			p[y*w+x] = float32(0.1 + 0.0005*float64(y))
		}
	}
	return p
}

// horizLine is the line y=cy over the whole width, width px thick.
func horizLine(cy, width float64) Segment {
	return Segment{Nx: 0, Ny: 1, C: cy, T0: -1e6, T1: 1e6, Width: width}
}

func swathStats(plane []float32, w, h int, s Segment) (mean, variance float64, count int) {
	var sum, sumsq float64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if s.Contains(float64(x), float64(y)) {
				v := float64(plane[y*w+x])
				sum += v
				sumsq += v * v
				count++
			}
		}
	}
	if count == 0 {
		return 0, 0, 0
	}
	mean = sum / float64(count)
	return mean, sumsq/float64(count) - mean*mean, count
}

func TestApplySwathLocalBG_MatchesBackground(t *testing.T) {
	const w, h = 200, 200
	plane := gradient(w, h)
	s := horizLine(100, 4)

	n := ApplySwathLocalBG(plane, w, h, s, 42)
	require.Positive(t, n)
	mean, variance, count := swathStats(plane, w, h, s)
	require.Positive(t, count)
	assert.InDelta(t, 0.1+0.0005*100, mean, 0.02) // ≈ local background at the swath centre
	assert.Positive(t, variance)                  // noise injected — no dead stripe
}

func TestApplySwathLocalBG_Deterministic(t *testing.T) {
	const w, h = 200, 200
	s := horizLine(100, 4)
	a, b := gradient(w, h), gradient(w, h)
	na := ApplySwathLocalBG(a, w, h, s, 99)
	nb := ApplySwathLocalBG(b, w, h, s, 99)
	assert.Equal(t, na, nb)
	assert.Equal(t, a, b)
}

func TestApplySwathLocalBG_LeavesStarUntouched(t *testing.T) {
	const w, h = 200, 200
	plane := gradient(w, h)
	star := 30*w + 100 // far in y from the y=100 line ⇒ off swath
	plane[star] = 5.0

	ApplySwathLocalBG(plane, w, h, horizLine(100, 4), 7)
	assert.Equal(t, float32(5.0), plane[star])
}

func TestSegmentContains_HonorsExtent(t *testing.T) {
	s := Segment{Nx: 0, Ny: 1, C: 100, T0: -150, T1: -50, Width: 4} // project(x,y) = -x ⇒ x∈[50,150]
	assert.True(t, s.Contains(100, 100))                            // on line, within extent
	assert.False(t, s.Contains(10, 100))                            // outside extent
	assert.False(t, s.Contains(100, 110))                           // ⊥ distance 10 > Width
}

func TestApplySwathMedian_ReplacesSwath(t *testing.T) {
	const w, h = 120, 120
	plane := gradient(w, h)
	median := make([]float32, w*h)
	for i := range median {
		median[i] = 0.2
	}
	s := horizLine(60, 4)
	off := 10*w + 5 // off swath
	before := plane[off]

	n := ApplySwathMedian(plane, median, w, h, s)
	require.Positive(t, n)
	assert.Equal(t, float32(0.2), plane[60*w+30]) // swath pixel replaced by median
	assert.Equal(t, before, plane[off])           // off-swath untouched
	// Size mismatch is a no-op.
	assert.Zero(t, ApplySwathMedian(plane, median[:10], w, h, s))
}

func TestMaskFrame_MonoStreak(t *testing.T) {
	const w, h = 220, 220
	im := fits.NewImage(w, h, 1)
	copy(im.Pix[0], noisyPlane(w, h, 0.1, 0.004, 20))
	addStreak(im.Pix[0], w, h, 30, 110, 190, 110, 0.2, 0.8)
	peak := im.Pix[0][110*w+110]

	res := MaskFrame(im, DefaultParams(5))
	require.Len(t, res.Segments, 1)
	assert.Positive(t, res.MaskedPx)
	assert.Less(t, im.Pix[0][110*w+110], peak) // streak painted down toward background

	// Nil / empty inputs never panic.
	assert.Zero(t, MaskFrame(nil, DefaultParams(5)).MaskedPx)
}

func TestMaskFrameFile_RoundTrip(t *testing.T) {
	const w, h = 220, 220
	dir := t.TempDir()
	path := filepath.Join(dir, "frame.fits")
	im := fits.NewImage(w, h, 1)
	copy(im.Pix[0], noisyPlane(w, h, 0.1, 0.004, 21))
	addStreak(im.Pix[0], w, h, 30, 110, 190, 110, 0.2, 0.8)
	require.NoError(t, im.WriteFITS(path))

	res, err := MaskFrameFile(path, DefaultParams(5))
	require.NoError(t, err)
	require.Len(t, res.Segments, 1)
	assert.Positive(t, res.MaskedPx)

	// The rewrite persisted: the on-disk peak is now near background.
	reload, err := fits.ReadImage(path)
	require.NoError(t, err)
	assert.Less(t, reload.Pix[0][110*w+110], float32(0.2))
}
