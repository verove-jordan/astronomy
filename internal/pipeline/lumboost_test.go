package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBoostLumCurve(t *testing.T) {
	curve := []float64{0, 0, 0.5, 0.5, 1, 1}
	cases := []struct {
		name  string
		curve []float64
		boost float64
		want  []float64
	}{
		{"zero boost unchanged", curve, 0, curve},
		{"midpoint lifted by boost, endpoints fixed", curve, 0.1, []float64{0, 0, 0.5, 0.6, 1, 1}},
		{"empty curve synthesized", nil, 0.1, []float64{0, 0, 0.5, 0.6, 1, 1}},
		// The highlight anchor pins core/star points exactly (y ≥ 0.90): even max boost cannot
		// trade bright-core contrast for arm brightness.
		{"highlights pinned", []float64{0, 0, 0.5, 0.98, 1, 1}, 0.25, []float64{0, 0, 0.5, 0.98, 1, 1}},
		// The shadow anchor pins sky-level points (y ≤ 0.05): the flattened sky keeps its exact
		// brightness while the galaxy-periphery point (y 0.20, above the 0.18 knee) gets the full
		// lift — the deepsky LumCurve's sky point sits at y 0.025.
		{"sky point pinned, periphery lifted", []float64{0, 0, 0.04, 0.025, 0.08, 0.2, 1, 1}, 0.1,
			[]float64{0, 0, 0.04, 0.025, 0.08, 0.264, 1, 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := boostLumCurve(tc.curve, tc.boost)
			assert.InDeltaSlice(t, tc.want, got, 1e-9)
		})
	}
}

// The default deepsky LumCurve must be byte-identical through a zero boost (the knob-off contract).
func TestBoostLumCurve_ZeroIsIdentity(t *testing.T) {
	orig := []float64{0, 0, 0.04, 0.025, 0.08, 0.2, 1, 1}
	got := boostLumCurve(orig, 0)
	assert.Equal(t, orig, got)
	assert.Equal(t, &orig[0], &got[0], "zero boost must return the same slice, not a copy")
}
