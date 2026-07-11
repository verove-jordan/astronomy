package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/gimp"
	"github.com/verove-jordan/astronomy/internal/mode"
)

func TestTierForStage(t *testing.T) {
	assert.Equal(t, tierC, tierForStage(stageStacked))
	assert.Equal(t, tierB, tierForStage(stageAligned))
	assert.Equal(t, tierB, tierForStage(stageCombined))
	assert.Equal(t, tierB, tierForStage(stageColorCal))
	assert.Equal(t, tierA, tierForStage(stageDeconv))
	assert.Equal(t, tierA, tierForStage(stageStarless))
	assert.Equal(t, tierA, tierForStage(stageFinal))
	assert.Equal(t, tierA, tierForStage(""))
	assert.Equal(t, tierA, tierForStage("nonsense"))
}

// TestRerunTierSelection covers the routing decision at the heart of the feature: the re-entry tier is
// the higher (more expensive) of the param's own tier and the stage the user restarted from.
func TestRerunTierSelection(t *testing.T) {
	base := mode.For(mode.Deepsky)
	sel := func(patch, stage string) tier {
		next := base
		_, err := ApplyParamPatch(&next, []byte(patch))
		require.NoError(t, err)
		t2 := tierOf(base, next)
		if f := tierForStage(stage); f > t2 {
			t2 = f
		}
		return t2
	}

	// A composite-only edit from the Final card stays Tier A (the fast path).
	assert.Equal(t, tierA, sel(`{"lum_opacity":0.7}`, stageFinal))
	// The same edit, but the user restarted from the Stacked card → forced up to Tier C.
	assert.Equal(t, tierC, sel(`{"lum_opacity":0.7}`, stageStacked))
	// A linear-prep edit is Tier B even from the Final card.
	assert.Equal(t, tierB, sel(`{"background_level":0.1}`, stageFinal))
	// A grade edit is Tier C on its own.
	assert.Equal(t, tierC, sel(`{"fwhm_sigma":2}`, stageFinal))
}

func TestLinearPrepRoundTrip(t *testing.T) {
	dir := t.TempDir()
	scratch := t.TempDir()
	base := filepath.Join(scratch, "base.tif")
	lum := filepath.Join(scratch, "lum.tif")
	require.NoError(t, os.WriteFile(base, []byte("BASE"), 0o644))
	require.NoError(t, os.WriteFile(lum, []byte("LUM"), 0o644))

	in := gimp.Inputs{Base: base, Lum: lum, Color: true, CalibratedColor: true}
	require.NoError(t, persistLinearPrep(dir, in, []string{"spcc ok"}))

	got, notes, ok := loadLinearPrep(dir)
	require.True(t, ok)
	assert.Equal(t, filepath.Join(dir, linearDirName, "base.tif"), got.Base, "paths repointed into the run dir")
	assert.Equal(t, filepath.Join(dir, linearDirName, "lum.tif"), got.Lum)
	assert.Empty(t, got.Ha, "absent components stay empty")
	assert.True(t, got.Color)
	assert.True(t, got.CalibratedColor)
	assert.Equal(t, []string{"spcc ok"}, notes)

	// The copied TIFFs actually exist under the run dir.
	assert.FileExists(t, got.Base)
	assert.FileExists(t, got.Lum)

	// A missing base TIFF invalidates the prep (the caller must rebuild it — Tier B).
	require.NoError(t, os.Remove(got.Base))
	_, _, ok = loadLinearPrep(dir)
	assert.False(t, ok)
}

func TestLoadLinearPrep_AbsentIsNotOK(t *testing.T) {
	_, _, ok := loadLinearPrep(t.TempDir())
	assert.False(t, ok)
}

func TestStageManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := mode.For(mode.Deepsky)
	p.LumOpacity = 0.65
	p.Saturation = 0.2
	require.NoError(t, writeStageManifest(dir, &p, "20260101_120000"))

	m, ok := readStageManifest(dir)
	require.True(t, ok)
	assert.Equal(t, "20260101_120000", m.RunID)
	assert.Equal(t, string(mode.Deepsky), m.Mode)
	assert.InDelta(t, 0.65, m.Preset.LumOpacity, 1e-9)
	assert.InDelta(t, 0.2, m.Preset.Saturation, 1e-9)
	assert.Positive(t, m.UpdatedAtMs)
}

func TestReadStageManifest_Absent(t *testing.T) {
	_, ok := readStageManifest(t.TempDir())
	assert.False(t, ok)
}

func TestBackupFinal(t *testing.T) {
	dir := t.TempDir()
	backupFinal(dir) // no final yet → no-op, no error
	assert.NoFileExists(t, filepath.Join(dir, "final_prev.png"))

	require.NoError(t, os.WriteFile(filepath.Join(dir, "final.png"), []byte("PNG"), 0o644))
	backupFinal(dir)
	b, err := os.ReadFile(filepath.Join(dir, "final_prev.png"))
	require.NoError(t, err)
	assert.Equal(t, "PNG", string(b))
}

func TestFilterChannelRecords(t *testing.T) {
	prior := []ChannelResult{{Filter: "L"}, {Filter: "R"}, {Filter: "G"}, {Filter: "B"}}
	kept := filterChannelRecords(prior, map[string]string{"L": "aligned_L", "R": "aligned_R"})
	assert.Len(t, kept, 2)
	// Divergent keys (e.g. an OIII-as-B substitution) must never blank the record.
	kept = filterChannelRecords(prior, map[string]string{"OIII": "aligned_OIII"})
	assert.Len(t, kept, 4)
}
