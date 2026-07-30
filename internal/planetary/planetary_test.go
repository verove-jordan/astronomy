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

// usedChannels decides which classified channels the finish actually consumes: a full R/G/B trio keeps
// only the LRGB inputs (an Ha set beside them is skipped outright — never converted), anything less
// keeps the historical order[0] mono pick.
func TestUsedChannels(t *testing.T) {
	tests := []struct {
		name    string
		order   []string
		used    []string
		skipped []string
	}{
		{"LRGB plus Ha skips Ha", []string{"L", "R", "G", "B", "Ha"}, []string{"L", "R", "G", "B"}, []string{"Ha"}},
		{"plain RGB keeps all", []string{"R", "G", "B"}, []string{"R", "G", "B"}, nil},
		{"L plus Ha is mono L", []string{"L", "Ha"}, []string{"L"}, []string{"Ha"}},
		{"Ha plus SII is mono Ha", []string{"Ha", "SII"}, []string{"Ha"}, []string{"SII"}},
		{"single mono group", []string{""}, []string{""}, []string{}}, // order[1:] → empty, not nil
		{"empty", nil, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			used, skipped := usedChannels(tt.order)
			assert.Equal(t, tt.used, used)
			assert.Equal(t, tt.skipped, skipped)
		})
	}
}

// cleanupChannel must free everything a stacked channel staged — aligned warps, converted frames, .seq
// bookkeeping, the mono staging dir — while keeping the channel master and refusing to touch anything
// outside the run scratch (an in-place channel's frames live in the user's capture folder).
func TestCleanupChannel(t *testing.T) {
	runRoot := t.TempDir()
	chDir := filepath.Join(runRoot, "ch_L")
	require.NoError(t, os.MkdirAll(filepath.Join(chDir, "aligned"), 0o755))
	touch := func(p string) {
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
	}
	touch(filepath.Join(chDir, "aligned", "f_00001.fits"))
	touch(filepath.Join(chDir, "vid_00001.fits"))
	touch(filepath.Join(chDir, "vid_.seq"))
	touch(filepath.Join(chDir, "master_L.fits"))
	framesDir := filepath.Join(runRoot, "mono")
	require.NoError(t, os.MkdirAll(framesDir, 0o755))
	touch(filepath.Join(framesDir, "frame_00001.tif"))

	cleanupChannel(runRoot, chDir, framesDir)

	entries, err := os.ReadDir(chDir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "only the master survives")
	assert.Equal(t, "master_L.fits", entries[0].Name())
	_, err = os.Stat(framesDir)
	assert.True(t, os.IsNotExist(err), "staging dir must be removed")

	// A frames dir OUTSIDE the run scratch (in-place FITS channel) must never be removed.
	captureDir := t.TempDir()
	touch(filepath.Join(captureDir, "orig.fits"))
	cleanupChannel(runRoot, chDir, captureDir)
	_, err = os.Stat(filepath.Join(captureDir, "orig.fits"))
	assert.NoError(t, err, "capture folder must be untouched")
}
