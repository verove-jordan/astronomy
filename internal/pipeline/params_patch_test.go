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
