package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/siril"
)

func TestCollectStagePreviews_ParsesAndSorts(t *testing.T) {
	dir := t.TempDir()
	prev := filepath.Join(dir, "previews")
	require.NoError(t, os.MkdirAll(prev, 0o755))
	// Out-of-order names + two that don't match the <NN>_<stage> convention (must be ignored).
	for _, n := range []string{"010_combined.png", "001_stacked_L.png", "900_final.png", "notastage.png", "abc_bad.png"} {
		require.NoError(t, os.WriteFile(filepath.Join(prev, n), []byte("x"), 0o644))
	}
	got := collectStagePreviews(dir)
	require.Len(t, got, 3) // the two malformed names are skipped
	assert.Equal(t, 1, got[0].Index)
	assert.Equal(t, stageStacked, got[0].Stage)
	assert.Equal(t, "L", got[0].Filter)
	assert.Equal(t, stageCombined, got[1].Stage)
	assert.Equal(t, stageFinal, got[2].Stage)
	assert.Empty(t, got[2].Filter)
	assert.Equal(t, filepath.Join(prev, "900_final.png"), got[2].PngPath)
}

func TestCollectStagePreviews_EmptyDir(t *testing.T) {
	assert.Empty(t, collectStagePreviews(t.TempDir()))
}

func TestCapturePreview_CopyEmitsAndWrites(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "final.png")
	require.NoError(t, os.WriteFile(src, []byte("pngbytes"), 0o644))

	var got []postprocess.StagePreview
	opts := Options{
		Preset: &mode.Preset{Previews: true},
		Runner: siril.New("/nonexistent-siril", siril.Limits{}),
		OnProgress: func(p Progress) {
			if p.StagePreview != nil {
				got = append(got, *p.StagePreview)
			}
		},
	}
	// linear=false → copy the already-stretched PNG (no Siril run).
	capturePreview(context.Background(), opts, dir, ordFinal, stageFinal, "", src, false)

	require.Len(t, got, 1)
	assert.Equal(t, stageFinal, got[0].Stage)
	assert.Equal(t, ordFinal, got[0].Index)
	dest := filepath.Join(dir, "previews", "900_final.png")
	assert.Equal(t, dest, got[0].PngPath)
	assert.FileExists(t, dest)
}

func TestCapturePreview_DisabledIsNoOp(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "final.png")
	require.NoError(t, os.WriteFile(src, []byte("x"), 0o644))
	emitted := 0
	opts := Options{
		Preset: &mode.Preset{Previews: false}, // previews off → nothing happens
		Runner: siril.New("/nonexistent", siril.Limits{}),
		OnProgress: func(p Progress) {
			if p.StagePreview != nil {
				emitted++
			}
		},
	}
	capturePreview(context.Background(), opts, dir, ordFinal, stageFinal, "", src, false)
	assert.Zero(t, emitted)
	assert.NoDirExists(t, filepath.Join(dir, "previews"))
}
