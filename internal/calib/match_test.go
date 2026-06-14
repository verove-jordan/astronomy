package calib

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

func masters() []Master {
	return []Master{
		{Type: MasterBias, Gain: 139, Offset: 21, Bin: 1, FrameCount: 8, Path: "bias.fits"},
		{Type: MasterDark, ExposureMs: 120000, Gain: 139, Offset: 21, Bin: 1, TempMilliC: -15000, HasTemp: true, FrameCount: 5, Path: "dark120.fits"},
		{Type: MasterDark, ExposureMs: 300000, Gain: 139, Offset: 21, Bin: 1, TempMilliC: -15000, HasTemp: true, FrameCount: 5, Path: "dark300.fits"},
		{Type: MasterFlat, Filter: "L", ExposureMs: 2000, Gain: 139, Offset: 21, Bin: 1, FrameCount: 4, Path: "flatL.fits"},
	}
}

func light(filter string, exp, gain int64, tempC int) inspect.SetKey {
	return inspect.SetKey{Type: inspect.Light, Filter: filter, ExposureMs: exp, Gain: gain, Offset: 21, Bin: 1, TempBucket: tempC}
}

func TestMatchForLight_FullMatch(t *testing.T) {
	sel := MatchForLight(light("L", 120000, 139, -15), masters())
	if assert.NotNil(t, sel.Dark) {
		assert.Equal(t, "dark120.fits", sel.Dark.Path)
	}
	if assert.NotNil(t, sel.Flat) {
		assert.Equal(t, "flatL.fits", sel.Flat.Path)
	}
	if assert.NotNil(t, sel.Bias) {
		assert.Equal(t, "bias.fits", sel.Bias.Path)
	}
	assert.Empty(t, sel.Notes)
}

func TestMatchForLight_PicksExposureMatchedDark(t *testing.T) {
	sel := MatchForLight(light("Ha", 300000, 139, -15), masters())
	if assert.NotNil(t, sel.Dark) {
		assert.Equal(t, "dark300.fits", sel.Dark.Path)
	}
	assert.Nil(t, sel.Flat, "no Ha flat exists")
	assert.NotEmpty(t, sel.Notes)
}

func TestMatchForLight_TemperatureTolerance(t *testing.T) {
	within := MatchForLight(light("L", 120000, 139, -18), masters()) // |−18 − −15| = 3 ≤ 5
	assert.NotNil(t, within.Dark)

	outside := MatchForLight(light("L", 120000, 139, -25), masters()) // |−25 − −15| = 10 > 5
	assert.Nil(t, outside.Dark)
}

func TestMatchForLight_NoCameraMatch(t *testing.T) {
	sel := MatchForLight(light("L", 120000, 200, -15), masters()) // different gain
	assert.Nil(t, sel.Dark)
	assert.Nil(t, sel.Bias)
}
