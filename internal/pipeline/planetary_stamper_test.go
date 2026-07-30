package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The stamper must inject the held Index/Total onto step-only events (Siril log lines arrive with
// Total 0 and would otherwise reset the job bar to 0), pass through events that carry their own Total,
// and throttle duplicate percent updates.
func TestPlanetaryStamper_StampsHeldPercentOntoLineEvents(t *testing.T) {
	var got []Progress
	s := newPlanetaryStamper(func(p Progress) { got = append(got, p) })

	s.setPercent(12.3)                                       // → Index 123 / Total 1000
	s.forward(Progress{Step: "planetary", Line: "log line"}) // Total 0 → stamped
	s.forward(Progress{Step: "masters", Line: "another"})    // any step-only event → stamped
	s.setPercent(12.34)                                      // same Index 123 → throttled, no event
	s.forward(Progress{Step: "deep", Index: 2, Total: 4})    // carries a Total → untouched
	s.setPercent(50)                                         // → Index 500

	require.Len(t, got, 5)
	assert.Equal(t, Progress{Step: "planetary", Index: 123, Total: 1000}, got[0])
	assert.Equal(t, 123, got[1].Index)
	assert.Equal(t, 1000, got[1].Total)
	assert.Equal(t, "log line", got[1].Line)
	assert.Equal(t, 123, got[2].Index)
	assert.Equal(t, Progress{Step: "deep", Index: 2, Total: 4}, got[3])
	assert.Equal(t, 500, got[4].Index)
}

func TestPlanetaryStamper_NilSink(t *testing.T) {
	s := newPlanetaryStamper(nil)
	s.setPercent(10)
	s.forward(Progress{Step: "planetary", Line: "x"}) // must not panic
}
