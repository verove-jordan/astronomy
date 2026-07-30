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

func TestMatchForLight_DarkOptimizeFallback(t *testing.T) {
	// No 60s dark exists, but a bias + same-camera darks of other exposures do: the longest dark is
	// selected for Siril dark optimization (-opt) instead of skipping dark calibration entirely.
	sel := MatchForLight(light("L", 60000, 139, -15), masters())
	if assert.NotNil(t, sel.Dark) {
		assert.Equal(t, "dark300.fits", sel.Dark.Path, "the longest scalable dark wins")
	}
	assert.True(t, sel.DarkOptimize)
	assert.True(t, anyContains(sel.Notes, "dark-optimized"), "expected a dark-optimization note, got %v", sel.Notes)
}

func TestMatchForLight_NoDarkOptimizeWithoutBias(t *testing.T) {
	// Dark optimization needs the bias to isolate the thermal signal: without one the mismatched
	// dark must NOT be applied.
	ms := []Master{
		{Type: MasterDark, ExposureMs: 300000, Gain: 139, Offset: 21, Bin: 1, TempMilliC: -15000, HasTemp: true, FrameCount: 5, Path: "dark300.fits"},
	}
	sel := MatchForLight(light("L", 60000, 139, -15), ms)
	assert.Nil(t, sel.Dark)
	assert.False(t, sel.DarkOptimize)
	assert.True(t, anyContains(sel.Notes, "no matching dark"), "got %v", sel.Notes)
}

// force_calibration_frames: a wrong-GAIN dark+bias are applied anyway (strict matching drops them), with
// a mismatch note. Same exposure, so the dark is applied directly (no dark-optimization).
func TestMatchForLight_ForcedCrossGain(t *testing.T) {
	light := light("L", 120000, 200, -15) // gain 200 — masters() are all gain 139

	strict := MatchForLightExcluding(light, masters(), nil, false)
	assert.Nil(t, strict.Dark, "strict: gain mismatch drops the dark")
	assert.Nil(t, strict.Bias, "strict: gain mismatch drops the bias")

	forced := MatchForLightExcluding(light, masters(), nil, true)
	if assert.NotNil(t, forced.Dark, "forced: the gain-139 dark is applied to gain-200 lights") {
		assert.Equal(t, "dark120.fits", forced.Dark.Path)
	}
	assert.False(t, forced.DarkOptimize, "same exposure → direct subtraction, no -opt")
	assert.NotNil(t, forced.Bias, "forced: the gain-139 bias is applied too")
	assert.True(t, anyContains(forced.Notes, "forced dark — applied despite mismatched"),
		"expected a gain-mismatch note, got %v", forced.Notes)
}

// force_calibration_frames: a dark whose temperature is outside the ±5 °C tolerance is applied anyway,
// with a temperature-mismatch note.
func TestMatchForLight_ForcedOutOfTemp(t *testing.T) {
	light := light("L", 120000, 139, -25) // |−25 − −15| = 10 > 5

	assert.Nil(t, MatchForLightExcluding(light, masters(), nil, false).Dark, "strict: out-of-tolerance dark dropped")

	forced := MatchForLightExcluding(light, masters(), nil, true)
	if assert.NotNil(t, forced.Dark) {
		assert.Equal(t, "dark120.fits", forced.Dark.Path)
	}
	assert.True(t, anyContains(forced.Notes, "°C"), "expected a temperature-mismatch note, got %v", forced.Notes)
}

// force_calibration_frames with no bias and no exposure-matched dark: the closest dark is subtracted
// directly (the -opt path needs a bias), with a warning that the thermal fit is approximate.
func TestMatchForLight_ForcedDirectNoBias(t *testing.T) {
	ms := []Master{
		{Type: MasterDark, ExposureMs: 300000, Gain: 139, Offset: 21, Bin: 1, TempMilliC: -15000, HasTemp: true, FrameCount: 5, Path: "dark300.fits"},
	}
	light := light("L", 60000, 139, -15) // no 60s dark, no bias to scale a 300s one

	assert.Nil(t, MatchForLightExcluding(light, ms, nil, false).Dark, "strict: no bias → mismatched dark refused")

	forced := MatchForLightExcluding(light, ms, nil, true)
	if assert.NotNil(t, forced.Dark, "forced: the 300s dark is applied to 60s lights directly") {
		assert.Equal(t, "dark300.fits", forced.Dark.Path)
	}
	assert.False(t, forced.DarkOptimize, "no bias → cannot dark-optimize; direct subtraction instead")
	assert.True(t, anyContains(forced.Notes, "without scaling"),
		"expected an approximate-subtraction note, got %v", forced.Notes)
}

// Planetary lunar captures (SharpCap 16-bit TIFF sidecars) carry gain 0 / offset 0 and no explicit bin —
// both lights AND cal frames parse identically, so a full match must hold on that degenerate key shape,
// with the dark's real temperature landing inside the ±5 °C bucket tolerance.
func TestMatchForLight_PlanetaryShapedKeys(t *testing.T) {
	lunar := []Master{
		{Type: MasterBias, Gain: 0, Offset: 0, Bin: 1, FrameCount: 64, Path: "bias.fits"},
		{Type: MasterDark, ExposureMs: 10, Gain: 0, Offset: 0, Bin: 1, TempMilliC: -17000, HasTemp: true, FrameCount: 64, Path: "dark10.fits"},
		{Type: MasterFlat, Filter: "L", ExposureMs: 10, Gain: 0, Offset: 0, Bin: 1, FrameCount: 64, Path: "flatL.fits"},
		{Type: MasterFlat, Filter: "Ha", ExposureMs: 10, Gain: 0, Offset: 0, Bin: 1, FrameCount: 64, Path: "flatHa.fits"},
	}
	key := inspect.SetKey{Type: inspect.Light, Filter: "L", ExposureMs: 10, Gain: 0, Offset: 0, Bin: 1, TempBucket: -20}
	sel := MatchForLight(key, lunar)
	if assert.NotNil(t, sel.Dark) {
		assert.Equal(t, "dark10.fits", sel.Dark.Path, "-17°C dark within ±5°C of the -20 bucket")
	}
	assert.False(t, sel.DarkOptimize)
	if assert.NotNil(t, sel.Flat) {
		assert.Equal(t, "flatL.fits", sel.Flat.Path, "per-filter flat must win over the Ha flat")
	}
	if assert.NotNil(t, sel.Bias) {
		assert.Equal(t, "bias.fits", sel.Bias.Path)
	}
}

// TestMatchForLight_FlatNearestNight pins the cross-night flat ranking: when the light's night has
// no own flat, the temporally NEAREST night wins — regardless of candidate order or stack depth
// (two 100-frame flats used to tie all the way down and fall to list order, handing one channel a
// 3-week-old flat while its neighbours got the 11-day one).
func TestMatchForLight_FlatNearestNight(t *testing.T) {
	light := inspect.SetKey{Type: inspect.Light, Filter: "L", Gain: 0, Bin: 1, ExposureMs: 120000, Session: "2020-05-06"}
	far := Master{Type: MasterFlat, Filter: "L", Gain: 230, Bin: 1, FrameCount: 100, Session: "2020-04-15", Path: "far.fits"}
	near := Master{Type: MasterFlat, Filter: "L", Gain: 130, Bin: 1, FrameCount: 100, Session: "2020-04-26", Path: "near.fits"}
	for name, masters := range map[string][]Master{
		"near listed second": {far, near},
		"near listed first":  {near, far},
	} {
		t.Run(name, func(t *testing.T) {
			sel := MatchForLight(light, masters)
			if sel.Flat == nil || sel.Flat.Path != "near.fits" {
				t.Fatalf("want the 2020-04-26 flat, got %+v", sel.Flat)
			}
		})
	}
	t.Run("same night still outranks nearest", func(t *testing.T) {
		own := Master{Type: MasterFlat, Filter: "L", Gain: 99, Bin: 1, FrameCount: 3, Session: "2020-05-06", Path: "own.fits"}
		sel := MatchForLight(light, []Master{far, near, own})
		if sel.Flat == nil || sel.Flat.Path != "own.fits" {
			t.Fatalf("want the same-night flat, got %+v", sel.Flat)
		}
	})
	t.Run("unknown-night flat ranks last on a multi-night run", func(t *testing.T) {
		// A night-less master (library row, or a promoted header-less flat set) has unknowable dust
		// age: even a deep one must lose to a flat dated days away. A promoted lone-frame ghost flat
		// once outranked every per-night candidate here and re-polluted the L channel (job 364).
		ghost := Master{Type: MasterFlat, Filter: "L", FrameCount: 200, Path: "ghost.fits"}
		sel := MatchForLight(light, []Master{ghost, far, near})
		if sel.Flat == nil || sel.Flat.Path != "near.fits" {
			t.Fatalf("want the dated 2020-04-26 flat over the night-less one, got %+v", sel.Flat)
		}
		// As the ONLY candidate it is still applied — better than no flat correction at all.
		sel = MatchForLight(light, []Master{ghost})
		if sel.Flat == nil || sel.Flat.Path != "ghost.fits" {
			t.Fatalf("want the night-less flat as last resort, got %+v", sel.Flat)
		}
	})
	t.Run("deeper stack still breaks a same-distance tie", func(t *testing.T) {
		shallow := near
		shallow.FrameCount, shallow.Path = 10, "shallow.fits"
		sel := MatchForLight(light, []Master{shallow, near})
		if sel.Flat == nil || sel.Flat.Path != "near.fits" {
			t.Fatalf("want the deeper flat, got %+v", sel.Flat)
		}
	})
}
