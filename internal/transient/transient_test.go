package transient

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestMaskCrossFrame_RemovesMovingTransientKeepsStatic models a slow satellite: a bright transient that
// lands on a different pixel in each frame (so per sky pixel it appears once) over a static star + noisy
// background. The mask must clean every transient (replacing it with the per-pixel median) while leaving
// the consistent star and the background untouched.
func TestMaskCrossFrame_RemovesMovingTransientKeepsStatic(t *testing.T) {
	const n, w, h = 8, 20, 20
	const starIdx = 5*w + 5
	frames := make([]*fits.Image, n)
	for i := range frames {
		im := fits.NewImage(w, h, 1)
		for p := range im.Pix[0] {
			im.Pix[0][p] = 100 + float32((i*7+p*3)%5) - 2 // background ~98..102, varies per frame+pixel
		}
		im.Pix[0][starIdx] = 500 + float32((i*3)%5) - 2 // a static star with slight per-frame jitter
		im.Pix[0][i*w+2] = 900                          // the moving transient: one per frame, column 2, row i
		frames[i] = im
	}

	masked, err := MaskCrossFrame(frames, 3.0)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, masked, n, "should clean at least the n moving transients")

	for i := range frames {
		assert.Less(t, float64(frames[i].Pix[0][i*w+2]), 200.0,
			"moving transient in frame %d not cleaned to the background median", i)
		assert.InDelta(t, 500.0, float64(frames[i].Pix[0][starIdx]), 10.0,
			"static star wrongly masked in frame %d", i)
	}
}

// TestMaskCrossFrame_NoopBelowMinFrames guards that the per-pixel statistic isn't applied when it can't
// be robust (too few frames) or when disabled (k<=0) — it must leave every frame byte-for-byte unchanged.
func TestMaskCrossFrame_NoopBelowMinFrames(t *testing.T) {
	mk := func(n int) []*fits.Image {
		out := make([]*fits.Image, n)
		for i := range out {
			im := fits.NewImage(8, 8, 1)
			im.Pix[0][10] = float32(1000 * i) // wild values that WOULD be flagged if the mask ran
			out[i] = im
		}
		return out
	}

	few := mk(MinFrames - 1)
	masked, err := MaskCrossFrame(few, 3.0)
	require.NoError(t, err)
	assert.Zero(t, masked, "fewer than MinFrames must be a no-op")
	assert.Equal(t, float32(3000), few[3].Pix[0][10], "no-op must not alter pixels")

	enough := mk(MinFrames + 2)
	masked, err = MaskCrossFrame(enough, 0) // disabled
	require.NoError(t, err)
	assert.Zero(t, masked, "k<=0 must be a no-op")
}

// TestMaskCrossFrame_RejectsMismatchedFrames guards the dimension contract.
func TestMaskCrossFrame_RejectsMismatchedFrames(t *testing.T) {
	frames := make([]*fits.Image, MinFrames)
	for i := range frames {
		frames[i] = fits.NewImage(8, 8, 1)
	}
	frames[2] = fits.NewImage(8, 9, 1) // odd one out
	_, err := MaskCrossFrame(frames, 3.0)
	assert.Error(t, err)
}
