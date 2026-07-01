package planetary

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/fits"
)

func TestCubicWeights_PartitionOfUnity(t *testing.T) {
	for _, tt := range []float64{0, 0.25, 0.5, 0.75, 0.9} {
		w := cubicWeights(tt)
		assert.InDelta(t, 1.0, w[0]+w[1]+w[2]+w[3], 1e-9, "Catmull-Rom taps sum to 1 at t=%v", tt)
	}
}

func TestSampleCubic_IntegerExact(t *testing.T) {
	const w, h = 24, 18
	src := make([]float32, w*h)
	for i := range src {
		src[i] = float32(math.Sin(float64(i))) // arbitrary content
	}
	for _, p := range []struct{ x, y int }{{0, 0}, {5, 7}, {23, 17}, {12, 3}} {
		got := sampleCubic(src, w, h, float64(p.x), float64(p.y))
		assert.InDelta(t, float64(src[p.y*w+p.x]), float64(got), 1e-6,
			"integer sample must return the exact pixel at (%d,%d)", p.x, p.y)
	}
}

func TestCubicShift_IntegerShiftMovesContent(t *testing.T) {
	im := fits.NewImage(40, 30, 1)
	im.Pix[0][10*40+10] = 1.0
	out := cubicShift(im, 5, 3) // content moves right 5, down 3 → bright pixel at (15,13)
	assert.InDelta(t, 1.0, float64(out.Pix[0][13*40+15]), 1e-6, "bright pixel at (15,13)")
	assert.InDelta(t, 0.0, float64(out.Pix[0][10*40+10]), 1e-6, "original location empty")
}

// A half-pixel shift is the worst case for interpolation low-pass. On a half-Nyquist sine (period 4 px),
// Catmull-Rom must retain markedly more high-frequency amplitude than bilinear — the crux of the fix
// (three compounded bilinear passes were smearing the crater detail away).
func TestSampleCubic_RetainsHighFreqVsBilinear(t *testing.T) {
	const w, h = 128, 8
	src := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			src[y*w+x] = float32(math.Sin(2 * math.Pi * float64(x) / 4)) // period 4 px = half-Nyquist
		}
	}
	var cubicVar, biVar, origVar float64
	for y := 2; y < h-2; y++ {
		for x := 4; x < w-4; x++ {
			o := float64(src[y*w+x])
			origVar += o * o
			c := float64(sampleCubic(src, w, h, float64(x)+0.5, float64(y)))
			cubicVar += c * c
			b := bilinearSample(src, w, h, float64(x)+0.5, float64(y))
			biVar += b * b
		}
	}
	assert.Greater(t, cubicVar, biVar, "Catmull-Rom retains more half-Nyquist energy than bilinear")
	assert.Greater(t, cubicVar, 1.25*biVar, "and markedly more (≈0.78 vs ≈0.5 of the original power)")
	assert.Less(t, biVar, origVar, "bilinear low-passes the half-Nyquist sine")
}

// bilinearSample mirrors the old planetary resampler (edge-clamped) for the A/B comparison above.
func bilinearSample(src []float32, w, h int, sx, sy float64) float64 {
	x0 := int(math.Floor(sx))
	y0 := int(math.Floor(sy))
	fx := sx - float64(x0)
	fy := sy - float64(y0)
	at := func(x, y int) float64 { return float64(src[clampInt(y, 0, h-1)*w+clampInt(x, 0, w-1)]) }
	top := at(x0, y0)*(1-fx) + at(x0+1, y0)*fx
	bot := at(x0, y0+1)*(1-fx) + at(x0+1, y0+1)*fx
	return top*(1-fy) + bot*fy
}
