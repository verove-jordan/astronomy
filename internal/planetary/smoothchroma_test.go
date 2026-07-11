package planetary

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestSmoothChroma_PreservesMeanFlattensColour: the colour smooth must keep the per-pixel RGB mean
// EXACT (Siril's rgbcomp -lum leaks RGB detail into the output lightness, so the mean carries real
// detail) while flattening the channel disagreements — the green/magenta mottle left by per-channel
// seeing warps.
func TestSmoothChroma_PreservesMeanFlattensColour(t *testing.T) {
	const w, h = 64, 64
	dir := t.TempDir()
	masters := map[string]string{}
	mk := func(f string, val func(x, y int) float32) {
		im := fits.NewImage(w, h, 1)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				im.Pix[0][y*w+x] = val(x, y)
			}
		}
		base := filepath.Join(dir, f)
		require.NoError(t, im.WriteFITS(base+".fits"))
		masters[f] = base
	}
	detail := func(x, y int) float32 { return 0.3 + 0.4*float32((x/8+y/8)%2) } // shared structure
	speckle := func(x, y int) float32 {                                        // opposing R/B patches, mean-neutral (colour mottle stand-in)
		if (x/6+y/6)%2 == 0 {
			return 0.08
		}
		return -0.08
	}
	mk("R", func(x, y int) float32 { return detail(x, y) + speckle(x, y) })
	mk("G", detail)
	mk("B", func(x, y int) float32 { return detail(x, y) - speckle(x, y) })

	require.NoError(t, smoothChroma(masters, 12))

	r, err := fits.ReadImage(masters["R"] + ".fits")
	require.NoError(t, err)
	g, err := fits.ReadImage(masters["G"] + ".fits")
	require.NoError(t, err)
	b, err := fits.ReadImage(masters["B"] + ".fits")
	require.NoError(t, err)

	var maxMeanErr, maxDev float64
	for i := range r.Pix[0] {
		m := float64(r.Pix[0][i]+g.Pix[0][i]+b.Pix[0][i]) / 3
		if d := math.Abs(m - float64(detail(i%w, i/w))); d > maxMeanErr {
			maxMeanErr = d
		}
		if d := math.Abs(float64(r.Pix[0][i] - b.Pix[0][i])); d > maxDev {
			maxDev = d
		}
	}
	assert.Less(t, maxMeanErr, 1e-4, "per-pixel mean (shared detail) preserved exactly")
	assert.Less(t, maxDev, 0.08, "R−B mottle flattened to under half its 0.16 input amplitude")
}
