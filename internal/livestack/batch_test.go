package livestack

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/verove-jordan/astronomy/internal/inspect"
)

func TestHumanDur(t *testing.T) {
	cases := []struct {
		name string
		ms   int64
		want string
	}{
		{"seconds", 45_000, "45s"},
		{"minutes", 65_000, "1m05s"},
		{"hours", 3_900_000, "1h05m"},
		{"zero", 0, "0s"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, humanDur(c.ms))
		})
	}
}

func TestOrderedFilters_CanonicalThenSorted(t *testing.T) {
	got := orderedFilters(map[string]bool{"R": true, "L": true, "Ha": true, "Z": true})
	assert.Equal(t, []string{"L", "R", "Ha", "Z"}, got)
}

func TestTag(t *testing.T) {
	cases := map[string]string{"Ha": "Ha", "": "mono", "L": "L", "S II": "S_II", "!!!": "ch"}
	for in, want := range cases {
		assert.Equal(t, want, tag(in), "tag(%q)", in)
	}
}

func TestCalibSignature_SensitiveToCalibrationOnly(t *testing.T) {
	mk := func(darkCount int) *inspect.Inventory {
		return &inspect.Inventory{Sets: []inspect.Set{
			{Key: inspect.SetKey{Type: inspect.Dark, ExposureMs: 120000, Gain: 200, Bin: 1}, Count: darkCount},
			{Key: inspect.SetKey{Type: inspect.Bias, Gain: 200, Bin: 1}, Count: 50},
			{Key: inspect.SetKey{Type: inspect.Light, Filter: "L"}, Count: 10}, // lights are ignored
		}}
	}
	base := calibSignature(mk(20))
	assert.NotEmpty(t, base)
	assert.Equal(t, base, calibSignature(mk(20)), "identical calibration → identical signature")
	assert.NotEqual(t, base, calibSignature(mk(21)), "one more dark → new signature → rebuild")
	assert.NotContains(t, base, string(inspect.Light), "lights must not affect the calibration signature")
}
