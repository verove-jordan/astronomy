package siril

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStackMasterScript(t *testing.T) {
	s := StackMasterScript("cal", "/m/master_bias")
	assert.Contains(t, s, "link cal -out=.")
	assert.Contains(t, s, "stack cal rej winsorized 3 3 -nonorm -out=/m/master_bias")
}

func TestStackFlatScript_WithBias(t *testing.T) {
	s := StackFlatScript("cal", "/m/master_flat", "/m/master_bias.fits")
	assert.Contains(t, s, "calibrate cal -bias=/m/master_bias.fits -prefix=pp_")
	assert.Contains(t, s, "stack pp_cal rej winsorized 3 3 -norm=mul -out=/m/master_flat")
}

func TestStackFlatScript_NoBias(t *testing.T) {
	s := StackFlatScript("cal", "/m/master_flat", "")
	assert.NotContains(t, s, "calibrate")
	assert.Contains(t, s, "stack cal rej winsorized 3 3 -norm=mul -out=/m/master_flat")
}

func TestLightStackScript_FullCalibration(t *testing.T) {
	s := LightStackScript("light", CalibMasters{
		Dark: "/m/d.fits", Flat: "/m/f.fits", Bias: "/m/b.fits",
	}, "/out/master_L")
	assert.Contains(t, s, "calibrate light -dark=/m/d.fits -flat=/m/f.fits -bias=/m/b.fits -cc=dark -prefix=pp_")
	assert.Contains(t, s, "register pp_light")
	assert.Contains(t, s, "stack r_pp_light rej winsorized 3 3 -norm=addscale -output_norm -out=/out/master_L")
}

func TestLightStackScript_NoCalibration(t *testing.T) {
	s := LightStackScript("light", CalibMasters{}, "/out/master_L")
	assert.NotContains(t, s, "calibrate")
	assert.Contains(t, s, "register light")
	assert.Contains(t, s, "stack r_light")
	assert.True(t, strings.HasPrefix(s, "requires"))
}

func TestDenoiseScript(t *testing.T) {
	// -vst and -da3d are mutually exclusive in Siril; VST takes precedence.
	s := DenoiseScript("master_R.fits", "master_R", DenoiseOptions{Modulation: 0.8, VST: true, DA3D: true})
	assert.Contains(t, s, "load master_R.fits")
	assert.Contains(t, s, "denoise -vst -mod=0.80")
	assert.NotContains(t, s, "-da3d")
	assert.Contains(t, s, "save master_R")
}

func TestDenoiseScript_DA3DWhenNoVST(t *testing.T) {
	s := DenoiseScript("x.fits", "x", DenoiseOptions{Modulation: 0.7, DA3D: true})
	assert.Contains(t, s, "denoise -da3d -mod=0.70")
}

func TestDenoiseScript_NoFlagsWhenFullStrengthNoEngine(t *testing.T) {
	// modulation 1.0 (full) with no engine flags → bare `denoise`.
	s := DenoiseScript("x.fits", "x", DenoiseOptions{Modulation: 1})
	assert.Contains(t, s, "\ndenoise\n")
}

func TestPreviewScript(t *testing.T) {
	s := PreviewScript("master_L.fits", "L_preview", 0.5)
	assert.Contains(t, s, "load master_L.fits")
	assert.Contains(t, s, "resample 0.500")
	assert.Contains(t, s, "autostretch")
	assert.Contains(t, s, "savepng L_preview")
}

func TestColorCalibrateScript(t *testing.T) {
	s := ColorCalibrateScript("combined", "combined",
		SolveOptions{FocalMM: 740, PixelUm: 3.8, Catalog: "nomad"},
		SpccOptions{MonoSensor: "ZWO ASI1600MM Pro", RFilter: "Astronomik R", WhiteRef: "Average Spiral Galaxy"})
	assert.Contains(t, s, "load combined")
	assert.Contains(t, s, "platesolve -focal=740.0 -pixelsize=3.80 -catalog=nomad")
	assert.Contains(t, s, `spcc -monosensor="ZWO ASI1600MM Pro" -rfilter="Astronomik R" -whiteref="Average Spiral Galaxy"`)
}

func TestColorCalibrateScript_HeaderCoordsOmitted(t *testing.T) {
	s := ColorCalibrateScript("c", "c", SolveOptions{FocalMM: 740, PixelUm: 3.8}, SpccOptions{})
	// no coords → platesolve relies on header WCS, so no leading coordinate token
	assert.Contains(t, s, "platesolve -focal=740.0")
	assert.NotContains(t, s, "platesolve  ")
}

func TestNeutralizeScript(t *testing.T) {
	s := NeutralizeScript("combined", "combined", 1, 0)
	assert.Contains(t, s, "subsky 1")
	assert.Contains(t, s, "rmgreen 0")
	assert.Contains(t, s, "save combined")
}

func TestSubskyCmd_ClampsToSirilRange(t *testing.T) {
	tests := []struct {
		name   string
		degree int
		want   string
	}{
		{"zero clamps up to 1", 0, "subsky 1\n"},
		{"negative clamps up to 1", -2, "subsky 1\n"},
		{"in range passes through", 3, "subsky 3\n"},
		{"upper bound", 4, "subsky 4\n"},
		{"above range clamps to 4", 5, "subsky 4\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SubskyCmd(tt.degree))
		})
	}
}

func TestFinishScript_LinkedStretch(t *testing.T) {
	s := FinishScript("combined", "final", true, 0.15, []string{"png", "tif"})
	assert.Contains(t, s, "autostretch -linked")
	assert.Contains(t, s, "satu 0.15")
	assert.Contains(t, s, "savepng final")
	assert.Contains(t, s, "savetif final")
}
