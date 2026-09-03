package skypano

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// newMasked builds a one-channel test canvas from a value function and a mask function.
func newMasked(w, h int, val func(x, y int) float32, covered func(x, y int) bool) (*fits.Image, []float32) {
	im := fits.NewImage(w, h, 1)
	mask := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if covered(x, y) {
				im.Pix[0][i], mask[i] = val(x, y), 1
			}
		}
	}
	return im, mask
}

func TestFillHoles(t *testing.T) {
	const w, h = 64, 64

	t.Run("covered pixels are left exactly as they were", func(t *testing.T) {
		val := func(x, y int) float32 { return float32(x*7+y*13%11) / 100 }
		im, mask := newMasked(w, h, val, func(x, y int) bool { return x < w/2 })
		want := append([]float32(nil), im.Pix[0]...)

		FillHoles(im, mask)

		for i, m := range mask {
			if m > 0 {
				require.Equal(t, want[i], im.Pix[0][i], "covered pixel %d was modified", i)
			}
		}
	})

	t.Run("a hole is filled from the level around it", func(t *testing.T) {
		im, mask := newMasked(w, h, func(x, y int) float32 { return 0.5 },
			func(x, y int) bool { return x < 24 || x >= 40 || y < 24 || y >= 40 })

		FillHoles(im, mask)

		for y := 24; y < 40; y++ {
			for x := 24; x < 40; x++ {
				assert.InDelta(t, 0.5, im.Pix[0][y*w+x], 1e-3, "hole pixel (%d,%d)", x, y)
			}
		}
	})

	// This is the whole reason the file exists: left as zeros, a background model fitted over the
	// surround dives toward it and lays a dark rim just inside the data.
	t.Run("the empty side does not dive toward zero", func(t *testing.T) {
		im, mask := newMasked(w, h, func(x, y int) float32 { return 1 },
			func(x, y int) bool { return x < w/2 })

		FillHoles(im, mask)

		for y := 0; y < h; y++ {
			for x := w / 2; x < w; x++ {
				assert.InDelta(t, 1.0, im.Pix[0][y*w+x], 1e-3, "filled pixel (%d,%d)", x, y)
			}
		}
	})

	t.Run("a gradient across the boundary steps only slightly, and never overshoots", func(t *testing.T) {
		// A horizontal ramp on the left half. Push-pull relaxes toward the local mean rather than
		// continuing the slope, so a step is expected — what matters is that it is a fraction of the
		// level rather than a fall to zero, and that nothing is invented outside the known range.
		im, mask := newMasked(w, h, func(x, y int) float32 { return 0.2 + 0.8*float32(x)/float32(w) },
			func(x, y int) bool { return x < w/2 })
		lo, hi := float32(0.2), float32(0.2+0.8*31.0/64.0)

		FillHoles(im, mask)

		for y := 0; y < h; y++ {
			last := im.Pix[0][y*w+w/2-1]
			first := im.Pix[0][y*w+w/2]
			assert.Less(t, float64(last-first), 0.3*float64(last),
				"row %d stepped down by more than 30%% of the level at the boundary", y)
			for x := w / 2; x < w; x++ {
				v := im.Pix[0][y*w+x]
				assert.GreaterOrEqual(t, v, lo, "filled pixel (%d,%d) undershot the known range", x, y)
				assert.LessOrEqual(t, v, hi, "filled pixel (%d,%d) overshot the known range", x, y)
			}
		}
	})

	t.Run("an all-empty mask leaves the plane untouched", func(t *testing.T) {
		im, mask := newMasked(w, h, func(x, y int) float32 { return 0.3 }, func(x, y int) bool { return false })
		for i := range im.Pix[0] {
			im.Pix[0][i] = 0.3 // values with no mask at all: nothing to extrapolate from
		}
		want := append([]float32(nil), im.Pix[0]...)

		FillHoles(im, mask)

		assert.Equal(t, want, im.Pix[0])
	})

	t.Run("every channel is filled", func(t *testing.T) {
		im := fits.NewImage(w, h, 3)
		mask := make([]float32, w*h)
		for y := 0; y < h; y++ {
			for x := 0; x < w/2; x++ {
				i := y*w + x
				mask[i] = 1
				for c := 0; c < 3; c++ {
					im.Pix[c][i] = float32(c+1) / 4
				}
			}
		}

		FillHoles(im, mask)

		for c := 0; c < 3; c++ {
			assert.InDelta(t, float64(c+1)/4, im.Pix[c][10*w+w-1], 1e-3, "channel %d was not filled", c)
		}
	})

	t.Run("a mismatched mask is a no-op rather than a panic", func(t *testing.T) {
		im := fits.NewImage(8, 8, 1)
		require.NotPanics(t, func() { FillHoles(im, make([]float32, 3)) })
		require.NotPanics(t, func() { FillHoles(nil, make([]float32, 64)) })
	})
}
