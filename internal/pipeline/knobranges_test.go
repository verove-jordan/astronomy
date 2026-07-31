package pipeline

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/mode"
)

// rangeModes is every mode whose glossary the UI documents with min/max.
var rangeModes = []mode.Mode{mode.Deepsky, mode.Planetary, mode.Comet, mode.Milkyway, mode.Sun}

// clampedParam applies a single-knob patch through the real apply/clamp path and returns the mode's
// resulting value for that knob — the ground truth KnobRangesFor must mirror.
func clampedParam(t *testing.T, m mode.Mode, key string, v float64) float64 {
	t.Helper()
	p := mode.For(m)
	raw, err := json.Marshal(map[string]float64{key: v})
	require.NoError(t, err)
	_, err = ApplyParamPatch(&p, raw)
	require.NoError(t, err)
	got := ParamsFor(p)[key]
	switch n := got.(type) {
	case int:
		return float64(n)
	case float64:
		return n
	default:
		t.Fatalf("%s param %q is not numeric (%T)", m, key, got)
		return 0
	}
}

// TestKnobRangesFor_MatchClamps drives an out-of-range value into each knob and asserts the run's own
// clamp lands it exactly on the advertised Min/Max — so a bound edited in a clamp but not in
// KnobRangesFor (or vice-versa) fails here. The under-range probe stays > 0 (Min-0.001) so the knobs
// whose 0 is an "off" value (Min ≥ 0.2) still exercise their real [Min,Max] clamp rather than the off path.
func TestKnobRangesFor_MatchClamps(t *testing.T) {
	for _, m := range rangeModes {
		for key, r := range KnobRangesFor(m) {
			key, r := key, r
			t.Run(string(m)+"/"+key, func(t *testing.T) {
				lo, hi := r.Min-0.001, r.Max+1000
				if r.Int {
					lo, hi = r.Min-1, r.Max+1
				}
				assert.InDelta(t, r.Min, clampedParam(t, m, key, lo), 1e-9, "under-range should clamp up to Min")
				assert.InDelta(t, r.Max, clampedParam(t, m, key, hi), 1e-9, "over-range should clamp down to Max")
			})
		}
	}
}

// TestKnobRangesFor_Completeness pins the two-way contract with ParamsFor: every advertised range is a
// real knob, and every numeric knob advertises a range (so a newly added numeric knob can't ship
// without one). Booleans and enum knobs (palette/look) intentionally have no range.
func TestKnobRangesFor_Completeness(t *testing.T) {
	for _, m := range rangeModes {
		params := ParamsFor(mode.For(m))
		ranges := KnobRangesFor(m)
		for k := range ranges {
			_, ok := params[k]
			assert.Truef(t, ok, "%s: range key %q is not a real param", m, k)
		}
		for k, v := range params {
			switch reflect.ValueOf(v).Kind() {
			case reflect.Int, reflect.Int64, reflect.Float32, reflect.Float64:
				_, ok := ranges[k]
				assert.Truef(t, ok, "%s: numeric param %q has no range", m, k)
			}
		}
	}
}
