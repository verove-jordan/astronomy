package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// touch creates a non-empty file, making the parent dirs as needed.
func touch(t *testing.T, dir, rel string) string {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
	return p
}

func keysOf(items []StageArtifact) []string {
	out := make([]string, 0, len(items))
	for _, a := range items {
		out = append(out, a.Key)
	}
	return out
}

func TestStageArtifacts(t *testing.T) {
	t.Run("a one-shot-colour run offers one channel plus the shared stages, in pipeline order", func(t *testing.T) {
		dir := t.TempDir()
		for _, f := range []string{
			"master_RGB.fits", "trim_RGB.fits", "rgb_base.fits",
			"rgb_base_stretch.fits", "final.tif", "linear/rgb_base_bg.fits",
		} {
			touch(t, dir, f)
		}
		got := StageArtifacts(dir)
		assert.Equal(t, []string{
			"stacked_RGB", "trimmed_RGB", "background", "linear_final", "stretched", "final",
		}, keysOf(got), "ordered by pipeline position, not by filename")
	})

	t.Run("a mono run offers one entry per filter", func(t *testing.T) {
		dir := t.TempDir()
		for _, f := range []string{"master_L.fits", "master_R.fits", "master_Ha.fits"} {
			touch(t, dir, f)
		}
		assert.Equal(t, []string{"stacked_Ha", "stacked_L", "stacked_R"}, keysOf(StageArtifacts(dir)))
	})

	t.Run("stages the run did not produce are absent, not empty entries", func(t *testing.T) {
		dir := t.TempDir()
		touch(t, dir, "final.tif")
		got := StageArtifacts(dir)
		require.Len(t, got, 1)
		assert.Equal(t, "final", got[0].Key)
	})

	t.Run("a zero-byte artifact is skipped", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "final.tif"), nil, 0o644))
		assert.Empty(t, StageArtifacts(dir))
	})

	t.Run("linear sources are flagged so the export knows to autostretch", func(t *testing.T) {
		dir := t.TempDir()
		touch(t, dir, "master_RGB.fits")
		touch(t, dir, "rgb_base_stretch.fits")
		touch(t, dir, "final.tif")
		byKey := map[string]StageArtifact{}
		for _, a := range StageArtifacts(dir) {
			byKey[a.Key] = a
		}
		assert.True(t, byKey["stacked_RGB"].Linear, "a stacked master is linear")
		assert.False(t, byKey["stretched"].Linear, "already stretched")
		assert.False(t, byKey["final"].Linear, "the final is a display image")
	})
}

func TestExportStage_Rejects(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "final.tif")

	t.Run("an unknown stage is refused rather than approximated", func(t *testing.T) {
		// "combined" is deliberately not exportable: rgb_base.fits is processed in place, so its
		// pixels no longer hold the combined stage by the time a run finishes.
		_, err := ExportStage(t.Context(), nil, dir, "combined", "png")
		require.Error(t, err)
	})

	t.Run("an unsupported format is refused", func(t *testing.T) {
		_, err := ExportStage(t.Context(), nil, dir, "final", "webp")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "png or tif")
	})
}
