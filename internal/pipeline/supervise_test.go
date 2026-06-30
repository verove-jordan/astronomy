package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComposeParams_Clamp(t *testing.T) {
	got := composeParams{Saturation: 9, HaScreen: -1, HaBlackPoint: 5, ChromaBlur: 99, CropFrac: 1}.clamp()
	assert.Equal(t, composeParams{Saturation: 0.6, HaScreen: 0, HaBlackPoint: 0.3, ChromaBlur: 12, CropFrac: 0.1}, got)
}

func TestComposeParamsPatch_Apply_PartialOnlyOverrides(t *testing.T) {
	cur := composeParams{Saturation: 0.16, HaScreen: 0.42, HaBlackPoint: 0.12, ChromaBlur: 0, CropFrac: 0.035}
	sat := 0.30
	got := composeParamsPatch{Saturation: &sat}.apply(cur)

	assert.Equal(t, 0.30, got.Saturation)   // overridden
	assert.Equal(t, 0.42, got.HaScreen)     // untouched
	assert.Equal(t, 0.12, got.HaBlackPoint) // untouched
	assert.Equal(t, 0.035, got.CropFrac)    // untouched
}

func TestParseDecision(t *testing.T) {
	tests := []struct {
		name      string
		reply     string
		wantScore float64
		wantDone  bool
		wantSat   *float64
	}{
		{"plain json", `{"score":8,"done":true,"reasoning":"clean"}`, 8, true, nil},
		{"fenced json", "```json\n{\"score\":7.5,\"done\":false}\n```", 7.5, false, nil},
		{"prose wrapped", `Sure: {"score":6,"next":{"saturation":0.25}} done`, 6, false, fptr(0.25)},
		{"garbage falls back to neutral", "not json at all", 5, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := parseDecision(tt.reply)
			assert.Equal(t, tt.wantScore, d.Score)
			assert.Equal(t, tt.wantDone, d.Done)
			if tt.wantSat == nil {
				if d.Next != nil {
					assert.Nil(t, d.Next.Saturation)
				}
				return
			}
			require.NotNil(t, d.Next)
			require.NotNil(t, d.Next.Saturation)
			assert.Equal(t, *tt.wantSat, *d.Next.Saturation)
		})
	}
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
