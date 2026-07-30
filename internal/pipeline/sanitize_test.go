package pipeline

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeNonFinite(t *testing.T) {
	type inner struct {
		WFWHM float64 `json:"wfwhm"`
	}
	type record struct {
		Name    string             `json:"name"`
		Score   float64            `json:"score"`
		Metrics []inner            `json:"metrics"`
		ByKey   map[string]float64 `json:"by_key"`
		Rot     *float64           `json:"rot,omitempty"`
	}

	nan := math.NaN()
	tests := []struct {
		name      string
		in        *record
		wantPaths []string
	}{
		{
			name:      "finite record untouched",
			in:        &record{Name: "ok", Score: 1.5, Metrics: []inner{{WFWHM: 2.1}}},
			wantPaths: nil,
		},
		{
			name:      "NaN struct field zeroed",
			in:        &record{Score: math.NaN()},
			wantPaths: []string{"score"},
		},
		{
			name:      "Inf in nested slice zeroed",
			in:        &record{Metrics: []inner{{WFWHM: 1}, {WFWHM: math.Inf(1)}}},
			wantPaths: []string{"metrics[1].wfwhm"},
		},
		{
			name:      "NaN map value zeroed",
			in:        &record{ByKey: map[string]float64{"a": math.NaN()}},
			wantPaths: []string{"by_key[a]"},
		},
		{
			name:      "NaN behind pointer zeroed",
			in:        &record{Rot: &nan},
			wantPaths: []string{"rot"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeNonFinite(tt.in)
			assert.Equal(t, tt.wantPaths, got)
			// The whole point: the record must be serializable afterwards.
			_, err := json.Marshal(tt.in)
			require.NoError(t, err)
		})
	}
}

func TestNonFiniteNote_BoundsThePathList(t *testing.T) {
	paths := make([]string, 12)
	for i := range paths {
		paths[i] = "p"
	}
	note := nonFiniteNote(paths)
	assert.Contains(t, note, "… and 4 more")
}
