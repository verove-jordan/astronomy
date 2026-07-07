package pipeline

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reuseStackedChannel is the resume short-circuit: it reuses a channel's on-disk master (skipping the
// expensive re-stack) only when the run is resuming AND that master exists.
func TestReuseStackedChannel(t *testing.T) {
	outDir := t.TempDir()
	writeFile := func(name string) string {
		p := filepath.Join(outDir, name)
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
		return p
	}

	t.Run("no resume → never reuses", func(t *testing.T) {
		writeFile("master_L.fits")
		_, ok := reuseStackedChannel(Options{}, "M101", "L", outDir)
		assert.False(t, ok)
	})

	t.Run("resume but master missing → not reused", func(t *testing.T) {
		_, ok := reuseStackedChannel(Options{Resume: &ResumeState{}}, "M101", "Ha", outDir)
		assert.False(t, ok)
	})

	t.Run("resume + master present → reused with output path", func(t *testing.T) {
		master := writeFile("master_R.fits")
		ch, ok := reuseStackedChannel(Options{Resume: &ResumeState{}}, "M101", "R", outDir)
		require.True(t, ok)
		assert.Equal(t, "R", ch.Filter)
		assert.Equal(t, master, ch.OutputPath)
		assert.Empty(t, ch.Err)
		assert.Empty(t, ch.PreviewPath, "no preview on disk → none set")
	})

	t.Run("reuses the preview when present", func(t *testing.T) {
		writeFile("master_G.fits")
		preview := writeFile("master_G_preview.png")
		ch, ok := reuseStackedChannel(Options{Resume: &ResumeState{}}, "M101", "G", outDir)
		require.True(t, ok)
		assert.Equal(t, preview, ch.PreviewPath)
	})
}

// A reused channel must satisfy channelMastersMap's guard (Err=="" && OutputPath!="" && Filter!="") so
// combine picks it up — otherwise a resumed channel would silently drop out of the final image.
func TestReuseStackedChannel_SurvivesChannelMastersMap(t *testing.T) {
	outDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outDir, "master_L.fits"), []byte("x"), 0o644))
	ch, ok := reuseStackedChannel(Options{Resume: &ResumeState{}}, "M101", "L", outDir)
	require.True(t, ok)

	masters := channelMastersMap(&Result{Channels: []ChannelResult{ch}}, outDir)
	assert.Equal(t, filepath.Join(outDir, "master_L.fits"), masters["L"])
}

// PausedError is caught by the job layer via errors.As (not a plain string match), so it must unwrap.
func TestPausedError_ErrorsAs(t *testing.T) {
	err := error(&PausedError{RunID: "20260101_120000", OutDir: "/out/M101/20260101_120000"})
	var pe *PausedError
	require.True(t, errors.As(err, &pe))
	assert.Equal(t, "20260101_120000", pe.RunID)
}

// pauseRequested is nil-safe: a run with no pause hook (CLI/MCP) never pauses.
func TestOptions_PauseRequested(t *testing.T) {
	assert.False(t, Options{}.pauseRequested(), "nil hook never pauses")
	assert.False(t, Options{PauseRequested: func() bool { return false }}.pauseRequested())
	assert.True(t, Options{PauseRequested: func() bool { return true }}.pauseRequested())
}
