package rawconv

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDcrawThumbArgs_PreservesLevelsForMeasurement pins the flag that separates a preview develop
// from a measurement develop. Without -W, dcraw_emu normalises every frame to its own range, and a
// classifier that compares frames with each other then reads nonsense: on a real session it called
// the 27 horizon lights darks and the darks and bias lights.
func TestDcrawThumbArgs_PreservesLevelsForMeasurement(t *testing.T) {
	tests := []struct {
		name         string
		maxEdge      int
		autoBrighten bool
		wantW        bool
		wantHalf     bool
	}{
		{"measurement keeps levels comparable", 512, false, true, true},
		{"a preview may be brightened", 512, true, false, true},
		{"full size skips the half-size decode", 0, false, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dcrawThumbArgs(tt.maxEdge, tt.autoBrighten)

			assert.Equal(t, tt.wantW, contains(got, "-W"), "-W in %v", got)
			assert.Equal(t, tt.wantHalf, contains(got, "-h"), "-h in %v", got)
			assert.True(t, contains(got, "-6"), "16-bit output")
			assert.True(t, contains(got, "-w"), "camera white balance")
		})
	}
}

func contains(v []string, want string) bool {
	for _, s := range v {
		if s == want {
			return true
		}
	}
	return false
}
