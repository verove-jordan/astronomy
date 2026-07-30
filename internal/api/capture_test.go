package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
)

// Where a capture session may write. This is deliberately wider than every other path-taking
// endpoint — a night's frames routinely go straight to an external disk — so the boundary has to be
// pinned by tests rather than left to reading the code.
func TestCaptureRoot(t *testing.T) {
	dataDir := t.TempDir()
	drive := t.TempDir()
	outside := t.TempDir()

	s := &Server{cfg: &config.Config{
		DataDir:     dataDir,
		OutputDir:   filepath.Join(dataDir, "out"),
		WorkDir:     filepath.Join(dataDir, "work"),
		BrowseRoots: []string{drive},
	}}
	require.NoError(t, os.MkdirAll(s.cfg.OutputDir, 0o755))
	require.NoError(t, os.MkdirAll(s.cfg.WorkDir, 0o755))

	t.Run("an existing folder in the data directory is accepted", func(t *testing.T) {
		got, err := s.captureRoot(dataDir)
		require.NoError(t, err)
		assert.Equal(t, mustEval(t, dataDir), mustEval(t, got))
	})

	// A folder that does not exist yet must be ACCEPTED — a new night, or a new mosaic panel, is the
	// normal case. It is deliberately not created here: the device server writes the frames, and it
	// runs on the host, whereas this engine may be in a container where the destination drive is
	// mounted read-only. Creation (and the writability probe) live in devsrv.ensureWritableDir.
	t.Run("a night's folder is accepted before it exists, but not created here", func(t *testing.T) {
		target := filepath.Join(dataDir, "M31", "2026-07-27", "p03")
		got, err := s.captureRoot(target)
		require.NoError(t, err)
		assert.Equal(t, target, got)

		_, err = os.Stat(got)
		assert.True(t, os.IsNotExist(err),
			"validating a destination must not create it — that is the writer's job")
	})

	t.Run("an external drive is allowed", func(t *testing.T) {
		target := filepath.Join(drive, "AstroCaptures", "M42")
		got, err := s.captureRoot(target)
		require.NoError(t, err)
		assert.Equal(t, target, got)
	})

	t.Run("anywhere else is refused", func(t *testing.T) {
		_, err := s.captureRoot(filepath.Join(outside, "somewhere"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "data directory or a connected drive")
	})

	t.Run("a blank path is refused", func(t *testing.T) {
		_, err := s.captureRoot("   ")
		require.Error(t, err)
	})
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(p)
	require.NoError(t, err)
	return real
}
