package pipeline

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/stackalg"
)

// patchStack applies a knob patch to a mode's preset through the REAL entry point (the one the API,
// the presets and the rerun editor all share) and returns the result.
func patchStack(t *testing.T, m mode.Mode, knobs map[string]any) (mode.Preset, ParamPatchResult) {
	t.Helper()
	p := mode.For(m)
	raw, err := json.Marshal(knobs)
	require.NoError(t, err)
	res, err := ApplyParamPatch(&p, raw)
	require.NoError(t, err)
	return p, res
}

func TestApplyParamPatch_StackingKnobs(t *testing.T) {
	tests := []struct {
		name  string
		knobs map[string]any
		check func(t *testing.T, p mode.Preset)
	}{
		{
			name:  "the rejection algorithm",
			knobs: map[string]any{"stack_reject": "linear_fit"},
			check: func(t *testing.T, p mode.Preset) {
				assert.Equal(t, stackalg.RejectLinearFit, p.Stack.Reject)
				// Its own defaults render, not the previous algorithm's sigmas.
				assert.Equal(t, "rej linear 5 3.5 -norm=addscale -output_norm", siril.StackClause(p.Stack, 30))
			},
		},
		{
			name:  "explicit rejection parameters",
			knobs: map[string]any{"stack_reject": "sigma", "stack_reject_low": 2.5, "stack_reject_high": 2.0},
			check: func(t *testing.T, p mode.Preset) {
				assert.Equal(t, "rej sigma 2.5 2 -norm=addscale -output_norm", siril.StackClause(p.Stack, 30))
			},
		},
		{
			// Sent together, an explicit sigma wins — the user asked for both in one breath.
			name:  "an explicit sigma survives a same-patch algorithm change",
			knobs: map[string]any{"stack_reject_low": 2.5, "stack_reject": "sigma"},
			check: func(t *testing.T, p mode.Preset) {
				assert.Equal(t, "rej sigma 2.5 3 -norm=addscale -output_norm", siril.StackClause(p.Stack, 30))
			},
		},
		{
			name:  "the combination method",
			knobs: map[string]any{"stack_combine": "median"},
			check: func(t *testing.T, p mode.Preset) {
				assert.Equal(t, "med -norm=addscale -output_norm", siril.StackClause(p.Stack, 30))
			},
		},
		{
			name:  "case and spacing are forgiven",
			knobs: map[string]any{"stack_reject": " Winsorized "},
			check: func(t *testing.T, p mode.Preset) {
				assert.Equal(t, stackalg.RejectWinsorized, p.Stack.Reject)
			},
		},
		{
			name:  "auto returns to the count-adaptive choice",
			knobs: map[string]any{"stack_reject": "auto"},
			check: func(t *testing.T, p mode.Preset) {
				assert.Equal(t, stackalg.RejectAuto, p.Stack.Reject)
				assert.Equal(t, "rej generalized 0.3 0.05 -norm=addscale -output_norm", siril.StackClause(p.Stack, 60))
			},
		},
		{
			name:  "normalization and weighting",
			knobs: map[string]any{"stack_norm": "mulscale", "stack_weight": "noise"},
			check: func(t *testing.T, p mode.Preset) {
				assert.Equal(t, stackalg.NormMulScale, p.Stack.Norm)
				assert.Equal(t, "noise", p.StackWeight)
			},
		},
		{
			name:  "weighting can be turned off",
			knobs: map[string]any{"stack_weight": "none"},
			check: func(t *testing.T, p mode.Preset) {
				assert.Empty(t, p.StackWeight)
				assert.NotContains(t, siril.StackClause(p.Stack, 30), "-weight=")
			},
		},
		{
			name:  "the diagnostic flags",
			knobs: map[string]any{"stack_rejection_maps": true, "stack_fast_norm": true, "stack_feather": 20},
			check: func(t *testing.T, p mode.Preset) {
				got := siril.StackClause(p.Stack, 30)
				assert.Contains(t, got, "-rejmaps")
				assert.Contains(t, got, "-fastnorm")
				assert.Contains(t, got, "-feather=20")
			},
		},
		{
			name:  "a native-only algorithm routes to the Go combiner",
			knobs: map[string]any{"stack_reject": "entropy_weighted"},
			check: func(t *testing.T, p mode.Preset) {
				assert.Equal(t, stackalg.EngineNative, stackalg.EngineFor(p.Stack))
			},
		},
		{
			name:  "out-of-range values are clamped, not rejected",
			knobs: map[string]any{"stack_reject_high": 999, "stack_feather": 9000, "stack_local_norm_degree": 9},
			check: func(t *testing.T, p mode.Preset) {
				assert.Equal(t, stackalg.SigmaMax, p.Stack.High)
				assert.Equal(t, 512, p.Stack.Feather)
				assert.Equal(t, 4, p.Stack.LocalNormDegree)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, res := patchStack(t, mode.Deepsky, tt.knobs)
			assert.Empty(t, res.Ignored, "every key here must be part of the deep-sky surface")
			tt.check(t, p)
		})
	}
}

// TestApplyParamPatch_StackingAlgorithmChangeResetsSigmas: changing the algorithm on its own must
// drop the previous one's parameters — 3σ means nothing to percentile clipping, where the same
// number is a kept FRACTION.
func TestApplyParamPatch_StackingAlgorithmChangeResetsSigmas(t *testing.T) {
	p := mode.For(mode.Deepsky)
	raw, err := json.Marshal(map[string]any{"stack_reject": "sigma", "stack_reject_low": 2.5})
	require.NoError(t, err)
	_, err = ApplyParamPatch(&p, raw)
	require.NoError(t, err)
	require.Equal(t, 2.5, p.Stack.Low)

	raw, err = json.Marshal(map[string]any{"stack_reject": "percentile"})
	require.NoError(t, err)
	_, err = ApplyParamPatch(&p, raw)
	require.NoError(t, err)
	assert.Zero(t, p.Stack.Low, "the old sigma must not leak into a different algorithm's units")
	assert.Equal(t, "rej percentile 0.2 0.1 -norm=addscale -output_norm", siril.StackClause(p.Stack, 30))
}

// TestApplyParamPatch_StackingIsTierC pins the cost model: no re-entry cheaper than a re-stack can
// reflect a change to how pixels are combined.
func TestApplyParamPatch_StackingIsTierC(t *testing.T) {
	for _, knobs := range []map[string]any{
		{"stack_reject": "gesd"},
		{"stack_combine": "median"},
		{"stack_reject_low": 2},
		{"stack_norm": "add"},
		{"stack_weight": "nbstars"},
		{"stack_feather": 12},
		{"stack_local_norm": true},
	} {
		_, res := patchStack(t, mode.Deepsky, knobs)
		assert.Equal(t, "C", res.Tier, "%v must force a re-stack", knobs)
		assert.NotEmpty(t, res.Changed, "%v must report what it changed", knobs)
	}
}

// TestApplyParamPatch_StackingUnchangedIsCheap: re-sending the mode's own defaults must not force an
// expensive re-entry — the launch form prefills the box with exactly these values.
func TestApplyParamPatch_StackingDefaultsAreANoOp(t *testing.T) {
	defaults := ParamsFor(mode.For(mode.Deepsky))
	knobs := map[string]any{}
	for _, k := range []string{
		"stack_engine", "stack_combine", "stack_reject", "stack_reject_low", "stack_reject_high",
		"stack_norm", "stack_weight", "stack_feather", "stack_local_norm_degree",
	} {
		knobs[k] = defaults[k]
	}
	p, res := patchStack(t, mode.Deepsky, knobs)
	assert.Equal(t, "A", res.Tier, "the prefilled defaults must not trigger a re-stack")
	assert.Equal(t, mode.For(mode.Deepsky).Stack, p.Stack)
	assert.Equal(t, "wfwhm", p.StackWeight)
	assert.Equal(t, "rej winsorized 3 3 -norm=addscale -output_norm", siril.StackClause(p.Stack, 30))
}

func TestApplyParamPatch_StackingRejectsUnknownValues(t *testing.T) {
	tests := []struct {
		name    string
		knobs   map[string]any
		wantErr string
	}{
		{"rejection algorithm", map[string]any{"stack_reject": "kappa"}, "unknown stack_reject"},
		{"combination method", map[string]any{"stack_combine": "geomean"}, "unknown stack_combine"},
		{"normalization", map[string]any{"stack_norm": "additive"}, "unknown stack_norm"},
		{"weighting", map[string]any{"stack_weight": "sharpness"}, "unknown stack_weight"},
		{"engine", map[string]any{"stack_engine": "gpu"}, "unknown stack_engine"},
		{"wrong type", map[string]any{"stack_reject_low": "three"}, "stacking knobs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := mode.For(mode.Deepsky)
			raw, err := json.Marshal(tt.knobs)
			require.NoError(t, err)
			_, err = ApplyParamPatch(&p, raw)
			require.Error(t, err, "a bogus value must never be silently ignored")
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestApplyParamPatch_StackingIsModeScoped: the modes that stack natively must not advertise the
// Siril stacking knobs — offering them there would be a lie.
func TestApplyParamPatch_StackingIsModeScoped(t *testing.T) {
	for _, m := range []mode.Mode{mode.Deepsky, mode.Nebula, mode.Livestack, mode.Mosaic, mode.Comet} {
		_, res := patchStack(t, m, map[string]any{"stack_reject": "mad"})
		assert.Empty(t, res.Ignored, "%s stacks with Siril and must accept the knobs", m)
	}
	for _, m := range []mode.Mode{mode.Planetary, mode.Sun, mode.Milkyway} {
		_, res := patchStack(t, m, map[string]any{"stack_reject": "mad"})
		assert.Contains(t, res.Ignored, "stack_reject", "%s stacks natively and must report the knob as unknown", m)
	}
}

// TestApplyParamPatch_CometAlignedStackKeepsItsAsymmetry: the comet half is tuned separately, and the
// shared stack_* keys must not quietly symmetrize it.
func TestApplyParamPatch_CometAlignedStack(t *testing.T) {
	p, res := patchStack(t, mode.Comet, map[string]any{"stack_reject": "gesd"})
	assert.Empty(t, res.Ignored)
	assert.Equal(t, stackalg.RejectWinsorized, p.StackComet.Reject, "the comet-aligned stack is untouched")
	assert.Equal(t, "rej winsorized 4 1.8 -norm=addscale -output_norm", siril.StackClause(p.StackComet, 30))

	p, _ = patchStack(t, mode.Comet, map[string]any{"comet_stack_high": 1.2})
	assert.Equal(t, "rej winsorized 4 1.2 -norm=addscale -output_norm", siril.StackClause(p.StackComet, 30))
}

// TestApplyParamPatch_MasterRecipesArePerFrameType is the point of the feature: each calibration
// frame type carries its OWN algorithm, because their pools differ by an order of magnitude — a
// 200-frame bias set and a 5-flat set want opposite tests.
func TestApplyParamPatch_MasterRecipesArePerFrameType(t *testing.T) {
	p, res := patchStack(t, mode.Deepsky, map[string]any{
		"master_bias_reject":      "gesd",
		"master_flat_reject":      "percentile",
		"master_dark_combine":     "median",
		"master_dark_flat_reject": "mad",
	})
	require.Empty(t, res.Ignored)

	assert.Equal(t, stackalg.RejectGESD, p.Masters.Bias.Reject)
	assert.Equal(t, stackalg.RejectPercentile, p.Masters.Flat.Reject)
	assert.Equal(t, stackalg.CombineMedian, p.Masters.Dark.Combine)
	assert.Equal(t, stackalg.RejectMAD, p.Masters.DarkFlat.Reject)
	// Editing one type must not touch another.
	assert.Equal(t, stackalg.RejectAuto, p.Masters.Dark.Reject)

	// Each renders its own command, with the normalization physics still fixed per type.
	assert.Equal(t, "rej generalized 0.3 0.05 -nonorm", siril.StackClause(p.Masters.Bias, 200))
	assert.Equal(t, "rej percentile 0.2 0.1 -norm=mul", siril.StackClause(p.Masters.Flat, 5))
	assert.Equal(t, "med -nonorm", siril.StackClause(p.Masters.Dark, 30))
	assert.Equal(t, "C", res.Tier, "rebuilding masters means re-stacking")
}

// TestApplyParamPatch_MasterRecipesValidate: a bogus algorithm on a calibration type must fail as
// loudly as one on the lights.
func TestApplyParamPatch_MasterRecipesValidate(t *testing.T) {
	for key, want := range map[string]string{
		"master_bias_reject":      "unknown master_bias_reject",
		"master_flat_combine":     "unknown master_flat_combine",
		"master_dark_flat_reject": "unknown master_dark_flat_reject",
	} {
		p := mode.For(mode.Deepsky)
		raw, err := json.Marshal(map[string]any{key: "nonsense"})
		require.NoError(t, err)
		_, err = ApplyParamPatch(&p, raw)
		require.Error(t, err, key)
		assert.Contains(t, err.Error(), want)
	}
}

// TestMasterRecipes_DefaultsAreANoOp: the launch form prefills these keys, so re-sending them must
// not rebuild every master under a variant name.
func TestMasterRecipes_DefaultsAreANoOp(t *testing.T) {
	defaults := ParamsFor(mode.For(mode.Deepsky))
	knobs := map[string]any{}
	for _, prefix := range []string{"master_bias", "master_dark", "master_flat", "master_dark_flat"} {
		for _, field := range []string{"combine", "reject", "low", "high"} {
			k := prefix + "_" + field
			knobs[k] = defaults[k]
		}
	}
	p, res := patchStack(t, mode.Deepsky, knobs)
	assert.Equal(t, "A", res.Tier, "the prefilled defaults must not trigger a rebuild")
	assert.Equal(t, mode.For(mode.Deepsky).Masters, p.Masters)
}
