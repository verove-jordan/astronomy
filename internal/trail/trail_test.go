package trail

import (
	"math"
	"testing"
)

// TestParams_VoteFracDefaultsToTheConstant: every existing caller passes a zero VoteFrac and must
// keep the satellite-tuned behaviour exactly.
func TestParams_VoteFracDefaultsToTheConstant(t *testing.T) {
	tests := []struct {
		name string
		p    Params
		want float64
	}{
		{"unset keeps the default", DefaultParams(3), trailVoteFrac},
		{"raw params too", RawParams(5), trailVoteFrac},
		{"an override is used", Params{VoteFrac: 0.08}, 0.08},
		{"a negative is ignored", Params{VoteFrac: -1}, trailVoteFrac},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.voteFrac(); got != tt.want {
				t.Fatalf("voteFrac() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestParams_RawSeedKDefaultsToTheConstant: Raw mode ignores K by design, so the override must be its
// own field and must leave every existing caller exactly where it was.
func TestParams_RawSeedKDefaultsToTheConstant(t *testing.T) {
	tests := []struct {
		name string
		p    Params
		want float64
	}{
		{"RawParams keeps the constant whatever k it is given", RawParams(3), trailBrightK},
		{"and with zero", RawParams(0), trailBrightK},
		{"an explicit override is used", Params{Mode: Raw, RawSeedK: 12}, 12},
		{"residual mode is unaffected", Params{Mode: Residual, K: 3, RawSeedK: 12}, 2.1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := seedK(tt.p); math.Abs(got-tt.want) > 1e-9 {
				t.Fatalf("seedK() = %v, want %v", got, tt.want)
			}
		})
	}
}
