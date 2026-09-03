package skypano

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// skyWithStars builds a panel of ordinary sky: a faint background with point sources everywhere.
func skyWithStars(w, h int) *fits.Image {
	im := fits.NewImage(w, h, 3)
	for i := range im.Pix[0] {
		for c := 0; c < 3; c++ {
			im.Pix[c][i] = 0.05
		}
	}
	// A deterministic scatter of stars, dense enough that every tile gets several.
	s := uint32(0x1234567)
	for n := 0; n < w*h/220; n++ {
		s ^= s << 13
		s ^= s >> 17
		s ^= s << 5
		x := int(s % uint32(w))
		s ^= s << 13
		s ^= s >> 17
		s ^= s << 5
		y := int(s % uint32(h))
		starAt(im, float64(x), float64(y), 0.8)
	}
	return im
}

// blotOut replaces a region with a smooth object: bright, featureless, no stars.
func blotOut(im *fits.Image, x0, y0, x1, y1 int, level float32) {
	for y := y0; y < y1 && y < im.H; y++ {
		for x := x0; x < x1 && x < im.W; x++ {
			for c := 0; c < im.C; c++ {
				im.Pix[c][y*im.W+x] = level
			}
		}
	}
}

func TestFindOccluders(t *testing.T) {
	const w, h = 512, 512
	o := DefaultOccluderOptions()

	t.Run("clean sky yields no mask at all", func(t *testing.T) {
		im := skyWithStars(w, h)
		assert.Nil(t, FindOccluders(im, o), "ordinary sky must not be masked")
	})

	// The case this exists for: something leaning into a corner, brighter than the sky and starless.
	t.Run("an object in a corner is masked and the sky is not", func(t *testing.T) {
		im := skyWithStars(w, h)
		blotOut(im, 340, 340, w, h, 0.35)

		mask := FindOccluders(im, o)
		require.NotNil(t, mask, "the object should have been found")

		assert.Zero(t, mask[420*w+420], "the middle of the object must be excluded")
		assert.InDelta(t, 1.0, float64(mask[80*w+80]), 1e-6, "sky far from the object must be untouched")
	})

	t.Run("a DARK starless object is found just as well as a bright one", func(t *testing.T) {
		im := skyWithStars(w, h)
		blotOut(im, 0, 380, 200, h, 0.005) // a dark shape along the bottom-left edge

		mask := FindOccluders(im, o)
		require.NotNil(t, mask)
		assert.Zero(t, mask[470*w+100], "the dark object must be excluded too")
	})

	// Border-connectedness is the guard against eating real sky structure.
	t.Run("a starless patch in the MIDDLE is left alone", func(t *testing.T) {
		im := skyWithStars(w, h)
		blotOut(im, 210, 210, 300, 300, 0.35) // floating, touching no edge

		mask := FindOccluders(im, o)
		if mask != nil {
			assert.InDelta(t, 1.0, float64(mask[255*w+255]), 1e-6,
				"a patch that reaches no edge is not something in the way")
		}
	})

	t.Run("a tiny nick at the edge is below the area floor", func(t *testing.T) {
		im := skyWithStars(w, h)
		blotOut(im, 0, 0, 24, 24, 0.35)
		assert.Nil(t, FindOccluders(im, o))
	})

	t.Run("the mask edge is feathered rather than a step", func(t *testing.T) {
		im := skyWithStars(w, h)
		blotOut(im, 340, 340, w, h, 0.35)

		mask := FindOccluders(im, o)
		require.NotNil(t, mask)
		partial := 0
		for _, v := range mask {
			if v > 0.02 && v < 0.98 {
				partial++
			}
		}
		assert.Greater(t, partial, 1000, "there should be a ramp, not a hard rim")
	})

	t.Run("a degenerate input is refused rather than panicking", func(t *testing.T) {
		require.NotPanics(t, func() { FindOccluders(nil, o) })
		bad := o
		bad.TilePx = 1
		require.NotPanics(t, func() { assert.Nil(t, FindOccluders(skyWithStars(64, 64), bad)) })
		flat := fits.NewImage(64, 64, 3) // all zero: no star energy anywhere
		require.NotPanics(t, func() { _ = FindOccluders(flat, o) })
	})
}

// The regression this guards: the panel carrying the horizon reported a third of itself blocked —
// its beach plus a lot of thin off-band sky — and dropping that much of one panel re-shuffled which
// panels cover the overlaps, which put rectangular seams across the finished canvas.
func TestFindOccluders_RefusesToMaskMostOfAPanel(t *testing.T) {
	const w, h = 512, 512
	o := DefaultOccluderOptions()
	im := skyWithStars(w, h)
	blotOut(im, 0, 260, w, h, 0.30) // a "landscape" filling the bottom half

	assert.Nil(t, FindOccluders(im, o),
		"a finding this large is a bad measurement, not a big object — the horizon clearing owns that case")

	relaxed := o
	relaxed.MaxAreaFrac = 0.9
	assert.NotNil(t, FindOccluders(im, relaxed), "raising the cap must let the same finding through")
}
