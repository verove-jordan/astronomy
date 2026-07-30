package planetary

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The run ledger must emit a monotone 0→100 sequence across weighted phases with per-frame ticks,
// throttled to whole-percent changes, and be a silent no-op without a sink (CLI/MCP runs).
func TestRunProgress_MonotoneAcrossPhases(t *testing.T) {
	var got []float64
	rp := newRunProgress(func(p float64) { got = append(got, p) })

	chSpan := (100 - finishWeight) / 2.0 // two channels
	for ch := 0; ch < 2; ch++ {
		for _, w := range []float64{phaseMaterialize, phaseCalibrate, phaseScore, phaseAlign, phaseStack} {
			rp.phase(chSpan * w)
			for i := 1; i <= 20; i++ {
				rp.tick(i, 20)
			}
		}
	}
	rp.phase(finishWeight)
	rp.finish()

	require.NotEmpty(t, got)
	for i := 1; i < len(got); i++ {
		assert.GreaterOrEqual(t, got[i], got[i-1], "percent must never move backwards (i=%d)", i)
	}
	assert.Equal(t, 100.0, got[len(got)-1], "finish pins the bar at 100")
	assert.LessOrEqual(t, len(got), 102, "whole-percent throttling caps the emission count")
}

func TestRunProgress_NilSinkIsSilent(t *testing.T) {
	rp := newRunProgress(nil)
	rp.phase(50)
	rp.tick(1, 2)
	rp.finish() // must not panic
}
