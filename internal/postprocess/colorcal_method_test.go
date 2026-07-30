package postprocess

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCalMethod_Classification pins the ladder semantics callers branch on: which rungs count as a
// trustworthy balance (linked stretch + no GIMP green trim + SCNR allowed) and which count as real
// catalogue photometry (no "fallback" warning).
func TestCalMethod_Classification(t *testing.T) {
	tests := []struct {
		name        string
		m           CalMethod
		calibrated  bool
		photometric bool
	}{
		{"none", CalNone, false, false},
		{"spcc", CalSPCC, true, true},
		{"pcc", CalPCC, true, true},
		{"star field", CalStarField, true, false},
		{"neutralized", CalNeutralized, false, false},
		{"palette", CalPalette, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.calibrated, tt.m.Calibrated())
			assert.Equal(t, tt.photometric, tt.m.Photometric())
		})
	}
}
