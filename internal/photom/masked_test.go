package photom

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// gradientImage builds a mono frame whose left half sits at lo and right half at hi — a crisp
// two-region fixture for mask-restricted measurement.
func gradientImage(w, h int, lo, hi float32) *fits.Image {
	im := fits.NewImage(w, h, 1)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := lo
			if x >= w/2 {
				v = hi
			}
			im.Pix[0][y*w+x] = v
		}
	}
	return im
}

func TestMeasureImageMasked_MatchesUnmaskedOnFullKeep(t *testing.T) {
	im := gradientImage(512, 512, 0.05, 0.30)
	keepAll := func(int, int) bool { return true }

	masked, ok := MeasureImageMasked(im, keepAll)
	require.True(t, ok)
	full := MeasureImage(im)

	assert.InDelta(t, full.Bg, masked.Bg, 1e-3)
	assert.InDelta(t, full.Q[11], masked.Q[11], 1e-3)
	assert.InDelta(t, full.SatFrac, masked.SatFrac, 1e-3)
}

func TestMeasureImageMasked_ExcludesBrightRegion(t *testing.T) {
	const w, h = 512, 512
	im := gradientImage(w, h, 0.05, 0.90)
	leftOnly := func(x, _ int) bool { return x < w/2 }

	fc, ok := MeasureImageMasked(im, leftOnly)
	require.True(t, ok)
	assert.InDelta(t, 0.05, fc.Bg, 1e-4, "background must come from the admitted half only")
	assert.InDelta(t, 0.05, fc.Q[11], 1e-4, "even the high probes never see the excluded bright half")
	assert.Zero(t, fc.SatFrac)
}

func TestMeasureImageMasked_TooFewSamplesNotOK(t *testing.T) {
	im := gradientImage(64, 64, 0.05, 0.30) // 4096 px total < minMaskedSamples
	keepAll := func(int, int) bool { return true }

	_, ok := MeasureImageMasked(im, keepAll)
	assert.False(t, ok)

	_, ok = MeasureImageMasked(gradientImage(512, 512, 0, 0), nil)
	assert.False(t, ok, "nil mask is not measurable")
}
