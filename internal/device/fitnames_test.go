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

// The exposure token must survive a microsecond exposure.
//
// It did not: three decimals named every frame shorter than half a millisecond "0sec", so a 32 µs
// bias — the ASI's own minimum — carried a name contradicting the EXPTIME card beside it. The
// filename exists to be an independent copy of the header; one that disagrees is worse than none.
func TestFrameMeta_FileName_ExposureToken(t *testing.T) {
	tests := []struct {
		name       string
		exposureUs int64
		want       string
	}{
		{"a normal sub", 30_000_000, "30sec"},
		{"a flat", 200_000, "0.2sec"},
		{"a millisecond", 1_000, "0.001sec"},
		{"the ASI minimum", 32, "0.000032sec"},
		{"one microsecond", 1, "0.000001sec"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := device.FrameMeta{Type: "light", ExposureUs: tt.exposureUs, Bin: 1}.FileName(1)
			assert.Contains(t, got, "_"+tt.want+"_")
		})
	}
}
