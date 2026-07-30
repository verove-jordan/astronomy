package fits

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResize(t *testing.T) {
	// A horizontal linear ramp survives bilinear resampling in both directions.
	im := NewImage(8, 4, 1)
	for y := 0; y < 4; y++ {
		for x := 0; x < 8; x++ {
			im.Pix[0][y*8+x] = float32(x) / 7
		}
	}

	assert.Same(t, im, im.Resize(8, 4), "same size returns the image untouched")

	down := im.Resize(4, 2)
	assert.Equal(t, 4, down.W)
	assert.Equal(t, 2, down.H)
	for x := 0; x < 4; x++ {
		// Pixel-centre sampling maps x' to (2x'+0.5)-0.5 = 2x' in source coords... within the ramp
		// any sampled position must stay on the line value = srcX/7.
		want := (float32(x)+0.5)*2/7 - 0.5/7
		assert.InDelta(t, want, down.Pix[0][x], 1e-4, "column %d", x)
	}

	up := down.Resize(8, 4)
	assert.Equal(t, 8, up.W)
	// Interior values return close to the original ramp (edges are clamped).
	for x := 1; x < 7; x++ {
		assert.InDelta(t, im.Pix[0][x], up.Pix[0][x], 0.08, "column %d", x)
	}
}
