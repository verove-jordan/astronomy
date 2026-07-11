package noise

import "math"

// Noise-envelope constants for the adaptive strength curve, in [0,1] Siril master units. Below
// adaptiveSigmaLo the frame is treated as clean, above adaptiveSigmaHi as very noisy.
const (
	adaptiveSigmaLo = 4e-4
	adaptiveSigmaHi = 6e-3
)

// AdaptiveFactor maps a measured noise sigma (and, as a fallback, the number of stacked frames) to a
// denoise-strength multiplier in [0.6, 1.4]: mid-noise -> 1.0, a quiet frame -> 0.6, an ugly one -> 1.4.
// When sigma is unavailable (<=0 or non-finite) it falls back to a frame-count heuristic (fewer
// stacked frames -> more noise -> stronger denoise).
func AdaptiveFactor(sigma float64, stackedFrames int) float64 {
	var a float64
	if sigma > 0 && isFinite(sigma) {
		a = clamp(math.Log(sigma/adaptiveSigmaLo)/math.Log(adaptiveSigmaHi/adaptiveSigmaLo), 0, 1)
	} else {
		a = clamp((48-float64(stackedFrames))/40, 0.1, 0.9)
	}
	return 0.6 + 0.8*a
}
