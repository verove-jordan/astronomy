package calib

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

func anyContains(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

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
	// No Ha flat exists, so the L flat is used as a cross-filter fallback (shared dust/vignetting).
	if assert.NotNil(t, sel.Flat) {
		assert.Equal(t, "flatL.fits", sel.Flat.Path)
	}
	assert.NotEmpty(t, sel.Notes)
}

func TestMatchForLight_CrossFilterFlatFallback(t *testing.T) {
	// A session with a single (Ha) flat set must still de-dust every channel: an L light gets the Ha
	// flat, with a note. An exact-filter flat, when present, is still preferred over the fallback.
	ms := []Master{
		{Type: MasterFlat, Filter: "Ha", ExposureMs: 1000, Gain: 139, Offset: 21, Bin: 1, FrameCount: 100, Path: "flatHa.fits"},
	}
	sel := MatchForLight(light("L", 120000, 139, -15), ms)
	if assert.NotNil(t, sel.Flat, "the Ha flat should de-dust the L channel") {
		assert.Equal(t, "flatHa.fits", sel.Flat.Path)
	}
	assert.True(t, anyContains(sel.Notes, "using the Ha flat"), "expected a cross-filter flat note, got %v", sel.Notes)

	// With an exact-filter flat available too, it wins over the cross-filter one (no fallback note).
	withL := append(ms, Master{Type: MasterFlat, Filter: "L", Gain: 139, Offset: 21, Bin: 1, FrameCount: 50, Path: "flatL.fits"})
	sel = MatchForLight(light("L", 120000, 139, -15), withL)
	if assert.NotNil(t, sel.Flat) {
		assert.Equal(t, "flatL.fits", sel.Flat.Path)
	}
	for _, n := range sel.Notes {
		assert.NotContains(t, n, "using the")
	}
}

func TestMatchForLight_NoFlatAtAll(t *testing.T) {
	// With no flats in the library, flat correction is simply skipped (no panic, clear note).
	sel := MatchForLight(light("L", 120000, 139, -15), []Master{
		{Type: MasterBias, Gain: 139, Offset: 21, Bin: 1, FrameCount: 8, Path: "bias.fits"},
	})
	assert.Nil(t, sel.Flat)
	assert.Contains(t, sel.Notes, "no flat available — flat correction skipped")
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
