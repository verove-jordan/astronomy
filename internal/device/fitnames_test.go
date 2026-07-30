package device_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/device"
)

// The slot count belongs to the HARDWARE; the names are user configuration that outlives any one
// wheel. Reconciling them is what stops a 7-filter setup from making a 5-slot wheel offer slots 6
// and 7 — which the UI did, and the driver then refused the move.
func TestFitFilterNames(t *testing.T) {
	// A configuration saved for a bigger wheel must be truncated, not carried over.
	assert.Equal(t, []string{"L", "R", "G", "B", "Ha"},
		device.FitFilterNames([]string{"L", "R", "G", "B", "Ha", "OIII", "SII"}, 5))

	// A shorter configuration must be padded, so every physical slot is still addressable.
	assert.Equal(t, []string{"L", "R", "", "", ""},
		device.FitFilterNames([]string{"L", "R"}, 5))

	// An empty middle slot is meaningful — nothing is fitted there — so it must survive rather than
	// letting every later filter shift up a slot.
	assert.Equal(t, []string{"L", "", "G", "B", "Ha"},
		device.FitFilterNames([]string{"L", "", "G", "B", "Ha"}, 5))

	assert.Equal(t, []string{"L", "R", "G", "B", "Ha", "OIII", "SII"},
		device.FitFilterNames([]string{"L", "R", "G", "B", "Ha", "OIII", "SII"}, 7))

	// No wheel means no slots — not a stale list from whatever was connected last.
	assert.Nil(t, device.FitFilterNames([]string{"L", "R"}, 0))
	assert.Nil(t, device.FitFilterNames([]string{"L", "R"}, -1))
	assert.Equal(t, []string{"", ""}, device.FitFilterNames(nil, 2))
}
