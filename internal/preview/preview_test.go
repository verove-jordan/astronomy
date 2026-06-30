package preview

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

func TestFitDims(t *testing.T) {
	cases := []struct {
		name         string
		w, h, max    int
		wantW, wantH int
	}{
		{"no downscale when within cap", 100, 80, 200, 100, 80},
		{"landscape capped on width", 2000, 1000, 1000, 1000, 500},
		{"portrait capped on height", 1000, 2000, 500, 250, 500},
		{"square", 3000, 3000, 1500, 1500, 1500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, h := fitDims(tc.w, tc.h, tc.max)
			assert.Equal(t, tc.wantW, w)
			assert.Equal(t, tc.wantH, h)
		})
	}
}

func TestAutoBounds(t *testing.T) {
	t.Run("gradient has lo < hi within range", func(t *testing.T) {
		pix := make([]uint16, 1000)
		for i := range pix {
			pix[i] = uint16(i * 65) // spread across the range
		}
		lo, hi := autoBounds(pix)
		assert.Less(t, lo, hi)
		assert.LessOrEqual(t, hi, uint16(65535))
	})
	t.Run("flat data falls back to full range", func(t *testing.T) {
		lo, hi := autoBounds(make([]uint16, 100)) // all zero
		assert.Equal(t, uint16(0), lo)
		assert.Equal(t, uint16(65535), hi)
	})
}

// A linear FITS gradient must normalize so the darkest column maps near 0 and the brightest near
// 65535, downsampled within the requested edge cap.
func TestLoad_FITSGradientNormalizesAndDownsamples(t *testing.T) {
	dir := t.TempDir()
	const n = 100
	im := fits.NewImage(n, n, 1)
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			im.Pix[0][y*n+x] = float32(x) // horizontal ramp 0..99
		}
	}
	path := filepath.Join(dir, "grad.fits")
	require.NoError(t, im.WriteFITS(path))

	pv, err := Load(context.Background(), path, 50)
	require.NoError(t, err)
	assert.Equal(t, 1, pv.C)
	assert.LessOrEqual(t, pv.W, 50)
	assert.LessOrEqual(t, pv.H, 50)
	require.Equal(t, pv.W*pv.H*pv.C, len(pv.Pix))
	assert.Less(t, pv.Pix[0], uint16(3000), "leftmost (darkest) maps near 0")
	assert.Greater(t, pv.Pix[pv.W-1], uint16(60000), "rightmost (brightest) maps near 65535")
	assert.Less(t, pv.AutoLo, pv.AutoHi)
}

// A color PNG decodes through the Go image stack as a 3-channel preview.
func TestLoad_PNGColor(t *testing.T) {
	dir := t.TempDir()
	img := image.NewNRGBA(image.Rect(0, 0, 20, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 20; x++ {
			img.Set(x, y, color.NRGBA{R: uint8(x * 12), G: 0, B: uint8(y * 25), A: 255})
		}
	}
	path := filepath.Join(dir, "c.png")
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, png.Encode(f, img))
	require.NoError(t, f.Close())

	pv, err := Load(context.Background(), path, 100)
	require.NoError(t, err)
	assert.Equal(t, 3, pv.C)
	assert.Equal(t, 20, pv.W)
	assert.Equal(t, 10, pv.H)
	assert.Equal(t, pv.W*pv.H*3, len(pv.Pix))
}

func TestSupportedExt(t *testing.T) {
	for _, ok := range []string{"/a/x.fits", "/a/x.FIT", "/a/x.dng", "/a/x.HEIC", "/a/x.tif", "/a/x.png", "/a/x.jpg"} {
		assert.True(t, SupportedExt(ok), ok)
	}
	for _, no := range []string{"/a/x.ser", "/a/x.txt", "/a/x.mp4", "/a/x"} {
		assert.False(t, SupportedExt(no), no)
	}
}
