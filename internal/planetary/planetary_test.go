package planetary

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStageImageFrames(t *testing.T) {
	src := t.TempDir()
	// A Moon capture as a FITS image series, nested in per-timestamp subfolders (ASICAP/SharpCap style),
	// plus a hidden dir and a non-image file that must both be ignored.
	for _, rel := range []string{
		"autorun/2020-07-07_03_06_01Z/m_0001.FIT",
		"autorun/2020-07-07_03_06_01Z/m_0002.fit",
		"autorun/2020-07-07_03_06_10Z/m_0003.FIT",
		"autorun/notes.txt",
		".thumbs/preview.fit",
	} {
		p := filepath.Join(src, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
	}

	seqDir := t.TempDir()
	n, err := stageImageFrames(src, seqDir)
	require.NoError(t, err)
	assert.Equal(t, 3, n, "3 FITS frames staged; the .txt and hidden-dir file ignored")

	staged, err := filepath.Glob(filepath.Join(seqDir, "frame_*"))
	require.NoError(t, err)
	assert.Len(t, staged, 3)
	// Flat, numbered, in acquisition (sorted-path) order.
	for i, name := range []string{"frame_00001.fit", "frame_00002.fit", "frame_00003.fit"} {
		assert.Equal(t, name, filepath.Base(staged[i]))
	}
}

func TestStageImageFrames_NoFrames(t *testing.T) {
	n, err := stageImageFrames(t.TempDir(), t.TempDir())
	require.NoError(t, err)
	assert.Zero(t, n, "an empty / image-less folder stages nothing (caller surfaces the error)")
}

func TestRejectLeastSharp(t *testing.T) {
	// Scores by frame index 1..5; sharpest are 3 and 5.
	scores := []float64{0.1, 0.2, 0.9, 0.05, 0.8}

	// Keep best 40% (2 of 5) → reject the other 3 (indices 1, 2, 4), sorted.
	assert.Equal(t, []int{1, 2, 4}, rejectLeastSharp(scores, 40))

	// Keep 100% → reject none.
	assert.Empty(t, rejectLeastSharp(scores, 100))

	// Always keep at least one even at 0%.
	assert.Len(t, rejectLeastSharp(scores, 0), len(scores)-1)
}

func TestLaplacianVariance(t *testing.T) {
	const w, h = 16, 16
	flat := make([]float64, w*h)
	for i := range flat {
		flat[i] = 100
	}
	sharp := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// high-frequency checkerboard
			sharp[y*w+x] = math.Mod(float64(x+y), 2) * 200
		}
	}
	assert.InDelta(t, 0, laplacianVariance(flat, w, h), 1e-9, "a flat field has ~zero sharpness")
	assert.Greater(t, laplacianVariance(sharp, w, h), laplacianVariance(flat, w, h), "detailed frame is sharper")
}
