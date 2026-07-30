package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestHaScreenGate pins the red-wash attenuation curve: full strength for a healthy stretched
// layer, linear fade above haLayerBgMax, skip beyond haLayerBgSkip (task #355's solid-red final).
func TestHaScreenGate(t *testing.T) {
	tests := []struct {
		name   string
		median float64
		want   float64
	}{
		{"healthy layer full strength", 0.05, 1},
		{"autostretch target full strength", 0.14, 1},
		{"boundary max full strength", 0.30, 1},
		{"midpoint half strength", 0.425, 0.5},
		{"boundary skip zero", 0.55, 0},
		{"task 355 wash zero", 0.72, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.want, haScreenGate(tt.median), 1e-9)
		})
	}
}

// writeHaStat writes a mono ha_stat.fits whose every pixel sits at level.
func writeHaStat(t *testing.T, dir string, level float32) {
	t.Helper()
	im := fits.NewImage(64, 64, 1)
	for i := range im.Pix[0] {
		im.Pix[0][i] = level
	}
	require.NoError(t, im.WriteFITS(filepath.Join(dir, haStatBase+".fits")))
}

func TestGateHaScreen(t *testing.T) {
	t.Run("healthy layer passes at full strength and removes the stat file", func(t *testing.T) {
		dir := t.TempDir()
		writeHaStat(t, dir, 0.05)
		factor, note := gateHaScreen(dir)
		assert.Equal(t, 1.0, factor)
		assert.Empty(t, note)
		assert.NoFileExists(t, filepath.Join(dir, haStatBase+".fits"))
	})
	t.Run("bright layer attenuates with a note", func(t *testing.T) {
		dir := t.TempDir()
		writeHaStat(t, dir, 0.45)
		factor, note := gateHaScreen(dir)
		assert.Greater(t, factor, 0.0)
		assert.Less(t, factor, 1.0)
		assert.Contains(t, note, "attenuated")
	})
	t.Run("washed layer skips the screen", func(t *testing.T) {
		dir := t.TempDir()
		writeHaStat(t, dir, 0.72)
		factor, note := gateHaScreen(dir)
		assert.Equal(t, 0.0, factor)
		assert.Contains(t, note, "Ha screen skipped")
	})
	t.Run("missing measurement soft-fails to full strength", func(t *testing.T) {
		dir := t.TempDir()
		factor, note := gateHaScreen(dir)
		assert.Equal(t, 1.0, factor)
		assert.Empty(t, note)
		_, err := os.Stat(filepath.Join(dir, haStatBase+".fits"))
		assert.True(t, os.IsNotExist(err))
	})
}
