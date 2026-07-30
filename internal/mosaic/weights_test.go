package mosaic

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

func flatMono(w, h int, v float32) *fits.Image {
	im := fits.NewImage(w, h, 1)
	for i := range im.Pix[0] {
		im.Pix[0][i] = v
	}
	return im
}

func TestBuildWeights_FeatherProfile(t *testing.T) {
	const w, h = 100, 60
	wm := BuildWeights(flatMono(w, h, 0.5), 0.6, 0.2, 4)
	require.Equal(t, w, wm.W)
	require.Equal(t, h, wm.H)

	at := func(x, y int) float32 { return wm.Pix[y*w+x] }
	// Zero at the border (the image edge counts as the footprint edge).
	for x := 0; x < w; x++ {
		assert.Zero(t, at(x, 0), "top border must be 0")
		assert.Zero(t, at(x, h-1), "bottom border must be 0")
	}
	// Eroded band (edgeErodePx=4) stays 0 just inside the border.
	assert.Zero(t, at(3, h/2))
	// Monotone non-decreasing from the left edge to the center.
	prev := float32(-1)
	for x := 0; x <= w/2; x++ {
		v := at(x, h/2)
		assert.GreaterOrEqual(t, v, prev, "weight must rise monotonically from the edge (x=%d)", x)
		prev = v
	}
	// Plateau of exactly 1 in the interior.
	assert.Equal(t, float32(1), at(w/2, h/2))
	assert.Equal(t, float32(1), at(w/2-5, h/2))
	// Ramp values strictly between 0 and 1 exist.
	assert.Greater(t, at(8, h/2), float32(0))
	assert.Less(t, at(8, h/2), float32(1))
}

func TestBuildWeights_HoleFeathersAround(t *testing.T) {
	const w, h = 96, 96
	im := flatMono(w, h, 0.5)
	for y := 40; y < 48; y++ {
		for x := 40; x < 48; x++ {
			im.Pix[0][y*w+x] = 0 // dead block inside the footprint
		}
	}
	wm := BuildWeights(im, 0.6, 0.2, 2)
	at := func(x, y int) float32 { return wm.Pix[y*w+x] }
	assert.Zero(t, at(44, 44), "hole itself has zero weight")
	assert.Zero(t, at(49, 44), "eroded ring around the hole is zero")
	ramp := at(53, 44)
	assert.Greater(t, ramp, float32(0), "feather ramp beyond the erosion")
	assert.Less(t, ramp, float32(1))
	assert.Equal(t, float32(1), at(20, 20), "far interior stays at full weight")
}

func TestBuildWeights_ZeroRegionIsOutsideFootprint(t *testing.T) {
	const w, h = 80, 80
	im := flatMono(w, h, 0.3)
	for y := 0; y < h; y++ {
		for x := 0; x < 12; x++ {
			im.Pix[0][y*w+x] = 0 // ragged registration edge: left band empty
		}
	}
	wm := BuildWeights(im, 0.6, 0.2, 4)
	for y := 0; y < h; y++ {
		for x := 0; x < 12; x++ {
			require.Zero(t, wm.Pix[y*w+x], "empty region must carry no weight (%d,%d)", x, y)
		}
	}
	assert.Zero(t, wm.Pix[40*w+14], "eroded band beyond the empty region is zero")
	assert.Greater(t, wm.Pix[40*w+22], float32(0), "feather resumes past the erosion")
}
