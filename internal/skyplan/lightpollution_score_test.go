package skyplan

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLightPollutionScore(t *testing.T) {
	const faint = 1.0       // faint broadband target (max moonlight/skyglow sensitivity)
	const narrowband = 0.25 // emission nebula — shrugs off skyglow

	tests := []struct {
		name      string
		sqm, sens float64
		want      float64
		delta     float64
	}{
		{"pristine never penalizes", 21.8, faint, 1.0, 0.001},
		{"unknown site → no penalty", 0, faint, 1.0, 0.001},
		{"city crushes faint broadband", 17.8, faint, 0.25, 0.02},
		{"city barely touches narrowband", 17.8, narrowband, 0.81, 0.02},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.want, lightPollutionScore(tt.sqm, tt.sens), tt.delta)
		})
	}
	// Even an impossibly over-bright sky stays above the floor — never a hard zero.
	assert.GreaterOrEqual(t, lightPollutionScore(5, faint), lpFloor)
}

func TestLightPollutionScore_DarkerSiteScoresHigher(t *testing.T) {
	prev := -1.0
	for _, sqm := range []float64{17.8, 18.5, 19.5, 20.5, 21.3, 21.8} {
		got := lightPollutionScore(sqm, 1.0)
		assert.Greater(t, got, prev, "a darker sky (%.1f) must score at least as high", sqm)
		prev = got
	}
}

func TestComposite_LightPollutionMultiplies(t *testing.T) {
	base := SubScores{MaxAlt: 1, AltNow: 1, DarkHours: 1, Framing: 1, Detectability: 1, Moon: 1}
	w := DefaultWeights()

	full := base
	full.LightPollution = 1.0
	half := base
	half.LightPollution = 0.5
	unset := base // LightPollution == 0 → treated as no penalty

	assert.Equal(t, 100, composite(w, full))
	assert.Equal(t, 50, composite(w, half))
	assert.Equal(t, 100, composite(w, unset), "a zero-value light-pollution sub-score must not zero the score")
}
