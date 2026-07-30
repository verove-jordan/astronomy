package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/postprocess"
)

// TestColorCalibrateCached_HitSkipsSPCC: a pre-seeded cache whose sig matches the input + options
// restores the pixels, the note and the METHOD (which gates SCNR downstream) — with a nil runner,
// a miss would have crashed calling Siril, so a pass proves the plate-solve+SPCC was skipped.
func TestColorCalibrateCached_HitSkipsSPCC(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "rgb_base.fits")
	require.NoError(t, os.WriteFile(base, []byte("pixels-v1"), 0o644))

	cc := postprocess.ColorCalOptions{Enabled: true, RemoveGreen: true, StarField: true}
	sig, err := fileSHA256(base)
	require.NoError(t, err)
	linDir := filepath.Join(dir, linearDirName)
	require.NoError(t, os.MkdirAll(linDir, 0o755))
	cacheFits := filepath.Join(linDir, "rgb_base_spcc.fits")
	require.NoError(t, os.WriteFile(cacheFits, []byte("calibrated-pixels"), 0o644))
	require.NoError(t, os.WriteFile(cacheFits+".sig", []byte(sig+"|"+ccOptionsHash(cc)), 0o644))
	require.NoError(t, os.WriteFile(cacheFits+".json",
		[]byte(`{"note":"SPCC photometric calibration applied","method":`+methodJSON(postprocess.CalSPCC)+`}`), 0o644))

	note, method, err := colorCalibrateCached(context.Background(), Options{}, nil, dir, "rgb_base", cc)
	require.NoError(t, err)
	assert.Contains(t, note, "reused from cache")
	assert.Equal(t, postprocess.CalSPCC, method)
	got, _ := os.ReadFile(base)
	assert.Equal(t, "calibrated-pixels", string(got))

	// A changed calibration option must miss (different key) — proven by the nil runner: reaching
	// Siril panics, so we only check the keys differ.
	cc2 := cc
	cc2.RemoveGreen = false
	assert.NotEqual(t, ccOptionsHash(cc), ccOptionsHash(cc2))
}

func methodJSON(m postprocess.CalMethod) string {
	return string(rune('0' + int(m)))
}

// TestExtractCombinedBackgroundCached_Hit mirrors the denoise-cache contract for the combined
// background pass: matching sig (+ unchanged graxpert/rbf mode) copies the flattened result back
// and restores the recorded note. nil runner ⇒ a miss would crash.
func TestExtractCombinedBackgroundCached_Hit(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "rgb_base.fits")
	require.NoError(t, os.WriteFile(base, []byte("pixels-v1"), 0o644))

	sig, err := fileSHA256(base)
	require.NoError(t, err)
	linDir := filepath.Join(dir, linearDirName)
	require.NoError(t, os.MkdirAll(linDir, 0o755))
	cacheFits := filepath.Join(linDir, "rgb_base_bg.fits")
	require.NoError(t, os.WriteFile(cacheFits, []byte("flattened-pixels"), 0o644))
	require.NoError(t, os.WriteFile(cacheFits+".sig", []byte(sig+"|rbf"), 0o644)) // no GraXpert configured → rbf mode
	require.NoError(t, os.WriteFile(cacheFits+".note", []byte("combined background flattened (RBF subsky; GraXpert unavailable)"), 0o644))

	opts := Options{Preset: &mode.Preset{CombinedBackgroundAI: true}}
	note := extractCombinedBackgroundCached(context.Background(), opts, nil, dir, "rgb_base", "", nil)
	assert.Contains(t, note, "reused from cache")
	assert.Contains(t, note, "RBF subsky")
	got, _ := os.ReadFile(base)
	assert.Equal(t, "flattened-pixels", string(got))
}

func TestTransferChroma_PreservesMeanExactly(t *testing.T) {
	orig := fits.NewImage(2, 1, 3)
	orig.Pix[0][0], orig.Pix[1][0], orig.Pix[2][0] = 0.9, 0.3, 0.3 // strong red pixel
	orig.Pix[0][1], orig.Pix[1][1], orig.Pix[2][1] = 0.1, 0.1, 0.1
	up := fits.NewImage(2, 1, 3)
	up.Pix[0][0], up.Pix[1][0], up.Pix[2][0] = 0.5, 0.5, 0.5 // denoise neutralized the colour
	up.Pix[0][1], up.Pix[1][1], up.Pix[2][1] = 0.4, 0.2, 0.0

	transferChroma(orig, up)

	for i := 0; i < 2; i++ {
		switch i {
		case 0: // mean was 0.5 — the neutral chroma lands exactly on it
			assert.InDelta(t, 0.5, orig.Pix[0][0], 1e-6)
			assert.InDelta(t, 0.5, orig.Pix[1][0], 1e-6)
		case 1: // mean 0.1 kept; up's chroma spread (±0.2 around its own mean 0.2) transferred
			assert.InDelta(t, 0.3, orig.Pix[0][1], 1e-6)
			assert.InDelta(t, 0.1, orig.Pix[1][1], 1e-6)
			assert.InDelta(t, -0.1, orig.Pix[2][1], 1e-6)
		}
		m := (orig.Pix[0][i] + orig.Pix[1][i] + orig.Pix[2][i]) / 3
		want := []float32{0.5, 0.1}[i]
		assert.InDelta(t, want, m, 1e-6, "per-pixel mean must be preserved exactly")
	}
}

func TestDenoiseScaleSigSuffix(t *testing.T) {
	assert.Empty(t, denoiseScaleSigSuffix(Options{}))
	assert.Empty(t, denoiseScaleSigSuffix(Options{DenoiseScale: 1.0}))
	assert.Equal(t, "|scale=0.500", denoiseScaleSigSuffix(Options{DenoiseScale: 0.5}))
}

func TestStepTimer(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	tm := newStepTimer(func() time.Time { return clock })

	tm.observe("masters")
	clock = clock.Add(2 * time.Minute)
	tm.observe("stacking L")
	clock = clock.Add(8 * time.Minute)
	tm.observe("stacking L") // same step: keeps accumulating, no reset
	clock = clock.Add(1 * time.Minute)
	tm.observe("masters") // revisited name accumulates into the same bucket
	clock = clock.Add(30 * time.Second)

	got := tm.finish()
	assert.Equal(t, []StepTiming{
		{Step: "masters", Ms: (2*time.Minute + 30*time.Second).Milliseconds()},
		{Step: "stacking L", Ms: (9 * time.Minute).Milliseconds()},
	}, got)
	line := timingSummary(got)
	assert.Contains(t, line, "masters 2m30s")
	assert.Contains(t, line, "stacking L 9m0s")
	assert.Contains(t, line, "total 11m30s")
	assert.Empty(t, timingSummary(nil))
}
