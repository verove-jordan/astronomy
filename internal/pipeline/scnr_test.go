package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/postprocess"
)

// scnrAfter mirrors the finish's gate: SCNR follows a trustworthy colour balance, EXCEPT PCC's.
func scnrAfter(m postprocess.CalMethod) bool { return m.Calibrated() && m != postprocess.CalPCC }

// SCNR is one-sided — it can only ever LOWER green — so it is only safe where a known green excess
// exists to remove. SPCC leaves one; the star-field rung's warm anchor makes its residual green real
// cast. PCC's balance leaves neither, so all SCNR can do there is push green down and red/blue up,
// which reads as magenta on a frame whose signal rides at 1e-4 over a 0.2449 pedestal.
func TestSCNRGate(t *testing.T) {
	tests := []struct {
		name string
		m    postprocess.CalMethod
		want bool
	}{
		{"SPCC leaves a known green cast", postprocess.CalSPCC, true},
		{"star-field is warm-anchored, so its green is real cast", postprocess.CalStarField, true},
		{"PCC needs no green removal and is harmed by it", postprocess.CalPCC, false},
		{"the neutralization fallback strips green itself", postprocess.CalNeutralized, false},
		{"a narrowband palette IS its colour", postprocess.CalPalette, false},
		{"uncalibrated colour is left to the GIMP trim", postprocess.CalNone, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, scnrAfter(tt.m))
		})
	}
	// The distinction only matters because PCC counts as trustworthy everywhere else.
	assert.True(t, postprocess.CalPCC.Calibrated(), "PCC still earns the linked stretch")
	assert.True(t, postprocess.CalPCC.Photometric(), "and still counts as real photometry")
}
