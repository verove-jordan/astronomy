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

func TestBuildScript_ModeSelection(t *testing.T) {
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
			script, res := buildScript(tc.channels, "final", DefaultOptions())
			assert.Equal(t, tc.wantMode, res.Mode)
			for _, want := range tc.contains {
				assert.Contains(t, script, want)
			}
			assert.Contains(t, script, "autostretch")
		})
	}
}

func TestBuildScript_Formats(t *testing.T) {
	opts := DefaultOptions()
	opts.Formats = []string{"png", "tif", "fits"}
	script, _ := buildScript(chans("R", "G", "B"), "final", opts)
	assert.Contains(t, script, "savepng final")
	assert.Contains(t, script, "savetif final")
	assert.Contains(t, script, "save final")
}
