package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/graxpert"
)

// TestDenoiseAICached_ReusesUnchangedInput: a pre-seeded cache whose sig matches the input is copied back
// and the (expensive) GraXpert pass is skipped entirely — the runner points at a missing binary, so if
// the cache had MISSED, denoiseAI would have been called and soft-failed ("skipped") instead of "reused".
func TestDenoiseAICached_ReusesUnchangedInput(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "rgb_base.fits")
	require.NoError(t, os.WriteFile(base, []byte("pixels-v1"), 0o644))

	linDir := filepath.Join(dir, linearDirName)
	require.NoError(t, os.MkdirAll(linDir, 0o755))
	sig, err := fileSHA256(base)
	require.NoError(t, err)
	cacheFits := filepath.Join(linDir, "rgb_base_denoised.fits")
	require.NoError(t, os.WriteFile(cacheFits, []byte("denoised-result"), 0o644))
	require.NoError(t, os.WriteFile(cacheFits+".sig", []byte(sig), 0o644))

	opts := Options{Graxpert: graxpert.New("/nonexistent/graxpert", "")}
	note := denoiseAICached(context.Background(), opts, base, dir, nil)

	assert.Contains(t, note, "reused")
	got, _ := os.ReadFile(base)
	assert.Equal(t, "denoised-result", string(got), "the cached denoised image is copied over the input")
}

// TestDenoiseAICached_MissDoesNotCacheOnFailure: with no cache and a broken GraXpert, the denoise
// soft-fails and nothing is cached (only a genuine success is persisted).
func TestDenoiseAICached_MissDoesNotCacheOnFailure(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "rgb_base.fits")
	require.NoError(t, os.WriteFile(base, []byte("pixels-v1"), 0o644))

	opts := Options{Graxpert: graxpert.New("/nonexistent/graxpert", "")}
	note := denoiseAICached(context.Background(), opts, base, dir, nil)

	assert.Contains(t, note, "skipped")
	assert.NoFileExists(t, filepath.Join(dir, linearDirName, "rgb_base_denoised.fits"))
}
