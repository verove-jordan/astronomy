package imgops

import "math"

// ReflectIndex maps an out-of-range index to its 'reflect' (half-sample symmetric) position, the
// SciPy default boundary mode: (... d c b a | a b c d | d c b a ...).
func ReflectIndex(i, n int) int {
	if n == 1 {
		return 0
	}
	for i < 0 || i >= n {
		if i < 0 {
			i = -i - 1
		}
		if i >= n {
			i = 2*n - i - 1
		}
	}
	return i
}

// GaussianBlur approximates a Gaussian filter (≈ scipy.ndimage.gaussian_filter) using three box
// passes — O(N) regardless of sigma, which matters because gradient/defect models use sigmas of a
// few hundred pixels. Boundaries clamp to the edge. sigma<=0 returns a copy.
func GaussianBlur(src []float32, w, h int, sigma float64) []float32 {
	out := make([]float32, len(src))
	copy(out, src)
	if sigma <= 0 {
		return out
	}
	tmp := make([]float32, len(src))
	for _, bw := range boxesForGauss(sigma, 3) {
		r := (bw - 1) / 2
		if r < 1 {
			continue
		}
		boxBlurH(out, tmp, w, h, r)
		boxBlurV(tmp, out, w, h, r)
	}
	return out
}

// boxesForGauss returns n odd box widths whose successive application approximates a Gaussian of
// the given sigma (Kovesi / Kutskir's fast-Gaussian construction).
func boxesForGauss(sigma float64, n int) []int {
	wIdeal := math.Sqrt(12*sigma*sigma/float64(n) + 1)
	wl := int(math.Floor(wIdeal))
	if wl%2 == 0 {
		wl--
	}
	wu := wl + 2
	mIdeal := (12*sigma*sigma - float64(n*wl*wl) - float64(4*n*wl) - float64(3*n)) / float64(-4*wl-4)
	m := int(math.Round(mIdeal))
	sizes := make([]int, n)
	for i := range sizes {
		if i < m {
			sizes[i] = wl
		} else {
			sizes[i] = wu
		}
	}
	return sizes
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// boxBlurH applies a horizontal moving-average of radius r (edge-clamped) via a running sum,
// writing into dst.
func boxBlurH(src, dst []float32, w, h, r int) {
	inv := 1.0 / float64(2*r+1)
	for y := 0; y < h; y++ {
		base := y * w
		acc := 0.0
		for k := -r; k <= r; k++ {
			acc += float64(src[base+clampInt(k, 0, w-1)])
		}
		for x := 0; x < w; x++ {
			dst[base+x] = float32(acc * inv)
			acc += float64(src[base+clampInt(x+r+1, 0, w-1)]) - float64(src[base+clampInt(x-r, 0, w-1)])
		}
	}
}

// boxBlurV applies a vertical moving-average of radius r (edge-clamped) via a running sum, writing
// into dst.
func boxBlurV(src, dst []float32, w, h, r int) {
	inv := 1.0 / float64(2*r+1)
	for x := 0; x < w; x++ {
		acc := 0.0
		for k := -r; k <= r; k++ {
			acc += float64(src[clampInt(k, 0, h-1)*w+x])
		}
		for y := 0; y < h; y++ {
			dst[y*w+x] = float32(acc * inv)
			acc += float64(src[clampInt(y+r+1, 0, h-1)*w+x]) - float64(src[clampInt(y-r, 0, h-1)*w+x])
		}
	}
}
