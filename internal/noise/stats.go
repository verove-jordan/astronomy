package noise

import (
	"math"
	"sort"
)

// clamp constrains v to [lo,hi].
func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// isFinite reports whether v is neither NaN nor ±Inf.
func isFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// allFinite reports whether every sample of p is finite (no NaN/±Inf).
func allFinite(p []float32) bool {
	for _, v := range p {
		if v != v || math.IsInf(float64(v), 0) { // v != v is the NaN test
			return false
		}
	}
	return true
}

// copyOf returns a fresh copy of s (so an in-place sort never disturbs the caller's data).
func copyOf(s []float64) []float64 {
	out := make([]float64, len(s))
	copy(out, s)
	return out
}

// ceilDiv returns ceil(a/b) for positive b, and 0 for non-positive b.
func ceilDiv(a, b int) int {
	if b <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

// median64 returns the median of vals, sorting it in place. Empty input returns 0.
func median64(vals []float64) float64 {
	n := len(vals)
	if n == 0 {
		return 0
	}
	sort.Float64s(vals)
	if n%2 == 1 {
		return vals[n/2]
	}
	return 0.5 * (vals[n/2-1] + vals[n/2])
}

// percentileSorted returns the linear-interpolated p-th percentile (0..100) of an already-sorted
// slice (numpy default interpolation). Empty input returns 0.
func percentileSorted(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[n-1]
	}
	rank := p / 100 * float64(n-1)
	lo := int(math.Floor(rank))
	if lo+1 >= n {
		return sorted[n-1]
	}
	return sorted[lo] + (rank-float64(lo))*(sorted[lo+1]-sorted[lo])
}

// percentile64 returns the p-th percentile of vals, sorting it in place.
func percentile64(vals []float64, p float64) float64 {
	sort.Float64s(vals)
	return percentileSorted(vals, p)
}

// robustSigma returns 1.4826·MAD(vals), the MAD-based robust estimate of the Gaussian sigma of vals.
// vals is used as scratch (reordered and overwritten). Empty input returns 0.
func robustSigma(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	med := median64(vals) // sorts vals
	for i := range vals {
		vals[i] = math.Abs(vals[i] - med)
	}
	return 1.4826 * median64(vals) // median of absolute deviations
}

// tileValues appends the values of the tile (tx,ty) of a tile×tile grid over an W×H plane to out
// (as float64), clipping the tile to the image bounds. Pass out[:0] to reuse a scratch buffer.
func tileValues(src []float32, w, h, tile, tx, ty int, out []float64) []float64 {
	x0, y0 := tx*tile, ty*tile
	x1, y1 := x0+tile, y0+tile
	if x1 > w {
		x1 = w
	}
	if y1 > h {
		y1 = h
	}
	for y := y0; y < y1; y++ {
		base := y * w
		for x := x0; x < x1; x++ {
			out = append(out, float64(src[base+x]))
		}
	}
	return out
}

// subsample64 returns at most n evenly-spaced samples of p as a fresh float64 slice, for fast
// percentile estimates on large planes. n<=0 or len(p)<=n copies every sample.
func subsample64(p []float32, n int) []float64 {
	step := 1
	if n > 0 && len(p) > n {
		step = len(p) / n
	}
	out := make([]float64, 0, len(p)/step+1)
	for i := 0; i < len(p); i += step {
		out = append(out, float64(p[i]))
	}
	return out
}

// smoothstep is the Hermite S-curve: 0 for x<=a, 1 for x>=b, smooth in between.
func smoothstep(a, b, x float64) float64 {
	if a >= b {
		if x < a {
			return 0
		}
		return 1
	}
	t := clamp((x-a)/(b-a), 0, 1)
	return t * t * (3 - 2*t)
}

// softThreshold applies the soft (shrinkage) threshold: sign(v)·max(|v|−t, 0).
func softThreshold(v, t float64) float64 {
	a := math.Abs(v) - t
	if a <= 0 {
		return 0
	}
	return math.Copysign(a, v)
}

// gridCoord maps a pixel index along one axis to fractional tile-grid coordinates, placing a tile's
// center at the integer grid index (pixel center (i+0.5) at tile center -> grid index tx).
func gridCoord(i, tile int) float64 {
	return (float64(i)+0.5)/float64(tile) - 0.5
}

// bilinearSample bilinearly interpolates a gw×gh grid at fractional coordinate (gx,gy), clamping to
// the grid edges. A 1×1 grid returns its single value.
func bilinearSample(grid []float64, gw, gh int, gx, gy float64) float64 {
	gx = clamp(gx, 0, float64(gw-1))
	gy = clamp(gy, 0, float64(gh-1))
	x0, y0 := int(math.Floor(gx)), int(math.Floor(gy))
	x1, y1 := x0+1, y0+1
	if x1 > gw-1 {
		x1 = gw - 1
	}
	if y1 > gh-1 {
		y1 = gh - 1
	}
	fx, fy := gx-float64(x0), gy-float64(y0)
	v00, v10 := grid[y0*gw+x0], grid[y0*gw+x1]
	v01, v11 := grid[y1*gw+x0], grid[y1*gw+x1]
	top := v00 + fx*(v10-v00)
	bot := v01 + fx*(v11-v01)
	return top + fy*(bot-top)
}
