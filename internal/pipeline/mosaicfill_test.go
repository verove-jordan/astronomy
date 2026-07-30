package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/mode"
)

func TestApplyMosaicConstraints(t *testing.T) {
	t.Run("off is a no-op", func(t *testing.T) {
		p := &mode.Preset{CoverageCrop: true, CropFrac: 0.035}
		applyMosaicConstraints(p)
		assert.True(t, p.CoverageCrop)
		assert.Equal(t, 0.035, p.CropFrac)
	})
	t.Run("on forces the union-keeping knobs", func(t *testing.T) {
		p := &mode.Preset{Mosaic: true, CoverageCrop: true, CropFrac: 0.035}
		applyMosaicConstraints(p)
		assert.False(t, p.CoverageCrop)
		assert.Zero(t, p.CropFrac)
		assert.True(t, p.SeamOffsetRefit, "mosaic implies the seam repair")
		assert.True(t, p.SeamNoiseEq)
		assert.Equal(t, "crop", p.MosaicFill, "default policy")
	})
	t.Run("fill policy survives", func(t *testing.T) {
		p := &mode.Preset{Mosaic: true, MosaicFill: "fill"}
		applyMosaicConstraints(p)
		assert.Equal(t, "fill", p.MosaicFill)
	})
}

func TestShiftGrid(t *testing.T) {
	g := twoZoneGrid(128, 128, 1, 5) // 16×16 cells
	shifted := shiftGrid(g, 64, 32, 256, 192)
	assert.Equal(t, 32, shifted.W)
	assert.Equal(t, 24, shifted.H)
	assert.Equal(t, g.Frames, shifted.Frames)
	assert.Zero(t, shifted.Counts[0], "new margin is uncovered")
	// Original cell (0,0)=1 lands at cell (8,4).
	assert.Equal(t, uint16(1), shifted.Counts[4*32+8])
	// Negative shift crops: content cell (8,0) becomes (0,0).
	cropped := shiftGrid(g, -64, 0, 64, 128)
	assert.Equal(t, g.Counts[0*16+8], cropped.Counts[0])
}

func TestFillNoCoverage(t *testing.T) {
	const w, h = 256, 256
	mkImage := func() (*fits.Image, []bool) {
		im := fits.NewImage(w, h, 1)
		covered := make([]bool, w*h)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				if x < w/2 {
					im.Pix[0][y*w+x] = 0.08 + 0.001*float32(x%7)
					covered[y*w+x] = true
				}
			}
		}
		return im, covered
	}

	im, covered := mkImage()
	orig := append([]float32(nil), im.Pix[0]...)
	frac, sigma := fillNoCoverage(im, covered, 42)
	assert.InDelta(t, 0.5, frac, 0.01)
	assert.Greater(t, sigma, 0.0)

	for y := 0; y < h; y++ {
		for x := 0; x < w/2; x++ {
			require.Equal(t, orig[y*w+x], im.Pix[0][y*w+x], "covered pixel (%d,%d) must stay byte-identical", x, y)
		}
	}
	var sum float64
	n := 0
	for y := 0; y < h; y++ {
		for x := w/2 + 8; x < w; x++ {
			sum += float64(im.Pix[0][y*w+x])
			n++
		}
	}
	assert.InDelta(t, 0.083, sum/float64(n), 0.01, "filled region sits at the covered sky level")

	im2, covered2 := mkImage()
	fillNoCoverage(im2, covered2, 42)
	assert.Equal(t, im.Pix[0], im2.Pix[0], "same seed reproduces the same grain")

	full := fits.NewImage(32, 32, 1)
	allCov := make([]bool, 32*32)
	for i := range allCov {
		allCov[i] = true
	}
	f, _ := fillNoCoverage(full, allCov, 1)
	assert.Zero(t, f, "fully covered image is a no-op")
}
