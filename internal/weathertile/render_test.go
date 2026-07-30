package weathertile

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/weather"
)

// uniformGrid builds an n×n single-frame cube over bbox with each named layer a constant value.
func uniformGrid(bbox [4]float64, n int, layers map[string]float32) weather.Grid {
	g := weather.Grid{BBox: bbox, Nx: n, Ny: n, Timesteps: []int64{1000}, Layers: map[string][][]float32{}, IssuedMs: 1}
	for m, v := range layers {
		row := make([]float32, n*n)
		for i := range row {
			row[i] = v
		}
		g.Layers[m] = [][]float32{row}
	}
	return g
}

// firstPainted returns the RGBA of the first pixel with alpha>0 (scanning row-major), or ok=false.
func firstPainted(img interface{ PixOffset(x, y int) int }, pix []uint8) (r, g, b, a uint8, ok bool) {
	for oy := 0; oy < tileSize; oy++ {
		for ox := 0; ox < tileSize; ox++ {
			i := img.PixOffset(ox, oy)
			if pix[i+3] > 0 {
				return pix[i], pix[i+1], pix[i+2], pix[i+3], true
			}
		}
	}
	return 0, 0, 0, 0, false
}

func TestRenderTile_HumidityUniform(t *testing.T) {
	g := uniformGrid([4]float64{0, 0, 10, 10}, 4, map[string]float32{"humidity": 100})
	// z5 tile (16,15) covers ~lon[0,11.25], lat[0,11] — overlaps the cube.
	img, painted := RenderTile(g, "humidity", 0, 5, 16, 15)
	require.True(t, painted)
	require.NotNil(t, img)
	r, gg, b, a, ok := firstPainted(img, img.Pix)
	require.True(t, ok, "the covering tile has painted pixels")
	// Uniform 100% → the humidity ramp's top stop (232,70,70) at alpha ~0.72 (±dither).
	assert.InDelta(t, 232, r, 2)
	assert.InDelta(t, 70, gg, 2)
	assert.InDelta(t, 70, b, 2)
	assert.InDelta(t, 0.72*255, a, 4)
}

func TestRenderTile_OutsideHullTransparent(t *testing.T) {
	g := uniformGrid([4]float64{0, 0, 10, 10}, 4, map[string]float32{"humidity": 100})
	_, painted := RenderTile(g, "humidity", 0, 5, 0, 0) // near the antimeridian / far north — outside the cube
	assert.False(t, painted)
}

func TestRenderTile_MissingMetricTransparent(t *testing.T) {
	g := uniformGrid([4]float64{0, 0, 10, 10}, 4, map[string]float32{"humidity": 100})
	_, painted := RenderTile(g, "precip", 0, 5, 16, 15) // no precip layer in the cube
	assert.False(t, painted)
	_, painted2 := RenderTile(g, "humidity", 5, 5, 16, 15) // frame out of range
	assert.False(t, painted2)
}

func TestRenderTile_CloudBandsComposite(t *testing.T) {
	// A dense low deck, clear mid/high → the low band colour dominates.
	g := uniformGrid([4]float64{0, 0, 10, 10}, 4, map[string]float32{
		"clouds_low": 100, "clouds_mid": 0, "clouds_high": 0,
	})
	img, painted := RenderTile(g, "clouds", 0, 5, 16, 15)
	require.True(t, painted)
	r, _, _, a, ok := firstPainted(img, img.Pix)
	require.True(t, ok)
	assert.InDelta(t, 236, r, 2)      // clouds_low colour
	assert.InDelta(t, 0.95*255, a, 4) // its perceptual alpha at 100%
}

func TestRenderTile_CloudsSingleFallback(t *testing.T) {
	// A cube with only total "clouds" (no bands) → the single-cover fallback (grey, luminance≈250 at 100%).
	g := uniformGrid([4]float64{0, 0, 10, 10}, 4, map[string]float32{"clouds": 100})
	img, painted := RenderTile(g, "clouds", 0, 5, 16, 15)
	require.True(t, painted)
	r, gg, b, _, ok := firstPainted(img, img.Pix)
	require.True(t, ok)
	assert.InDelta(t, 250, r, 1)
	assert.Equal(t, r, gg)
	assert.Equal(t, r, b)
}

func TestRenderTile_StandaloneBandMetrics(t *testing.T) {
	// Each altitude band renders standalone with its composite colour, so a band overlay matches its
	// contribution inside the "clouds" render.
	g := uniformGrid([4]float64{0, 0, 10, 10}, 4, map[string]float32{
		"clouds_low": 100, "clouds_mid": 100, "clouds_high": 100,
	})
	for metric, wantR := range map[string]uint8{"clouds_low": 236, "clouds_mid": 205, "clouds_high": 190} {
		img, painted := RenderTile(g, metric, 0, 5, 16, 15)
		require.True(t, painted, metric)
		r, _, _, a, ok := firstPainted(img, img.Pix)
		require.True(t, ok, metric)
		assert.InDelta(t, wantR, r, 2, metric)
		assert.Greater(t, a, uint8(0), metric)
	}
}

func TestRenderTile_DewSpread(t *testing.T) {
	// Saturated air (spread ≈1 °C) paints strongly; dry air (spread ≥10 °C) is transparent — the ramp is
	// INVERSE of the % metrics.
	wet := uniformGrid([4]float64{0, 0, 10, 10}, 4, map[string]float32{"dewspread": 1})
	img, painted := RenderTile(wet, "dewspread", 0, 5, 16, 15)
	require.True(t, painted)
	_, _, _, a, ok := firstPainted(img, img.Pix)
	require.True(t, ok, "fog-risk air paints")
	assert.Greater(t, a, uint8(120), "small spread → strong overlay")

	dry := uniformGrid([4]float64{0, 0, 10, 10}, 4, map[string]float32{"dewspread": 10})
	img2, painted2 := RenderTile(dry, "dewspread", 0, 5, 16, 15)
	require.True(t, painted2, "the metric exists → the tile renders (transparent pixels)")
	// The ordered dither adds ±2/255 of alpha noise even at ramp-alpha 0 (same as the precip ramp's
	// zero stop), so "invisible" means ≤2, not exactly 0.
	maxA := uint8(0)
	for i := 3; i < len(img2.Pix); i += 4 {
		if img2.Pix[i] > maxA {
			maxA = img2.Pix[i]
		}
	}
	assert.LessOrEqual(t, maxA, uint8(2), "dry air (spread ≥8 °C) is visually transparent")
}

func TestRampAt(t *testing.T) {
	// Below/above the range clamps; a midpoint interpolates.
	assert.Equal(t, uint8(80), rampAt(humidityRamp, 0).r) // clamps to the 50% stop
	assert.Equal(t, uint8(232), rampAt(humidityRamp, 100).r)
	mid := rampAt(humidityRamp, 60) // halfway 50→70
	assert.InDelta(t, (80+150)/2, mid.r, 1)
	assert.InDelta(t, (0.25+0.42)/2, mid.a, 1e-6)
}
