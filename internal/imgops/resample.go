package imgops

import "math"

// Catmull-Rom bicubic resampling, shared by the planetary single-pass warp and the mosaic panel
// reprojector. Bilinear interpolation is a triangle-kernel low-pass filter (≈0.81 MTF at
// half-Nyquist per pass); a Catmull-Rom pass retains ≈0.98 — which is why every geometric resample
// in the engine goes through this kernel, exactly once per pixel's lifetime.

// CubicKernel is the Catmull-Rom (a=-0.5) piecewise-cubic weight for a sample |x| pixels from the
// target position. It is a partition of unity (the four taps sum to 1) and reproduces the exact
// pixel at x=0.
func CubicKernel(x float64) float64 {
	const a = -0.5
	x = math.Abs(x)
	switch {
	case x <= 1:
		return (a+2)*x*x*x - (a+3)*x*x + 1
	case x < 2:
		return a*x*x*x - 5*a*x*x + 8*a*x - 4*a
	default:
		return 0
	}
}

// CubicWeights returns the four Catmull-Rom taps for a fractional position t in [0,1), for source
// samples at offsets -1,0,+1,+2 from the floored integer position.
func CubicWeights(t float64) [4]float64 {
	return [4]float64{CubicKernel(t + 1), CubicKernel(t), CubicKernel(t - 1), CubicKernel(t - 2)}
}

// SampleCubic samples a plane at fractional (sx,sy) with a separable 4×4 Catmull-Rom kernel. Reads
// are edge-CLAMPED (not zero-filled): zero-fill would darken bright content where the stencil
// overhangs the frame edge (the lunar limb, a mosaic panel border).
func SampleCubic(src []float32, w, h int, sx, sy float64) float32 {
	x0 := int(math.Floor(sx))
	y0 := int(math.Floor(sy))
	wx := CubicWeights(sx - float64(x0))
	wy := CubicWeights(sy - float64(y0))
	var sum float64
	for j := 0; j < 4; j++ {
		yy := clampIdx(y0-1+j, 0, h-1)
		row := yy * w
		var rowSum float64
		for i := 0; i < 4; i++ {
			xx := clampIdx(x0-1+i, 0, w-1)
			rowSum += wx[i] * float64(src[row+xx])
		}
		sum += wy[j] * rowSum
	}
	return float32(sum)
}

func clampIdx(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
