package pipeline

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/mode"
)

func TestApplyParamPatch_PerMode(t *testing.T) {
	tests := []struct {
		name    string
		mode    mode.Mode
		params  string
		tier    string
		changed []string
		ignored []string
		check   func(t *testing.T, p mode.Preset)
	}{
		{
			name: "deepsky tierA knob", mode: mode.Deepsky,
			params: `{"saturation":0.2}`, tier: "A", changed: []string{"saturation"},
			check: func(t *testing.T, p mode.Preset) { assert.InDelta(t, 0.2, p.Saturation, 1e-9) },
		},
		{
			name: "deepsky clamp wild saturation", mode: mode.Deepsky,
			params: `{"saturation":5}`, tier: "A", changed: []string{"saturation"},
			check: func(t *testing.T, p mode.Preset) { assert.InDelta(t, 0.35, p.Saturation, 1e-9) },
		},
		{
			name: "deepsky lum_opacity is tierA", mode: mode.Deepsky,
			params: `{"lum_opacity":0.7}`, tier: "A", changed: []string{"lum_opacity"},
			check: func(t *testing.T, p mode.Preset) { assert.InDelta(t, 0.7, p.LumOpacity, 1e-9) },
		},
		{
			name: "deepsky star_desat is tierA", mode: mode.Deepsky,
			params: `{"star_desat":0.6}`, tier: "A", changed: []string{"star_desat"},
			check: func(t *testing.T, p mode.Preset) { assert.InDelta(t, 0.6, p.StarDesat, 1e-9) },
		},
		{
			name: "deepsky palette is tierB", mode: mode.Deepsky,
			params: `{"palette":"SHO"}`, tier: "B", changed: []string{"palette"},
			check: func(t *testing.T, p mode.Preset) { assert.Equal(t, "sho", p.Palette) }, // normalized
		},
		{
			name: "deepsky invalid palette is dropped", mode: mode.Deepsky,
			params: `{"palette":"bogus"}`, tier: "A", changed: nil,
			check: func(t *testing.T, p mode.Preset) { assert.Empty(t, p.Palette) },
		},
		{
			name: "deepsky sii_screen is tierA", mode: mode.Deepsky,
			params: `{"sii_screen":0.35}`, tier: "A", changed: []string{"sii_screen"},
			check: func(t *testing.T, p mode.Preset) { assert.InDelta(t, 0.35, p.SIIScreen, 1e-9) },
		},
		{
			name: "deepsky clamps a wild sii_screen", mode: mode.Deepsky,
			params: `{"sii_screen":5}`, tier: "A", changed: []string{"sii_screen"},
			check: func(t *testing.T, p mode.Preset) { assert.InDelta(t, 0.8, p.SIIScreen, 1e-9) },
		},
		{
			name: "deepsky sii_black_point is tierA", mode: mode.Deepsky,
			params: `{"sii_black_point":0.05}`, tier: "A", changed: []string{"sii_black_point"},
			check: func(t *testing.T, p mode.Preset) { assert.InDelta(t, 0.05, p.SIIBlackPoint, 1e-9) },
		},
		{
			name: "deepsky sii_tint is a normalized string enum", mode: mode.Deepsky,
			params: `{"sii_tint":"GOLD"}`, tier: "A", changed: []string{"sii_tint"},
			check: func(t *testing.T, p mode.Preset) { assert.Equal(t, mode.SIITintGold, p.SIITint) },
		},
		{
			name: "deepsky invalid sii_tint is dropped", mode: mode.Deepsky,
			params: `{"sii_tint":"chartreuse"}`, tier: "A", changed: nil,
			check: func(t *testing.T, p mode.Preset) { assert.Empty(t, p.SIITint) },
		},
		{
			name: "deepsky clamps lum_opacity to the 0 floor", mode: mode.Deepsky,
			params: `{"lum_opacity":-1}`, tier: "A", changed: []string{"lum_opacity"},
			check: func(t *testing.T, p mode.Preset) { assert.InDelta(t, 0.0, p.LumOpacity, 1e-9) },
		},
		{
			name: "deepsky tierC grade knob", mode: mode.Deepsky,
			params: `{"fwhm_sigma":2.0}`, tier: "C", changed: []string{"fwhm_sigma"},
			check: func(t *testing.T, p mode.Preset) { assert.InDelta(t, 2.0, p.Grade.FWHMSigma, 1e-9) },
		},
		{
			name: "planetary stack knobs are tierC and clamped", mode: mode.Planetary,
			params: `{"best_percent":2,"deconv_alpha":100}`, tier: "C",
			changed: []string{"best_percent", "deconv_alpha"},
			check: func(t *testing.T, p mode.Preset) {
				assert.Equal(t, 5, p.Planetary.BestPercent)
				assert.InDelta(t, 300, p.Planetary.DeconvAlpha, 1e-9)
			},
		},
		{
			name: "planetary calibrate toggle is tierC", mode: mode.Planetary,
			params: `{"calibrate":false}`, tier: "C", changed: []string{"calibrate"},
			check: func(t *testing.T, p mode.Preset) {
				assert.False(t, p.Planetary.Calibrate, "preset default true → patched off")
			},
		},
		{
			name: "planetary earthshine enable is tierA", mode: mode.Planetary,
			params: `{"earthshine_gain":1}`, tier: "A", changed: []string{"earthshine_gain"},
			check: func(t *testing.T, p mode.Preset) {
				assert.InDelta(t, 1.0, p.Planetary.Finish.EarthshineGain, 1e-9)
			},
		},
		{
			name: "planetary earthshine clamps when enabled", mode: mode.Planetary,
			params: `{"earthshine_gain":9}`, tier: "A", changed: []string{"earthshine_gain"},
			check: func(t *testing.T, p mode.Preset) {
				assert.InDelta(t, 2.0, p.Planetary.Finish.EarthshineGain, 1e-9, "clamped to max")
			},
		},
		{
			name: "planetary earthshine negative stays off", mode: mode.Planetary,
			params: `{"earthshine_gain":-1}`, tier: "A", changed: nil,
			check: func(t *testing.T, p mode.Preset) {
				assert.Zero(t, p.Planetary.Finish.EarthshineGain, "≤0 is a clean off, never clamped on")
			},
		},
		{
			name: "planetary double_stack toggle is tierC", mode: mode.Planetary,
			params: `{"double_stack":false}`, tier: "C", changed: []string{"double_stack"},
			check: func(t *testing.T, p mode.Preset) {
				assert.False(t, p.Planetary.DoubleStack, "preset default true → patched off")
			},
		},
		{
			name: "planetary earthshine feather is tierA and clamps", mode: mode.Planetary,
			params: `{"earthshine_feather":0.5}`, tier: "A", changed: []string{"earthshine_feather"},
			check: func(t *testing.T, p mode.Preset) {
				assert.InDelta(t, 0.02, p.Planetary.Finish.EarthshineFeather, 1e-9, "clamped to max")
			},
		},
		{
			name: "planetary earthshine feather clamps up", mode: mode.Planetary,
			params: `{"earthshine_feather":0.0001}`, tier: "A", changed: []string{"earthshine_feather"},
			check: func(t *testing.T, p mode.Preset) {
				assert.InDelta(t, 0.002, p.Planetary.Finish.EarthshineFeather, 1e-9, "clamped to min")
			},
		},
		{
			name: "planetary drizzle change is tierC and snaps", mode: mode.Planetary,
			params: `{"drizzle_scale":1.9}`, tier: "C", changed: []string{"drizzle_scale"},
			check: func(t *testing.T, p mode.Preset) {
				assert.InDelta(t, 2.0, p.Planetary.DrizzleScale, 1e-9, "snapped to the nearest supported grid")
			},
		},
		{
			name: "planetary drizzle back to native is tierC", mode: mode.Planetary,
			params: `{"drizzle_scale":1}`, tier: "C", changed: []string{"drizzle_scale"},
			check: func(t *testing.T, p mode.Preset) {
				assert.InDelta(t, 1.0, p.Planetary.DrizzleScale, 1e-9, "preset default 1.5 → native")
			},
		},
		{
			name: "planetary align_points is tierC and snaps", mode: mode.Planetary,
			params: `{"align_points":500}`, tier: "C", changed: []string{"align_points"},
			check: func(t *testing.T, p mode.Preset) {
				assert.Equal(t, 484, p.Planetary.AlignPoints, "snapped to 22×22")
			},
		},
		{
			name: "planetary align_points clamps up", mode: mode.Planetary,
			params: `{"align_points":10}`, tier: "C", changed: []string{"align_points"},
			check: func(t *testing.T, p mode.Preset) {
				assert.Equal(t, 100, p.Planetary.AlignPoints, "snapped to the 10×10 floor")
			},
		},
		{
			name: "planetary align_points zero stays auto", mode: mode.Planetary,
			params: `{"align_points":0}`, tier: "A", changed: nil,
			check: func(t *testing.T, p mode.Preset) {
				assert.Equal(t, 0, p.Planetary.AlignPoints, "0 = auto, unchanged from default")
			},
		},
		{
			name: "planetary true_lum toggle is tierA", mode: mode.Planetary,
			params: `{"true_lum":false}`, tier: "A", changed: []string{"true_lum"},
			check: func(t *testing.T, p mode.Preset) {
				assert.False(t, p.Planetary.Finish.TrueLum, "preset default true → patched off")
			},
		},
		{
			name: "planetary shadow_lift is tierA and sets", mode: mode.Planetary,
			params: `{"shadow_lift":0.35}`, tier: "A", changed: []string{"shadow_lift"},
			check: func(t *testing.T, p mode.Preset) {
				assert.InDelta(t, 0.35, p.Planetary.Finish.ShadowLift, 1e-9)
			},
		},
		{
			name: "planetary shadow_lift clamps high", mode: mode.Planetary,
			params: `{"shadow_lift":3}`, tier: "A", changed: []string{"shadow_lift"},
			check: func(t *testing.T, p mode.Preset) {
				assert.InDelta(t, 1.0, p.Planetary.Finish.ShadowLift, 1e-9, "clamped to max")
			},
		},
		{
			name: "planetary shadow_lift negative stays off", mode: mode.Planetary,
			params: `{"shadow_lift":-0.5}`, tier: "A", changed: nil,
			check: func(t *testing.T, p mode.Preset) {
				assert.InDelta(t, 0.0, p.Planetary.Finish.ShadowLift, 1e-9, "clamped to off, unchanged from default")
			},
		},
		{
			name: "milkyway grade knobs", mode: mode.Milkyway,
			params: `{"look":"iphone","highlight_ceiling":0.5}`, tier: "A",
			changed: []string{"highlight_ceiling", "look"},
			check: func(t *testing.T, p mode.Preset) {
				assert.Equal(t, "iphone", p.Look)
				assert.InDelta(t, 0.5, p.HighlightCeil, 1e-9)
			},
		},
		{
			name: "comet restack knob", mode: mode.Comet,
			params: `{"trail_mask_k":2.0}`, tier: "C", changed: []string{"trail_mask_k"},
			check: func(t *testing.T, p mode.Preset) { assert.InDelta(t, 2.0, p.TrailMaskK, 1e-9) },
		},
		{
			name: "unknown keys reported, not fatal", mode: mode.Deepsky,
			params: `{"warp_factor":9,"saturation":0.2}`, tier: "A",
			changed: []string{"saturation"}, ignored: []string{"warp_factor"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := mode.For(tt.mode)
			res, err := ApplyParamPatch(&p, json.RawMessage(tt.params))
			require.NoError(t, err)
			assert.Equal(t, tt.tier, res.Tier)
			assert.Equal(t, tt.changed, res.Changed)
			assert.Equal(t, tt.ignored, res.Ignored)
			if tt.check != nil {
				tt.check(t, p)
			}
		})
	}
}

func TestApplyParamPatch_MalformedBodyErrors(t *testing.T) {
	p := mode.For(mode.Deepsky)
	_, err := ApplyParamPatch(&p, json.RawMessage(`["not","an","object"]`))
	assert.Error(t, err)
}

// fakePriors serves one canned prior for the warm-start test.
type fakePriors struct{ prior PriorIteration }

func (f fakePriors) BestFinishIterations(context.Context, string, string, float64, int) ([]PriorIteration, error) {
	return []PriorIteration{f.prior}, nil
}

func TestWarmStart_SeedsTunablesThroughClamps(t *testing.T) {
	seed := mode.For(mode.Deepsky)
	seed.Saturation = 5.0 // stale/out-of-range value in the stored blob — must clamp on read
	seed.BackgroundLevel = 0.09
	prior := PriorIteration{JobID: 42, Tier: "B", Combined: 8.2, Det: 7.5, Reasoning: "clean pass", Preset: presetBlob(seed)}

	working := mode.For(mode.Deepsky)
	opts := Options{FinishPriors: fakePriors{prior}, PriorObject: "m31"}
	note := warmStart(context.Background(), opts, &working)
	require.NotEmpty(t, note)
	assert.Contains(t, note, "job 42")
	assert.InDelta(t, 0.35, working.Saturation, 1e-9, "stale blob values re-clamp on read")
	assert.InDelta(t, 0.09, working.BackgroundLevel, 1e-9)
}

func TestHistoryBlock_DiffsAndCaps(t *testing.T) {
	mk := func(i int, sat float64, score float64) iterOutcome {
		return iterOutcome{
			index: i, tier: "A", combined: score, det: score, model: score,
			params: map[string]any{"saturation": sat, "ha_screen": 0.42},
			note:   "assessment",
		}
	}
	outs := []iterOutcome{mk(0, 0.12, 6.0), mk(1, 0.20, 7.1), mk(2, 0.20, 7.0)}
	block := historyBlock(outs, 1)
	assert.Contains(t, block, "pass 1")
	assert.Contains(t, block, "saturation: 0.12→0.2", "the param DIFF is what the model needs")
	assert.Contains(t, block, "← BEST so far")
	assert.NotContains(t, block, "ha_screen: 0.42→0.42", "unchanged knobs never appear")
	assert.LessOrEqual(t, len(block), historyMaxChars)

	assert.Empty(t, historyBlock(nil, -1))
}

func TestTunableJSON_ExcludesConsentKeys(t *testing.T) {
	// The cross-run warm start must never resurrect a user opt-in: a prior run with earthshine on
	// would otherwise silently enable it on a run where the user left it off.
	p := mode.For(mode.Planetary)
	p.Planetary.Finish.EarthshineGain = 1.4
	seed := tunableJSON(p)
	assert.NotContains(t, seed, "earthshine_gain", "consent knobs stay out of the warm-start seed")
	assert.Contains(t, seed, "stretch", "ordinary finish knobs still seed")
	assert.Contains(t, seed, "shadow_lift", "an ordinary finish knob — it seeds")
	assert.Contains(t, seed, "earthshine_feather", "the feather is NOT a consent knob (no-op while gain is off) — it seeds")
	assert.Contains(t, seed, "drizzle_scale", "drizzle is default-on, not consent-gated — it seeds")

	assert.Contains(t, tunableJSON(mode.For(mode.Deepsky)), "saturation", "non-consent modes unaffected")
}
