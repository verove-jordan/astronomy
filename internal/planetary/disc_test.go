package planetary

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// drawMoon renders a synthetic Moon for the disc-fit and earthshine tests: a disc of radius r at
// (cx,cy) with a ~1.5 px antialiased limb and an elliptical terminator. litK ∈ [-1,1] places the
// terminator (-1 = full moon, 0 = half, 0.8 = thin crescent; the +x side is lit), softened over
// ~6 px. Noise is a fixed-seed LCG, so fixtures are fully deterministic.
func drawMoon(w, h int, cx, cy, r, litK, litVal, darkVal, skyVal, noiseAmp float64) *fits.Image {
	im := fits.NewImage(w, h, 1)
	p := im.Pix[0]
	rng := uint32(1)
	const termSoft = 6.0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			d := math.Hypot(float64(x)-cx, float64(y)-cy)
			disc := smoothstep((r - d) / 1.5)
			yy := float64(y) - cy
			halfW := 0.0
			if a := r*r - yy*yy; a > 0 {
				halfW = math.Sqrt(a)
			}
			lit := smoothstep((float64(x)-cx-litK*halfW)/termSoft + 0.5)
			v := skyVal*(1-disc) + disc*(darkVal+(litVal-darkVal)*lit)
			rng = rng*1664525 + 1013904223
			n := (float64(rng>>8)/float64(1<<24) - 0.5) * 2 * noiseAmp
			p[y*w+x] = float32(v + n)
		}
	}
	return im
}

func TestFitLunarDisc(t *testing.T) {
	const w, h = 2048, 2048
	tests := []struct {
		name            string
		cx, cy, r, litK float64
	}{
		{"full moon", 1024, 1024, 700, -1},
		{"gibbous", 1024, 1024, 700, -0.5},
		{"half", 1024, 1024, 700, 0},
		{"crescent", 1024, 1024, 700, 0.6},
		{"thin crescent", 1024, 1024, 700, 0.8},
		{"off-centre", 640, 1400, 560, 0.5},
		{"partly out of frame", 300, 1024, 700, 0.4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			im := drawMoon(w, h, tt.cx, tt.cy, tt.r, tt.litK, 0.8, 0.01, 0.001, 0.002)
			fit, ok := fitLunarDisc(im)
			require.True(t, ok, "disc must be found")
			assert.InDelta(t, tt.cx, fit.CX, 5, "centre x")
			assert.InDelta(t, tt.cy, fit.CY, 5, "centre y")
			assert.InDelta(t, tt.r, fit.R, 6, "radius")
		})
	}
}

func TestFitLunarDisc_Rejects(t *testing.T) {
	t.Run("flat noise", func(t *testing.T) {
		im := drawMoon(512, 512, 256, 256, 0, -1, 0, 0, 0.02, 0.01)
		_, ok := fitLunarDisc(im)
		assert.False(t, ok, "pure noise must not fit a disc")
	})
	t.Run("tiny dot", func(t *testing.T) {
		im := drawMoon(2048, 2048, 1024, 1024, 20, -1, 0.8, 0.01, 0.001, 0.002)
		_, ok := fitLunarDisc(im)
		assert.False(t, ok, "a 20 px dot is below the minimum lunar radius")
	})
}

func TestFitLunarDisc_Deterministic(t *testing.T) {
	im := drawMoon(2048, 2048, 1024, 1024, 700, 0.6, 0.8, 0.01, 0.001, 0.002)
	a, ok1 := fitLunarDisc(im)
	b, ok2 := fitLunarDisc(im)
	require.True(t, ok1)
	require.True(t, ok2)
	assert.Equal(t, a, b, "same input must fit the same circle (no randomness)")
}
