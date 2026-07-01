package planetary

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

func TestSharpnessWeights_EmphasizesSharpest(t *testing.T) {
	w := sharpnessWeights([]float64{100, 50, 10, 0})
	assert.InDelta(t, 1.0, w[0], 1e-9, "sharpest → weight 1")
	assert.InDelta(t, 0.125, w[1], 1e-6, "half sharpness → 0.5^3")
	assert.Equal(t, stackWeightMin, w[2], "0.1^3 is below the floor")
	assert.Equal(t, stackWeightMin, w[3], "zero sharpness → floor")
}

func toGrid(im *fits.Image) []float64 {
	g := make([]float64, len(im.Pix[0]))
	for i, v := range im.Pix[0] {
		g[i] = float64(v)
	}
	return g
}

// A sharpness-weighted stack of one sharp frame + several blurred copies must retain more high-frequency
// detail than a plain mean of the same frames — the whole point of weighting for lucky imaging.
func TestStackWeightedFile_SharperThanPlainMean(t *testing.T) {
	dir := t.TempDir()
	sharp := diskImage(80, 80, 40, 40, 28) // crisp disk + a bright feature
	soft := blurPlane(sharp, 3)            // blurred copy (same content, lower sharpness)
	frames := []*fits.Image{sharp, soft, soft, soft}
	scores := []float64{100, 5, 5, 5}

	paths := make([]string, len(frames))
	for i, im := range frames {
		paths[i] = filepath.Join(dir, fmt.Sprintf("f_%02d.fits", i))
		require.NoError(t, im.WriteFITS(paths[i]))
	}
	require.NoError(t, stackWeightedFile(paths, scores, filepath.Join(dir, "m")))
	m, err := fits.ReadImage(filepath.Join(dir, "m.fits"))
	require.NoError(t, err)

	// Plain (unweighted) mean of the same frames, for comparison.
	pm := fits.NewImage(80, 80, 1)
	for _, f := range frames {
		for i := range pm.Pix[0] {
			pm.Pix[0][i] += f.Pix[0][i] / float32(len(frames))
		}
	}
	assert.Greater(t,
		laplacianVariance(toGrid(m), m.W, m.H),
		laplacianVariance(toGrid(pm), pm.W, pm.H),
		"sharpness-weighted stack must retain more detail than a plain mean")
}
