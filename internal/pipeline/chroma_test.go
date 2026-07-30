package pipeline

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// writeMono writes a constant-pedestal mono FITS with an optional bright square, and returns its path.
func writeMono(t *testing.T, dir, name string, pedestal, patch float32) string {
	t.Helper()
	const w, h = 96, 96
	im := fits.NewImage(w, h, 1)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := pedestal
			if x >= 40 && x < 56 && y >= 40 && y < 56 { // a small bright patch (signal)
				v = patch
			}
			im.Pix[0][y*w+x] = v
		}
	}
	p := filepath.Join(dir, name+".fits")
	require.NoError(t, im.WriteFITS(p))
	return p
}

func pixAt(t *testing.T, path string, x, y int) float32 {
	t.Helper()
	im, err := fits.ReadImage(path)
	require.NoError(t, err)
	return im.Pix[0][y*im.W+x]
}

// TestEqualizeBackgrounds_MatchesSkyPreservesSignal: the three channels' sky pedestals are pulled to the
// darkest, and the per-channel signal delta (patch − pedestal) is preserved (a uniform offset).
func TestEqualizeBackgrounds_MatchesSkyPreservesSignal(t *testing.T) {
	dir := t.TempDir()
	// R is darkest (0.10), G/B carry a positive cast (0.12 / 0.15). Each patch sits 0.40 above its sky.
	r := writeMono(t, dir, "R", 0.10, 0.50)
	g := writeMono(t, dir, "G", 0.12, 0.52)
	b := writeMono(t, dir, "B", 0.15, 0.55)

	note, err := equalizeBackgrounds(r, g, b)
	require.NoError(t, err)
	assert.Contains(t, note, "equalized")

	const eps = 1e-4
	// Sky of every channel now matches the darkest (0.10).
	assert.InDelta(t, 0.10, pixAt(t, r, 0, 0), eps, "R sky unchanged (already darkest)")
	assert.InDelta(t, 0.10, pixAt(t, g, 0, 0), eps, "G sky pulled down to R")
	assert.InDelta(t, 0.10, pixAt(t, b, 0, 0), eps, "B sky pulled down to R")
	// Signal delta preserved per channel (uniform offset, structure intact).
	assert.InDelta(t, 0.40, pixAt(t, g, 48, 48)-pixAt(t, g, 0, 0), eps, "G patch delta preserved")
	assert.InDelta(t, 0.40, pixAt(t, b, 48, 48)-pixAt(t, b, 0, 0), eps, "B patch delta preserved")

	// Idempotent: a second pass shifts by ~0.
	before := pixAt(t, b, 0, 0)
	_, err = equalizeBackgrounds(r, g, b)
	require.NoError(t, err)
	assert.InDelta(t, before, pixAt(t, b, 0, 0), eps, "second equalize is a no-op")
}

// TestChromaSmoothRGB_PreservesMeanFlattensColour: the RGB mean is kept EXACT while the opposing R/B
// colour mottle is flattened — the same contract as the planetary chroma smooth, on the combined RGB.
func TestChromaSmoothRGB_PreservesMeanFlattensColour(t *testing.T) {
	const w, h = 64, 64
	dir := t.TempDir()
	im := fits.NewImage(w, h, 3)
	detail := func(x, y int) float32 { return 0.3 + 0.4*float32((x/8+y/8)%2) } // shared luminance structure
	speckle := func(x, y int) float32 {                                        // opposing R/B patches, mean-neutral (colour mottle stand-in)
		if (x/6+y/6)%2 == 0 {
			return 0.08
		}
		return -0.08
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			im.Pix[0][i] = detail(x, y) + speckle(x, y) // R
			im.Pix[1][i] = detail(x, y)                 // G
			im.Pix[2][i] = detail(x, y) - speckle(x, y) // B
		}
	}
	path := filepath.Join(dir, "rgb_base.fits")
	require.NoError(t, im.WriteFITS(path))

	note, err := chromaSmoothRGB(path, chromaSmoothOpts{FinePx: 12})
	require.NoError(t, err)
	assert.Contains(t, note, "chroma smoothed")

	out, err := fits.ReadImage(path)
	require.NoError(t, err)
	var maxMeanErr, maxDev float64
	for i := range out.Pix[0] {
		m := float64(out.Pix[0][i]+out.Pix[1][i]+out.Pix[2][i]) / 3
		if d := math.Abs(m - float64(detail(i%w, i/w))); d > maxMeanErr {
			maxMeanErr = d
		}
		if d := math.Abs(float64(out.Pix[0][i] - out.Pix[2][i])); d > maxDev {
			maxDev = d
		}
	}
	assert.Less(t, maxMeanErr, 1e-4, "per-pixel RGB mean (shared detail) preserved exactly")
	assert.Less(t, maxDev, 0.08, "R−B colour mottle flattened to under half its 0.16 input amplitude")
}

// TestChromaSmoothRGB_NoopOnMonoOrZeroRadius: a mono image or radius ≤ 0 leaves the file untouched.
func TestChromaSmoothRGB_NoopOnMonoOrZeroRadius(t *testing.T) {
	dir := t.TempDir()
	mono := writeMono(t, dir, "L", 0.2, 0.6)
	note, err := chromaSmoothRGB(mono, chromaSmoothOpts{FinePx: 8})
	require.NoError(t, err)
	assert.Empty(t, note, "mono image → no-op")

	rgb := filepath.Join(dir, "rgb.fits")
	require.NoError(t, fits.NewImage(8, 8, 3).WriteFITS(rgb))
	note, err = chromaSmoothRGB(rgb, chromaSmoothOpts{})
	require.NoError(t, err)
	assert.Empty(t, note, "both radii 0 → no-op")
}

func TestIsRGBChannel(t *testing.T) {
	for _, f := range []string{"R", "G", "B"} {
		assert.True(t, isRGBChannel(f), f)
	}
	for _, f := range []string{"L", "Ha", "OIII", "SII", ""} {
		assert.False(t, isRGBChannel(f), f)
	}
}
