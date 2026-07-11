package planetary

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

func TestApWeightFields(t *testing.T) {
	cells := apGridN * apGridN
	a := make([]float64, cells)
	b := make([]float64, cells)
	a[0], b[0] = 1.0, 0.5 // frame A twice as sharp in cell 0
	// cell 1 has detail only in frame B; cell 2 is off-disk in both (0).
	a[1], b[1] = 0, 0.8

	fields := apWeightFields([][]float64{a, b})
	require.Len(t, fields, 2)
	assert.Equal(t, 1.0, fields[0][0], "the per-cell max normalizes to 1")
	assert.InDelta(t, 0.25, fields[1][0], 1e-9, "(0.5)^2 quality falloff")
	assert.Equal(t, apWeightMin, fields[0][1], "no detail in A's cell 1 → floored")
	assert.Equal(t, 1.0, fields[1][1])
	assert.Equal(t, 1.0, fields[0][2], "cell without detail in ANY frame stays neutral")
	assert.Equal(t, 1.0, fields[1][2])
}

func TestStackWeightedFileAP_RegionalDominance(t *testing.T) {
	// Frame A is all 1.0, frame B all 0.0; A "owns" the top-left cell, B the bottom-right. The master
	// must track A in A's cell and B in B's cell — the whole point of multi-point quality weighting.
	const w, h = 100, 100
	dir := t.TempDir()
	mk := func(name string, v float32) string {
		im := fits.NewImage(w, h, 1)
		for i := range im.Pix[0] {
			im.Pix[0][i] = v
		}
		p := filepath.Join(dir, name)
		require.NoError(t, im.WriteFITS(p))
		return p
	}
	pa := mk("a.fits", 1)
	pb := mk("b.fits", 0)

	cells := apGridN * apGridN
	fa := make([]float64, cells)
	fb := make([]float64, cells)
	for k := range fa {
		fa[k], fb[k] = apWeightMin, apWeightMin
	}
	fa[0] = 1                              // A dominates the top-left cell
	fb[apGridN*apGridN-1] = 1              // B dominates the bottom-right cell
	master := filepath.Join(dir, "master") // .fits appended by the stacker
	require.NoError(t, stackWeightedFileAP([]string{pa, pb}, []float64{1, 1}, [][]float64{fa, fb}, master))

	out, err := fits.ReadImage(master + ".fits")
	require.NoError(t, err)
	cell := w / apGridN
	topLeft := out.Pix[0][(cell/2)*w+cell/2]
	bottomRight := out.Pix[0][(h-cell/2)*w+(w-cell/2)]
	assert.Greater(t, float64(topLeft), 0.8, "A-dominant region tracks frame A")
	assert.Less(t, float64(bottomRight), 0.2, "B-dominant region tracks frame B")
}
