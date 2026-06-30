package skyevents

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLightPollutionFactorValue(t *testing.T) {
	faint := &Event{Kind: "comet"}
	bright := &Event{Kind: "opposition"} // not faint

	tests := []struct {
		name string
		e    *Event
		sqm  float64
		want float64
	}{
		{"pristine site no penalty", faint, 21.8, 1.0},
		{"unknown site no penalty", faint, 0, 1.0},
		{"bright event ignores skyglow", bright, 17.8, 1.0},
		{"city dims a faint comet", faint, 17.8, 0.30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.want, lightPollutionFactorValue(tt.e, tt.sqm), 0.02)
		})
	}
}

func TestObservability_DarkSiteBeatsCity(t *testing.T) {
	// A well-placed faint comet with no Moon: only light pollution should differ between the two sites.
	comet := &Event{Kind: "comet", AltAtBestDeg: 60, MoonIllum: 0, MoonSepDeg: 90}
	dark := observability(comet, 21.8)
	city := observability(comet, 17.8)
	require.InDelta(t, 1.0, dark, 0.001)
	assert.Less(t, city, dark)
	assert.GreaterOrEqual(t, city, lpEvFloor-0.001)
}

func TestObservability_BrightEventSiteIndependent(t *testing.T) {
	planet := &Event{Kind: "opposition", AltAtBestDeg: 60}
	assert.Equal(t, observability(planet, 21.8), observability(planet, 17.8))
}
