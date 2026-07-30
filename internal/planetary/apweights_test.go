package planetary

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

func TestApSelectionFields(t *testing.T) {
	// 12 frames; cell 0's local sharpness descends 12,11,…,1 → K = max(6, 25%·12=3) = 6, so the
	// 6th-best score (7) is the soft cutoff: well above ≈1, at the cutoff 0.5, well below ≈0.
	const frames = 12
	cells := apGridN * apGridN
	cellSharp := make([][]float64, frames)
	for i := range cellSharp {
		cellSharp[i] = make([]float64, cells)
		cellSharp[i][0] = float64(frames - i) // 12, 11, …, 1
		cellSharp[i][1] = 0                   // no detail in ANY frame → neutral
	}

	fields := apSelectionFields(cellSharp)
	require.Len(t, fields, frames)
	assert.Greater(t, fields[0][0], 0.99, "locally sharpest frame is fully selected")
	assert.InDelta(t, 0.5, fields[frames-7][0], 1e-9, "the K-th best score sits at the logistic midpoint")
	assert.Less(t, fields[frames-1][0], 0.01, "locally soft frames are excluded, not floored")
	for i := range fields {
		assert.Equal(t, 1.0, fields[i][1], "cell without detail in ANY frame stays neutral")
	}
}

func TestSelectionK(t *testing.T) {
	assert.Equal(t, 6, selectionK(12), "floored at apSelectMin")
	assert.Equal(t, 25, selectionK(100), "25% of the pool")
	assert.Equal(t, 3, selectionK(3), "never more than the pool")
}

func TestStackWeightedFileAP_RegionalDominance(t *testing.T) {
	// Frame A is all 1.0, frame B all 0.0; A "owns" the top-left cell, B the bottom-right. The master
	// must track A in A's cell and B in B's cell — the whole point of per-AP selection.
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
	fa[0] = 1                              // A dominates the top-left cell
	fb[apGridN*apGridN-1] = 1              // B dominates the bottom-right cell
	master := filepath.Join(dir, "master") // .fits appended by the stacker
	require.NoError(t, stackWeightedFileAP(context.Background(), []string{pa, pb}, []float64{1, 1}, [][]float64{fa, fb}, master, nil))

	out, err := fits.ReadImage(master + ".fits")
	require.NoError(t, err)
	cell := w / apGridN
	topLeft := out.Pix[0][(cell/2)*w+cell/2]
	bottomRight := out.Pix[0][(h-cell/2)*w+(w-cell/2)]
	assert.Greater(t, float64(topLeft), 0.8, "A-dominant region tracks frame A")
	assert.Less(t, float64(bottomRight), 0.2, "B-dominant region tracks frame B")
}
