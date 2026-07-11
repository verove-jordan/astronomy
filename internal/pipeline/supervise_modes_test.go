package pipeline

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/planetary"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// rawPatch marshals a knob map into the json.RawMessage a candidateRenderer's applyPatch consumes.
func rawPatch(t *testing.T, v map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func TestCometRenderer_ApplyPatch(t *testing.T) {
	c := &cometRenderer{}
	base := mode.Preset{BackgroundLevel: 0.06, BackgroundDegree: 1, Saturation: 0}
	tests := []struct {
		name    string
		patch   map[string]any
		wantSat float64
		wantBg  float64
		wantDeg int
		changed bool
	}{
		{"saturation", map[string]any{"saturation": 0.2}, 0.2, 0.06, 1, true},
		{"saturation clamps high", map[string]any{"saturation": 5.0}, 0.6, 0.06, 1, true},
		{"background clamps low", map[string]any{"background_level": 0.001}, 0, 0.03, 1, true},
		{"degree clamps high", map[string]any{"background_degree": 9}, 0, 0.06, 4, true},
		{"empty is a no-op", map[string]any{}, 0, 0.06, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, tr, changed := c.applyPatch(base, rawPatch(t, tt.patch), tierA)
			assert.Equal(t, tierA, tr, "comet is single-stage")
			assert.Equal(t, tt.changed, changed)
			assert.InDelta(t, tt.wantSat, next.Saturation, 1e-9)
			assert.InDelta(t, tt.wantBg, next.BackgroundLevel, 1e-9)
			assert.Equal(t, tt.wantDeg, next.BackgroundDegree)
		})
	}
}

func TestNightscapeRenderer_ApplyPatch(t *testing.T) {
	n := &nightscapeRenderer{}
	base := mode.Preset{Look: "natural", BackgroundLevel: 0.05}

	t.Run("look and brightness change", func(t *testing.T) {
		next, tr, changed := n.applyPatch(base, rawPatch(t, map[string]any{"look": "deepsky", "brightness": 0.09}), tierA)
		assert.Equal(t, tierA, tr)
		assert.True(t, changed)
		assert.Equal(t, "deepsky", next.Look)
		assert.InDelta(t, 0.09, next.BackgroundLevel, 1e-9)
	})
	t.Run("unknown look is ignored", func(t *testing.T) {
		next, _, changed := n.applyPatch(base, rawPatch(t, map[string]any{"look": "bogus"}), tierA)
		assert.Equal(t, "natural", next.Look)
		assert.False(t, changed)
	})
	t.Run("brightness clamps high", func(t *testing.T) {
		next, _, changed := n.applyPatch(base, rawPatch(t, map[string]any{"brightness": 0.5}), tierA)
		assert.True(t, changed)
		assert.InDelta(t, 0.2, next.BackgroundLevel, 1e-9)
	})
}

func TestPlanetaryRenderer_ApplyPatch(t *testing.T) {
	c := &planetaryRenderer{}
	base := mode.Preset{Planetary: planetary.Options{Finish: siril.DefaultPlanetaryFinish()}}

	t.Run("sharpen and saturation, sharpen clamps", func(t *testing.T) {
		next, tr, changed := c.applyPatch(base, rawPatch(t, map[string]any{"sharpen": 10.0, "saturation": 0.5}), tierA)
		assert.Equal(t, tierA, tr)
		assert.True(t, changed)
		assert.InDelta(t, 2.5, next.Planetary.Finish.Sharpen, 1e-9, "clamped to max")
		assert.InDelta(t, 0.5, next.Planetary.Finish.Saturation, 1e-9)
		// Untouched knobs keep their defaults.
		assert.InDelta(t, 0.6, next.Planetary.Finish.Stretch, 1e-9)
	})
	t.Run("empty patch is a no-op", func(t *testing.T) {
		_, _, changed := c.applyPatch(base, rawPatch(t, map[string]any{}), tierA)
		assert.False(t, changed)
	})
}

// TestModeRenderers_ParamsAndTiers pins the single-stage contract the shared loop relies on: every
// non-deepsky renderer starts and stays at tierA, and its params map exposes the knobs it tunes.
func TestModeRenderers_ParamsAndTiers(t *testing.T) {
	comet := &cometRenderer{}
	assert.Equal(t, tierA, comet.firstTier())
	assert.Equal(t, tierA, comet.maxTier(Options{}))
	assert.Contains(t, comet.params(mode.Preset{Saturation: 0.3}), "saturation")

	night := &nightscapeRenderer{}
	assert.Equal(t, tierA, night.firstTier())
	assert.Contains(t, night.params(mode.Preset{BackgroundLevel: 0.05}), "brightness")

	plan := &planetaryRenderer{}
	assert.Equal(t, tierA, plan.firstTier())
	p := plan.params(mode.Preset{Planetary: planetary.Options{Finish: siril.DefaultPlanetaryFinish()}})
	assert.Contains(t, p, "sharpen")
	assert.InDelta(t, 1.0, p["sharpen"], 1e-9)
}
