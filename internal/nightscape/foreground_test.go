package nightscape

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// frameWithSkyAndGround is a foreground frame shaped like the real thing: a bright sky over a dark
// landscape. The ratio between them is what the white point has to respect — on the run this was
// written for the sky sat about 35x above the ground.
func frameWithSkyAndGround(w, h int, sky, groundLo, groundHi float32) *fits.Image {
	im := fits.NewImage(w, h, 3)
	horizon := h * 2 / 3
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := sky
			if y >= horizon {
				v = groundLo + (groundHi-groundLo)*float32(x)/float32(w)
			}
			for c := 0; c < 3; c++ {
				im.Pix[c][y*w+x] = v
			}
		}
	}
	return im
}

func TestForegroundWhitePoint_IsSetByTheSkyInTheFrame(t *testing.T) {
	im := frameWithSkyAndGround(120, 120, 0.066, 0.0015, 0.0025)

	white := ForegroundWhitePoint(im, 99.9)

	assert.InDelta(t, 0.066, white, 1e-4,
		"the white point must come from the sky, not the landscape")
	assert.Nil(t, nil)
	assert.Zero(t, ForegroundWhitePoint(nil, 99.9))
}

func TestStretchForeground(t *testing.T) {
	const w, h = 120, 120
	const sky, gLo, gHi = float32(0.066), float32(0.0015), float32(0.0025)

	groundRow := func(im *fits.Image) []float32 {
		y := h*2/3 + 5
		return im.Pix[0][y*w : (y+1)*w]
	}

	// The defect this guards: normalise by the landscape's OWN range and it fills 0..1, then asinh
	// lifts it to a flat pale slab. Normalised against the frame's sky it stays a dark landscape.
	t.Run("a dark landscape stays dark when normalised against the frame", func(t *testing.T) {
		im := frameWithSkyAndGround(w, h, sky, gLo, gHi)
		white := ForegroundWhitePoint(im, 99.9)

		StretchForeground(im, white, 6)

		row := groundRow(im)
		for x, v := range row {
			assert.Less(t, v, float32(0.55), "landscape pixel %d was stretched to near white (%.3f)", x, v)
			assert.Greater(t, v, float32(0.01), "landscape pixel %d was crushed to black (%.3f)", x, v)
		}
	})

	t.Run("normalising by the landscape alone blows it out", func(t *testing.T) {
		framed := frameWithSkyAndGround(w, h, sky, gLo, gHi)
		alone := frameWithSkyAndGround(w, h, sky, gLo, gHi)

		StretchForeground(framed, ForegroundWhitePoint(framed, 99.9), 6)
		StretchForeground(alone, float64(gHi), 6) // the white point measured over the ground only

		fr, al := groundRow(framed), groundRow(alone)
		assert.Less(t, fr[w/2], al[w/2],
			"the frame-normalised landscape must be darker than the landscape-normalised one")
		assert.Greater(t, al[w-1], float32(0.9), "the landscape-only normalisation should indeed clip")
	})

	t.Run("the curve stays monotonic across the landscape", func(t *testing.T) {
		im := frameWithSkyAndGround(w, h, sky, gLo, gHi)

		StretchForeground(im, ForegroundWhitePoint(im, 99.9), 6)

		row := groundRow(im)
		for x := 1; x < len(row); x++ {
			require.GreaterOrEqual(t, row[x], row[x-1], "the curve reversed at x=%d", x)
		}
	})

	t.Run("a white point of zero or less is a no-op rather than a divide by zero", func(t *testing.T) {
		im := frameWithSkyAndGround(w, h, sky, gLo, gHi)
		want := append([]float32(nil), im.Pix[0]...)

		StretchForeground(im, 0, 6)
		assert.Equal(t, want, im.Pix[0])

		StretchForeground(im, -1, 6)
		assert.Equal(t, want, im.Pix[0])

		require.NotPanics(t, func() { StretchForeground(nil, 1, 6) })
	})
}
