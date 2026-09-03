package skypano

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// starAt paints a small gaussian, the way a star lands on a canvas.
func starAt(im *fits.Image, cx, cy, amp float64) {
	const sigma = 1.6
	for y := int(cy) - 6; y <= int(cy)+6; y++ {
		for x := int(cx) - 6; x <= int(cx)+6; x++ {
			if x < 0 || y < 0 || x >= im.W || y >= im.H {
				continue
			}
			d2 := (float64(x)-cx)*(float64(x)-cx) + (float64(y)-cy)*(float64(y)-cy)
			v := float32(amp * math.Exp(-d2/(2*sigma*sigma)))
			for c := 0; c < im.C; c++ {
				im.Pix[c][y*im.W+x] += v
			}
		}
	}
}

// peakAndValley reports the brightest pixel of a star and the dip between a doubled pair, measured
// along the row through them. A single star has no dip; a doubled one does.
func peakAndValley(im *fits.Image, y, x0, x1 int) (peak, valley float32) {
	valley = float32(math.MaxFloat32)
	for x := x0; x <= x1; x++ {
		v := im.Pix[0][y*im.W+x]
		if v > peak {
			peak = v
		}
	}
	mid := (x0 + x1) / 2
	for x := mid - 1; x <= mid+1; x++ {
		if v := im.Pix[0][y*im.W+x]; v < valley {
			valley = v
		}
	}
	return peak, valley
}

func TestBlendTwoBand_RemovesTheDoubledStar(t *testing.T) {
	const w, h = 120, 60
	const y = 30
	// Two panels that disagree about where a star is by 6 px — the situation a pair of independently
	// solved panels actually produces, at the scale measured on the real panorama.
	const xa, xb = 57.0, 63.0

	// What the weighted average draws: both stars, half strength each.
	avg := fits.NewImage(w, h, 3)
	starAt(avg, xa, y, 0.5)
	starAt(avg, xb, y, 0.5)
	// What the best-covering panel alone says: one star.
	best := make([][]float32, 3)
	one := fits.NewImage(w, h, 3)
	starAt(one, xa, y, 1.0)
	for c := 0; c < 3; c++ {
		best[c] = one.Pix[c]
	}
	// A smooth background difference between them, which the blend must NOT keep from the single
	// panel — that is the seam the average exists to hide.
	for i := range avg.Pix[0] {
		for c := 0; c < 3; c++ {
			avg.Pix[c][i] += 0.20
			best[c][i] += 0.05
		}
	}
	weight := make([]float32, w*h)
	for i := range weight {
		weight[i] = 1
	}

	// Before: the pair has a dip between the two peaks.
	p0, v0 := peakAndValley(avg, y, int(xa)-4, int(xb)+4)
	require.Less(t, v0, p0*0.95, "the fixture should start doubled")

	blendTwoBand(avg, best, weight, 16)

	t.Run("the star is single again", func(t *testing.T) {
		p1, v1 := peakAndValley(avg, y, int(xa)-4, int(xb)+4)
		assert.Greater(t, p1, p0, "the surviving star should be at full strength, not half")
		// A single gaussian rises monotonically to its peak: no dip in the middle of the pair.
		assert.Less(t, math.Abs(float64(v1-p1))/float64(p1), 0.99,
			"there should no longer be two peaks with a valley between them")
		// The ghost at xb must be gone: that column should now be background, not a second star.
		ghost := avg.Pix[0][y*w+int(xb)+1]
		bg := avg.Pix[0][y*w+5]
		assert.InDelta(t, float64(bg), float64(ghost), 0.05, "the second image of the star survived")
	})

	t.Run("the smooth level comes from the average, not the single panel", func(t *testing.T) {
		// Far from the star the answer must be the AVERAGE's background (0.20), not the panel's (0.05).
		assert.InDelta(t, 0.20, float64(avg.Pix[0][y*w+5]), 0.02)
	})
}

func TestBlendTwoBand_IsANoOpWhenDisabled(t *testing.T) {
	const w, h = 40, 20
	im := fits.NewImage(w, h, 3)
	starAt(im, 20, 10, 1)
	want := append([]float32(nil), im.Pix[0]...)
	best := make([][]float32, 3)
	for c := range best {
		best[c] = make([]float32, w*h)
	}
	weight := make([]float32, w*h)
	for i := range weight {
		weight[i] = 1
	}

	blendTwoBand(im, best, weight, 0) // radius 0 = off

	assert.Equal(t, want, im.Pix[0])
}

func TestMaskedBoxBlur_IgnoresUncoveredPixels(t *testing.T) {
	const w, h = 20, 1
	p := make([]float32, w*h)
	covered := make([]float32, w*h)
	for x := 0; x < 10; x++ { // left half covered, value 1; right half uncovered, value 0
		p[x], covered[x] = 1, 1
	}

	got := maskedBoxBlur(p, covered, w, h, 3)

	for x := 0; x < 10; x++ {
		assert.InDelta(t, 1.0, float64(got[x]), 1e-5,
			"pixel %d was dragged toward the uncovered side", x)
	}
}
