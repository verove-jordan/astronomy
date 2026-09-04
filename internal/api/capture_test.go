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

// The plate scale is what turns a dither in PIXELS into a mount nudge in arcseconds. Nothing in the
// browser has ever sent it, so a run that asked for dithering got none — silently, once every
// DitherN frames, all night, with a single "dither skipped: the image scale is unknown" note to show
// for it. It is derivable from the optics, so it is derived; and from the RUN's focal length,
// because the second rig arrives as focal_mm on the request and is 3x shorter than the configured
// one.
func TestArcsecPerPixel(t *testing.T) {
	tests := []struct {
		name             string
		focalMM, pixelUm float64
		want             float64
	}{
		{"FC-100 DF with the ASI1600MM", 740, 3.8, 1.059},
		{"RedCat 51 with the ASI2600MC", 250, 3.76, 3.102},
		{"no focal length is no scale", 0, 3.8, 0},
		{"no pixel size is no scale", 740, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.want, arcsecPerPixel(tt.focalMM, tt.pixelUm), 0.001)
		})
	}
}
