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
