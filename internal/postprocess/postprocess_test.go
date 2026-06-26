package postprocess

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func chans(filters ...string) map[string]string {
	m := map[string]string{}
	for _, f := range filters {
		m[f] = "master_" + f
	}
	return m
}

func TestBuildCombine_ModeSelection(t *testing.T) {
	cases := []struct {
		name     string
		channels map[string]string
		wantMode string
		contains []string
	}{
		{"HaLRGB", chans("R", "G", "B", "L", "Ha"), "HaLRGB", []string{"pm \"max($master_R$, $master_Ha$)\"", "-lum=lum_ha"}},
		{"LRGB", chans("R", "G", "B", "L"), "LRGB", []string{"rgbcomp master_R master_G master_B -lum=master_L"}},
		{"RGB", chans("R", "G", "B"), "RGB", []string{"rgbcomp master_R master_G master_B -out=combined"}},
		{"HaRGB", chans("R", "G", "B", "Ha"), "HaRGB", []string{"red_ha", "rgbcomp red_ha master_G master_B -out=combined"}},
		{"SHO", chans("Ha", "OIII", "SII"), "SHO", []string{"rgbcomp master_SII master_Ha master_OIII"}},
		{"mono", chans("L"), "mono", []string{"load master_L"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script, res := buildCombine(tc.channels, DefaultOptions())
			assert.Equal(t, tc.wantMode, res.Mode)
			for _, want := range tc.contains {
				assert.Contains(t, script, want)
			}
			// The combine stage produces a LINEAR `combined.fits` (background-extracted); the
			// stretch/save now happen in the separate finish stage.
			assert.Contains(t, script, "subsky")
			assert.Contains(t, script, "save combined")
			assert.NotContains(t, script, "autostretch")
		})
	}
}

func TestIsColor(t *testing.T) {
	for _, m := range []string{"RGB", "LRGB", "HaRGB", "HaLRGB", "SHO"} {
		assert.True(t, isColor(m), m)
	}
	assert.False(t, isColor("mono"))
}

func TestSolveFailed(t *testing.T) {
	assert.True(t, solveFailed("log: Plate solving failed, no match"))
	assert.True(t, solveFailed("No stars detected in the image"))
	assert.False(t, solveFailed("Plate solving succeeded: 1.06 arcsec/px"))
	assert.False(t, solveFailed(""))
}
