package noise

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAdaptiveFactor_SigmaCurve(t *testing.T) {
	tests := []struct {
		name  string
		sigma float64
		want  float64
	}{
		{"at low anchor", adaptiveSigmaLo, 0.6},
		{"below low clamps", 1e-4, 0.6},
		{"geometric mid", math.Sqrt(adaptiveSigmaLo * adaptiveSigmaHi), 1.0},
		{"at high anchor", adaptiveSigmaHi, 1.4},
		{"above high clamps", 1e-2, 1.4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.want, AdaptiveFactor(tt.sigma, 0), 1e-9)
		})
	}
}

func TestAdaptiveFactor_MonotonicInSigma(t *testing.T) {
	sigmas := []float64{1e-4, 5e-4, 1e-3, 2e-3, 4e-3, 6e-3, 1e-2}
	var prev float64
	for i, s := range sigmas {
		f := AdaptiveFactor(s, 0)
		assert.GreaterOrEqualf(t, f, 0.6, "factor below floor at sigma %g", s)
		assert.LessOrEqualf(t, f, 1.4, "factor above ceil at sigma %g", s)
		if i > 0 {
			assert.GreaterOrEqualf(t, f, prev, "not monotonic at sigma %g", s)
		}
		prev = f
	}
}

func TestAdaptiveFactor_FrameFallback(t *testing.T) {
	tests := []struct {
		name   string
		sigma  float64
		frames int
		want   float64
	}{
		{"nan uses frames", math.NaN(), 28, 1.0},
		{"zero uses frames", 0, 28, 1.0},
		{"negative uses frames", -1, 28, 1.0},
		{"few frames -> strong", 0, 8, 1.32},
		{"many frames clamp low", 0, 88, 0.68},
		{"zero frames clamp high", 0, 0, 1.32},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.want, AdaptiveFactor(tt.sigma, tt.frames), 1e-9)
		})
	}
}

func TestAdaptiveFactor_FrameFallbackMonotonic(t *testing.T) {
	frames := []int{0, 8, 18, 28, 38, 48, 88}
	var prev float64 = math.Inf(1)
	for _, n := range frames {
		f := AdaptiveFactor(math.NaN(), n)
		assert.LessOrEqualf(t, f, prev+1e-12, "factor should not increase with frames at %d", n)
		prev = f
	}
}
