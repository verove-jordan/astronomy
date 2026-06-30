package lightpollution

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSqmToBortle(t *testing.T) {
	tests := []struct {
		name string
		sqm  float64
		want int
	}{
		{"pristine", 22.0, 1},
		{"truly dark", 21.95, 2},
		{"rural", 21.75, 3},
		{"rural-suburban", 21.4, 4},
		{"suburban", 20.8, 5},
		{"bright suburban", 20.0, 6},
		{"suburban-urban", 19.2, 7},
		{"city", 18.6, 8},
		{"inner city", 17.5, 9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sqmToBortle(tt.sqm))
		})
	}
}

func TestValueToSQM(t *testing.T) {
	tests := []struct {
		name string
		v    float64
		unit string
		want float64 // -1 = only assert ordering elsewhere
	}{
		{"sqm passthrough", 21.3, "sqm", 21.3},
		{"empty unit is sqm", 20.1, "", 20.1},
		{"bortle 4", 4, "bortle", 21.47},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.want, valueToSQM(tt.v, tt.unit), 0.001)
		})
	}
}

func TestRadianceToSQM_MonotonicAndBounded(t *testing.T) {
	// Brighter (more radiance) must never yield a darker SQM, and the result stays in the physical
	// range. Very low radiances saturate at the pristine cap, so monotonicity is non-strict there.
	prev := 99.0
	for _, r := range []float64{0.0, 0.1, 0.25, 1, 5, 20, 100, 1000} {
		got := radianceToSQM(r)
		assert.LessOrEqual(t, got, pristineSQM)
		assert.GreaterOrEqual(t, got, 16.0)
		assert.LessOrEqual(t, got, prev, "SQM must not increase as radiance %.2f grows", r)
		prev = got
	}
	// Above the saturation floor the mapping is strictly decreasing.
	assert.Less(t, radianceToSQM(100), radianceToSQM(1))
}

func TestLuminanceToSQM_DarkSiteNearPristine(t *testing.T) {
	// ~0.171 mcd/m² is the natural background — it should map near 22.0 mag/arcsec².
	assert.InDelta(t, 22.0, luminanceToSQM(0.171), 0.2)
	assert.Less(t, luminanceToSQM(10), luminanceToSQM(0.171))
}

func TestExpandURLs(t *testing.T) {
	assert.Equal(t,
		"https://x/q?lat=48.856600&lon=2.352200&k=secret",
		expandPointURL("https://x/q?lat={lat}&lon={lon}&k={key}", 48.8566, 2.3522, "secret"))
	assert.Equal(t,
		"https://t/5/15/10.png?k=secret",
		expandTileURL("https://t/{z}/{x}/{y}.png?k={key}", 5, 15, 10, "secret"))
}
