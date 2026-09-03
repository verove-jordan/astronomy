package inspect

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The numbers below were measured from the 2026-08-11 phone session by running the classifier's own
// statistics over its own downscaled thumbnails, so they are what the rules actually see.
var (
	// A capped-lens bias: nothing but read noise. Its robust spread is exactly zero, which collapsed
	// the peak threshold onto the median and turned 75 noise ripples into a "star field".
	phoneBias = frameStat{exposureMs: 67, median: 0, mad: 0, brightFrac: 0.00052, peaks: 75, hasStats: true}
	// A capped-lens dark at the same pointing.
	phoneDark = frameStat{exposureMs: 10000, median: 0, mad: 0, brightFrac: 0.00002, peaks: 2, hasStats: true}
	// Real 10-second lights of the sea horizon. At thumbnail scale the Milky Way is a smooth glow,
	// so the peak count straddles the eight needed to be called a light — these three were
	// classified DARK and would have been subtracted from their own siblings.
	horizonLightA = frameStat{exposureMs: 10000, median: 0.28419, mad: 0.10755, brightFrac: 0.00125, peaks: 5, hasStats: true}
	horizonLightB = frameStat{exposureMs: 10000, median: 0.26863, mad: 0.11085, brightFrac: 0.00105, peaks: 4, hasStats: true}
	horizonLightC = frameStat{exposureMs: 10000, median: 0.28047, mad: 0.12101, brightFrac: 0.00047, peaks: 0, hasStats: true}
	// A zenith light: the Milky Way core fills the frame, so the bright fraction alone carries it.
	zenithLight = frameStat{exposureMs: 10000, median: 0.26700, mad: 0.05439, brightFrac: 0.05286, peaks: 9, hasStats: true}
)

// TestClassifyByStats_PhoneSession pins the whole batch decision on real measured statistics.
func TestClassifyByStats_PhoneSession(t *testing.T) {
	batch := []frameStat{phoneBias, phoneDark, horizonLightA, horizonLightB, horizonLightC, zenithLight}
	want := []FrameType{Bias, Dark, Light, Light, Light, Light}

	assert.Equal(t, want, classifyByStats(batch))
}

func TestHasStars(t *testing.T) {
	tests := []struct {
		name string
		stat frameStat
		want bool
	}{
		{
			name: "a capped lens is not a star field, however many noise peaks it counts",
			stat: phoneBias,
			want: false,
		},
		{
			name: "a bright region still reads as structure when the median is perfectly flat",
			// More than half the pixels share one value, so the robust spread is zero — but 11% of
			// the frame is bright. Gating the FRACTION on a noise estimate would lose this.
			stat: frameStat{median: 0.0153, mad: 0, brightFrac: 0.11, peaks: 0, hasStats: true},
			want: true,
		},
		{
			name: "peaks still count when the noise floor is measurable",
			stat: frameStat{median: 0.27, mad: 0.10, brightFrac: 0.002, peaks: 9, hasStats: true},
			want: true,
		},
		{
			name: "too few peaks and too little structure is not a light",
			stat: frameStat{median: 0.27, mad: 0.10, brightFrac: 0.002, peaks: 4, hasStats: true},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, hasStars(tt.stat))
		})
	}
}

func TestExposedLevel(t *testing.T) {
	tests := []struct {
		name    string
		batch   []frameStat
		wantOK  bool
		wantCut float64
	}{
		{
			name:  "capped frames beside real sky give a clear cut",
			batch: []frameStat{phoneBias, phoneDark, horizonLightA, zenithLight},
			// darkest 0, brightest 0.28419 → a quarter of the way up.
			wantOK: true, wantCut: 0.0710,
		},
		{
			name: "a batch whose frames all sit at a similar level says nothing",
			// This is the shape of an ordinary deep-sky session: darks are dim, not black, and a
			// median is a poor stand-in for "saw a scene" when a light's signal is a small bright
			// region. The test must stay quiet rather than read the batch backwards.
			batch:  []frameStat{{median: 0.0153, hasStats: true}, {median: 0.0458, hasStats: true}},
			wantOK: false,
		},
		{
			name:   "no measured frames, no answer",
			batch:  []frameStat{{median: 0.5}},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cut, ok := exposedLevel(tt.batch)

			require.Equal(t, tt.wantOK, ok)
			if !tt.wantOK {
				return
			}
			assert.InDelta(t, tt.wantCut, cut, 0.001)
		})
	}
}

// TestClassifyByStats_OrdinarySessionUnchanged guards the deep-sky path: when the batch does not
// clearly hold both exposed and unexposed frames, the level rule must not fire and the classifier
// must behave exactly as it did before.
func TestClassifyByStats_OrdinarySessionUnchanged(t *testing.T) {
	structured := frameStat{exposureMs: 10, median: 0.0153, mad: 0, brightFrac: 0.11, peaks: 0, hasStats: true}
	uniformDim := frameStat{exposureMs: 10, median: 0.0458, mad: 0, brightFrac: 0, peaks: 0, hasStats: true}

	got := classifyByStats([]frameStat{structured, uniformDim})

	assert.Equal(t, Light, got[0], "a frame with a bright region is a light")
	assert.True(t, isCalibration(got[1]), "a uniform dim frame co-exposed with a light is calibration, got %s", got[1])
}
