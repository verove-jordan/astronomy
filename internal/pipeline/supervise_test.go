package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/verove-jordan/astronomy/internal/mode"
)

func TestClampPreset_BoundsAllTiers(t *testing.T) {
	p := mode.Preset{
		Saturation: 9, HaScreen: -1, HaBlackPoint: 5, ChromaBlur: 99, CropFrac: 1,
		BackgroundLevel: 9, BackgroundDegree: 9, StarReduce: 9,
		TrailMaskK: 99, DenoiseChroma: 9, DenoiseLum: -1,
	}
	p.Grade.RoundnessFloor = 9
	p.Grade.FWHMSigma = 99
	got := clampPreset(p)

	// Tier A composite knobs.
	assert.Equal(t, 0.6, got.Saturation)
	assert.Equal(t, 0.0, got.HaScreen)
	assert.Equal(t, 0.3, got.HaBlackPoint)
	assert.Equal(t, 12.0, got.ChromaBlur)
	assert.Equal(t, 0.1, got.CropFrac)
	// Tier B finish-prep knobs.
	assert.Equal(t, 0.2, got.BackgroundLevel)
	assert.Equal(t, 4, got.BackgroundDegree)
	assert.Equal(t, 1.0, got.StarReduce)
	// Tier C stack knobs.
	assert.Equal(t, 6.0, got.TrailMaskK)
	assert.Equal(t, 1.0, got.DenoiseChroma)
	assert.Equal(t, 0.0, got.DenoiseLum)
	assert.Equal(t, 0.95, got.Grade.RoundnessFloor)
	assert.Equal(t, 5.0, got.Grade.FWHMSigma)
}

func TestSupervisePatch_ApplyPartialOnlyOverrides(t *testing.T) {
	base := mode.For(mode.Deepsky)
	sat, bg := 0.30, 0.09
	no := false
	got := supervisePatch{Saturation: &sat, BackgroundLevel: &bg, ColorCalibration: &no}.apply(base)

	assert.Equal(t, 0.30, got.Saturation)        // Tier A overridden
	assert.Equal(t, 0.09, got.BackgroundLevel)   // Tier B overridden
	assert.False(t, got.ColorCalibration)        // Tier B bool overridden
	assert.Equal(t, base.HaScreen, got.HaScreen) // untouched
	assert.True(t, base.ColorCalibration)        // source preset not mutated
}

func TestTierOf_HighestChangedWins(t *testing.T) {
	base := mode.For(mode.Deepsky)

	a := base
	a.Saturation += 0.1
	assert.Equal(t, tierA, tierOf(base, a))

	b := base
	b.BackgroundLevel += 0.02
	assert.Equal(t, tierB, tierOf(base, b))

	c := base
	c.Grade.FWHMSigma += 0.5
	assert.Equal(t, tierC, tierOf(base, c))

	// A change spanning B and C re-enters at the highest (C).
	bc := base
	bc.BackgroundLevel += 0.02
	bc.TrailMaskK += 1
	assert.Equal(t, tierC, tierOf(base, bc))

	assert.Equal(t, tierA, tierOf(base, base)) // no change
}

func TestComposeChanged(t *testing.T) {
	base := mode.For(mode.Deepsky)
	assert.False(t, composeChanged(base, base))
	c := base
	c.HaScreen += 0.05
	assert.True(t, composeChanged(base, c))
}

func TestAffordableTier(t *testing.T) {
	assert.Equal(t, tierC, affordableTier(tierC, 3, 2))
	assert.Equal(t, tierB, affordableTier(tierC, 3, 0)) // Tier-C budget spent → cap at B
	assert.Equal(t, tierA, affordableTier(tierC, 0, 0)) // B+C spent → cap at A
	assert.Equal(t, tierB, affordableTier(tierB, 3, 2)) // ceiling caps at B
	assert.Equal(t, tierA, affordableTier(tierA, 3, 2)) // ceiling caps at A
}

func TestCapToTier_RevertsAboveTier(t *testing.T) {
	base := mode.For(mode.Deepsky)
	cand := base
	cand.Saturation += 0.1       // Tier A
	cand.BackgroundLevel += 0.02 // Tier B
	cand.TrailMaskK += 1         // Tier C

	capB := capToTier(base, cand, tierB)
	assert.Equal(t, cand.Saturation, capB.Saturation)
	assert.Equal(t, cand.BackgroundLevel, capB.BackgroundLevel)
	assert.Equal(t, base.TrailMaskK, capB.TrailMaskK) // Tier C reverted

	capA := capToTier(base, cand, tierA)
	assert.Equal(t, cand.Saturation, capA.Saturation)
	assert.Equal(t, base.BackgroundLevel, capA.BackgroundLevel) // Tier B reverted
	assert.Equal(t, base.TrailMaskK, capA.TrailMaskK)           // Tier C reverted
}

func TestParseDecision(t *testing.T) {
	tests := []struct {
		name      string
		reply     string
		wantScore float64
		wantDone  bool
		wantSat   *float64
	}{
		{"plain json done", `{"score":8,"done":true,"assessment":"clean"}`, 8, true, nil},
		{"fenced json", "```json\n{\"score\":7.5,\"done\":false}\n```", 7.5, false, nil},
		{"prose-wrapped action", `Sure: {"score":6,"action":{"tier":"A","patch":{"saturation":0.25}}} ok`, 6, false, fptr(0.25)},
		{"garbage falls back to neutral", "not json at all", 5, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := parseDecision(tt.reply)
			assert.Equal(t, tt.wantScore, d.Score)
			assert.Equal(t, tt.wantDone, d.Done)
			if tt.wantSat == nil {
				if d.Action != nil && d.Action.Patch != nil {
					assert.Nil(t, d.Action.Patch.Saturation)
				}
				return
			}
			require.NotNil(t, d.Action)
			require.NotNil(t, d.Action.Patch)
			require.NotNil(t, d.Action.Patch.Saturation)
			assert.Equal(t, *tt.wantSat, *d.Action.Patch.Saturation)
		})
	}
}

func TestParseDecision_DefectsAndBoolPatch(t *testing.T) {
	d := parseDecision(`{"score":5,"defects":[{"kind":"gradient","severity":"high","note":"light pollution"}],
	  "action":{"tier":"B","patch":{"combined_background_ai":true}}}`)
	require.Len(t, d.Defects, 1)
	assert.Equal(t, "gradient", d.Defects[0].Kind)
	assert.Equal(t, "high", d.Defects[0].Severity)
	require.NotNil(t, d.Action)
	require.NotNil(t, d.Action.Patch)
	require.NotNil(t, d.Action.Patch.CombinedBackgroundAI)
	assert.True(t, *d.Action.Patch.CombinedBackgroundAI)
}

func TestScoreFinish(t *testing.T) {
	clean := finishMetrics{Background: 0.06}
	assert.InDelta(t, 10.0, scoreFinish(clean, 0.06), 1e-9)

	clipped := finishMetrics{BlackClip: [3]float64{0.2, 0, 0}, Background: 0.06}
	assert.Less(t, scoreFinish(clipped, 0.06), 10.0)

	cast := finishMetrics{GreenCast: 0.3, Background: 0.06}
	assert.Less(t, scoreFinish(cast, 0.06), 10.0)

	// A thoroughly broken render floors at 0 rather than going negative.
	awful := finishMetrics{BlackClip: [3]float64{1, 1, 1}, WhiteClip: [3]float64{1, 1, 1}, GreenCast: 1}
	assert.GreaterOrEqual(t, scoreFinish(awful, 0.06), 0.0)
}

func fptr(v float64) *float64 { return &v }
