package inspect

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectInput(t *testing.T) {
	t.Run("video file", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "moon.mp4")
		require.NoError(t, os.WriteFile(f, []byte("x"), 0o644))
		k, err := DetectInput(f)
		require.NoError(t, err)
		assert.Equal(t, KindVideo, k)
	})

	t.Run("fits dir", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "a.fits"), []byte("x"), 0o644))
		k, err := DetectInput(dir)
		require.NoError(t, err)
		assert.Equal(t, KindFITS, k)
	})

	t.Run("raw dir (iPhone DNG / png)", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "IMG_0001.dng"), []byte("x"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "IMG_0002.png"), []byte("x"), 0o644))
		k, err := DetectInput(dir)
		require.NoError(t, err)
		assert.Equal(t, KindRaw, k)
	})

	t.Run("empty dir errors", func(t *testing.T) {
		_, err := DetectInput(t.TempDir())
		assert.Error(t, err)
	})
}

func TestListRawFrames(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"b.png", "a.dng", "c.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644))
	}
	frames, err := ListRawFrames(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join(dir, "a.dng"), filepath.Join(dir, "b.png")}, frames)
}
