package lightpollution

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBlackMarbleToSQM(t *testing.T) {
	assert.InDelta(t, pristineSQM, blackMarbleToSQM(0), 1e-9) // darkest → pristine
	assert.InDelta(t, 17.0, blackMarbleToSQM(255), 1e-9)      // brightest city → strongly polluted
	assert.InDelta(t, 19.5, blackMarbleToSQM(255*0.25), 1e-6) // 22 − 5·√0.25

	// Monotonically darker (higher SQM) as the pixel gets dimmer.
	assert.Greater(t, blackMarbleToSQM(10), blackMarbleToSQM(200))
}

func TestMercatorTilePixel(t *testing.T) {
	// At zoom 0 the whole world is one tile; (0,0) is its centre pixel.
	xt, yt, px, py := mercatorTilePixel(0, 0, 0)
	assert.Equal(t, 0, xt)
	assert.Equal(t, 0, yt)
	assert.Equal(t, 128, px)
	assert.Equal(t, 128, py)

	// A real site stays within the tile/pixel ranges and is deterministic.
	xt, yt, px, py = mercatorTilePixel(48.86, 2.35, sampleZoom)
	n := 1 << sampleZoom
	assert.GreaterOrEqual(t, xt, 0)
	assert.Less(t, xt, n)
	assert.GreaterOrEqual(t, yt, 0)
	assert.Less(t, yt, n)
	assert.GreaterOrEqual(t, px, 0)
	assert.LessOrEqual(t, px, 255)
	assert.GreaterOrEqual(t, py, 0)
	assert.LessOrEqual(t, py, 255)
}
