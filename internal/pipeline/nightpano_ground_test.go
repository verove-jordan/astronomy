package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/skypano"
)

func TestCompositeGround(t *testing.T) {
	const w, h = 4, 1
	// A canvas of four pixels: two marked as real sky, two not. The unmarked ones still hold a bright
	// value, because that is the situation under an arch's horizon — clearBelowHorizon clears the
	// COVERAGE, and the smeared stacked landscape Render put in the pixels stays exactly where it is.
	newCanvas := func() (*fits.Image, []bool) {
		img := fits.NewImage(w, h, 3)
		keep := []bool{true, true, false, false}
		vals := []float32{0.2, 0.2, 9.0, 9.0}
		for c := 0; c < 3; c++ {
			copy(img.Pix[c], vals)
		}
		return img, keep
	}
	groundLayer := func(weight []float32, val float32) *archLayer {
		g := fits.NewImage(w, h, 3)
		for c := 0; c < 3; c++ {
			for i := range g.Pix[c] {
				g.Pix[c][i] = val
			}
		}
		return &archLayer{Img: g, Weight: weight}
	}

	t.Run("over a real sky the two layers mix by weight", func(t *testing.T) {
		img, keep := newCanvas()
		compositeGround(img, keep, groundLayer([]float32{1, 0.5, 0, 0}, 0.4))

		assert.InDelta(t, 0.4, img.Pix[0][0], 1e-6, "full weight must be the landscape alone")
		assert.InDelta(t, 0.5*0.2+0.5*0.4, img.Pix[0][1], 1e-6, "half weight must be a half-and-half mix")
	})

	// The pale frame: fading the landscape toward an uncovered canvas pixel mixes in its blow-up.
	t.Run("where the sky is not real the landscape never mixes with it", func(t *testing.T) {
		img, keep := newCanvas()
		compositeGround(img, keep, groundLayer([]float32{0, 0, 1, 0.25}, 0.4))

		assert.InDelta(t, 0.4, img.Pix[0][2], 1e-6, "full weight over no sky must be the landscape")
		assert.InDelta(t, 0.25*0.4, img.Pix[0][3], 1e-6,
			"partial weight over no sky must fade toward BLACK, not toward the uncovered value")
		assert.Less(t, img.Pix[0][3], float32(0.4), "the fade must darken, never brighten")
	})

	t.Run("every painted pixel is kept, and nothing else is", func(t *testing.T) {
		img, keep := newCanvas()
		compositeGround(img, keep, groundLayer([]float32{0, 0, 0.3, 0}, 0.4))

		assert.Equal(t, []bool{true, true, true, false}, keep)
	})

	t.Run("unpainted pixels are left exactly alone", func(t *testing.T) {
		img, keep := newCanvas()
		want := append([]float32(nil), img.Pix[0]...)

		compositeGround(img, keep, groundLayer([]float32{0, 0, 0, 0}, 0.4))

		assert.Equal(t, want, img.Pix[0])
	})

	t.Run("a mismatched layer is a no-op rather than a panic", func(t *testing.T) {
		img, keep := newCanvas()
		want := append([]float32(nil), img.Pix[0]...)

		require.NotPanics(t, func() { compositeGround(img, keep, groundLayer([]float32{1}, 0.4)) })
		require.NotPanics(t, func() { compositeGround(img, keep, nil) })
		require.NotPanics(t, func() { compositeGround(nil, keep, groundLayer([]float32{1, 1, 1, 1}, 0.4)) })
		assert.Equal(t, want, img.Pix[0])
	})
}

func TestClearBelowHorizon(t *testing.T) {
	c := skypano.Canvas{
		Proj: skypano.Stereographic, Fr: skypano.Horizon,
		W: 201, H: 201, Lon0: 180, Lat0: 20, ScaleDegPerPix: 0.3,
		SiteLatDeg: 43.5, LSTDeg: 300,
	}
	full := func() []float32 {
		cov := make([]float32, c.W*c.H)
		for i := range cov {
			cov[i] = 1
		}
		return cov
	}

	t.Run("every cleared pixel is below the horizon and every kept one is above", func(t *testing.T) {
		cov := full()

		n := clearBelowHorizon(cov, c, nil)

		require.Positive(t, n, "a canvas centred 20 degrees up at 0.3 deg/px must reach under the horizon")
		for y := 0; y < c.H; y++ {
			for x := 0; x < c.W; x++ {
				alt, ok := c.AltitudeAt(float64(x)+0.5, float64(y)+0.5)
				below := !ok || alt < 0
				if below {
					require.Zero(t, cov[y*c.W+x], "pixel (%d,%d) at alt %.2f was left as sky", x, y, alt)
				} else {
					require.Equal(t, float32(1), cov[y*c.W+x], "pixel (%d,%d) at alt %.2f was cleared", x, y, alt)
				}
			}
		}
	})

	t.Run("it never resurrects coverage that was already zero", func(t *testing.T) {
		cov := full()
		for i := range cov {
			cov[i] = 0
		}
		assert.Zero(t, clearBelowHorizon(cov, c, nil), "already-empty coverage is not newly cleared")
	})

	t.Run("it leaves canvases that have no horizon alone", func(t *testing.T) {
		d := c
		d.Fr = skypano.Equatorial
		cov := full()
		assert.Zero(t, clearBelowHorizon(cov, d, nil))
		for i, v := range cov {
			require.Equal(t, float32(1), v, "pixel %d was cleared on a frame with no horizon", i)
		}
	})

	t.Run("a mismatched coverage is a no-op", func(t *testing.T) {
		assert.Zero(t, clearBelowHorizon(make([]float32, 3), c, nil))
	})

	// The dark band between the sea and the sky: the geometric horizon and the landscape's own
	// (gravity-derived) horizon are not the same line, so a strip ends up covered by neither and the
	// landscape's feather is faded to black across it.
	t.Run("sky is kept where the landscape is only partway in", func(t *testing.T) {
		cov := full()
		w := make([]float32, len(cov))
		var below, feather int
		for y := 0; y < c.H; y++ {
			for x := 0; x < c.W; x++ {
				i := y*c.W + x
				if alt, ok := c.AltitudeAt(float64(x)+0.5, float64(y)+0.5); ok && alt < 0 {
					below++
					if below%3 == 0 {
						w[i] = 0.4 // partway in
						feather++
					}
				}
			}
		}
		require.Positive(t, feather)

		clearBelowHorizon(cov, c, &archLayer{Weight: w})

		kept := 0
		for i, v := range w {
			if v > 0 && v < 1 && cov[i] > 0 {
				kept++
			}
		}
		assert.Equal(t, feather, kept, "every feathered pixel must keep its sky to blend with")
	})
}
