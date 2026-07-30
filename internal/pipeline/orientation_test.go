package pipeline

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// synthOriented builds a mono reference (bright band near the top) and an RGB base with the same
// structure, optionally row-mirrored — the M42 rgbcomp case.
func synthOriented(t *testing.T, dir string, mirrored bool) (basePath, refPath string) {
	t.Helper()
	const w, h = 64, 96
	ref := fits.NewImage(w, h, 1)
	for y := 0; y < h; y++ {
		v := float32(0.05)
		if y >= 10 && y < 20 {
			v = 0.9
		}
		for x := 0; x < w; x++ {
			ref.Pix[0][y*w+x] = v + float32(x)*1e-4
		}
	}
	base := fits.NewImage(w, h, 3)
	for y := 0; y < h; y++ {
		sy := y
		if mirrored {
			sy = h - 1 - y
		}
		for x := 0; x < w; x++ {
			for c := 0; c < 3; c++ {
				base.Pix[c][y*w+x] = ref.Pix[0][sy*w+x]
			}
		}
	}
	basePath = filepath.Join(dir, "rgb_base.fits")
	refPath = filepath.Join(dir, "aligned_L.fits")
	require.NoError(t, base.WriteFITS(basePath))
	require.NoError(t, ref.WriteFITS(refPath))
	return basePath, refPath
}

func TestEnsureRowOrientation(t *testing.T) {
	t.Run("mirrored base is corrected in place", func(t *testing.T) {
		basePath, refPath := synthOriented(t, t.TempDir(), true)
		note, err := ensureRowOrientation(basePath, refPath)
		require.NoError(t, err)
		assert.Contains(t, note, "base orientation corrected")
		fixed, err := fits.ReadImage(basePath)
		require.NoError(t, err)
		// The bright band must now sit at the TOP (rows 10-20), matching the reference.
		assert.Greater(t, fixed.Pix[0][15*fixed.W], float32(0.5), "band must be at the top after the fix")
		assert.Less(t, fixed.Pix[0][(fixed.H-16)*fixed.W], float32(0.2))
		// Idempotent: a second pass sees a correct base and leaves it alone.
		note2, err := ensureRowOrientation(basePath, refPath)
		require.NoError(t, err)
		assert.Empty(t, note2)
	})
	t.Run("correct base untouched", func(t *testing.T) {
		basePath, refPath := synthOriented(t, t.TempDir(), false)
		before, err := fits.ReadImage(basePath)
		require.NoError(t, err)
		note, err := ensureRowOrientation(basePath, refPath)
		require.NoError(t, err)
		assert.Empty(t, note)
		after, err := fits.ReadImage(basePath)
		require.NoError(t, err)
		assert.Equal(t, before.Pix[0], after.Pix[0], "a healthy base must stay byte-identical")
	})
	t.Run("canvas mismatch is a no-op", func(t *testing.T) {
		dir := t.TempDir()
		basePath, _ := synthOriented(t, dir, true)
		small := fits.NewImage(32, 32, 1)
		refPath := filepath.Join(dir, "small.fits")
		require.NoError(t, small.WriteFITS(refPath))
		note, err := ensureRowOrientation(basePath, refPath)
		require.NoError(t, err)
		assert.Empty(t, note)
	})
}
