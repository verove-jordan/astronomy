// Package nightscape ports the proven Milky-Way nightscape recipe (star-aligned sky stack + single
// clean foreground, masked composite, linear colour grade) into Go. It drives Siril for registration
// and does all per-pixel maths here, so the pipeline stays native Go (no Python). The numeric helpers
// in this file are the NumPy/SciPy equivalents the recipe relies on.
package nightscape

import (
	"math"
	"sort"
)

// percentile returns the linear-interpolated p-th percentile (0..100) of vals, matching numpy's
// default. vals is not modified.
func percentile(vals []float32, p float64) float64 {
	n := len(vals)
	if n == 0 {
		return 0
	}
	buf := make([]float64, n)
	for i, v := range vals {
		buf[i] = float64(v)
	}
	sort.Float64s(buf)
	if p <= 0 {
		return buf[0]
	}
	if p >= 100 {
		return buf[n-1]
	}
	rank := p / 100 * float64(n-1)
	lo := int(math.Floor(rank))
	frac := rank - float64(lo)
	if lo+1 >= n {
		return buf[n-1]
	}
	return buf[lo] + frac*(buf[lo+1]-buf[lo])
}

// reflectIndex maps an out-of-range index to its 'reflect' (half-sample symmetric) position, the
// SciPy default boundary mode: (... d c b a | a b c d | d c b a ...).
func reflectIndex(i, n int) int {
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

// gaussianBlur approximates a Gaussian filter (≈ scipy.ndimage.gaussian_filter) using three box
// passes — O(N) regardless of sigma, which matters because the light-pollution gradient uses sigmas
// of a few hundred pixels. Boundaries clamp to the edge. sigma<=0 returns a copy.
func gaussianBlur(src []float32, w, h int, sigma float64) []float32 {
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

// boxesForGauss returns n odd box widths whose successive application approximates a Gaussian of the
// given sigma (Kovesi / Kutskir's fast-Gaussian construction).
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

// boxBlurH applies a horizontal moving-average of radius r (edge-clamped) via a running sum.
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

// boxBlurV applies a vertical moving-average of radius r (edge-clamped) via a running sum.
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

// medianFilter applies a size×size median filter (reflect boundary), like scipy.ndimage.median_filter.
func medianFilter(src []float32, w, h, size int) []float32 {
	out := make([]float32, len(src))
	rad := size / 2
	window := make([]float32, 0, size*size)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			window = window[:0]
			for dy := -rad; dy <= rad; dy++ {
				yy := reflectIndex(y+dy, h)
				for dx := -rad; dx <= rad; dx++ {
					xx := reflectIndex(x+dx, w)
					window = append(window, src[yy*w+xx])
				}
			}
			sort.Slice(window, func(i, j int) bool { return window[i] < window[j] })
			out[y*w+x] = window[len(window)/2]
		}
	}
	return out
}

// binaryDilation grows the true region by `iterations` steps of 4-connectivity, matching
// scipy.ndimage.binary_dilation with its default cross structuring element.
func binaryDilation(mask []bool, w, h, iterations int) []bool {
	cur := make([]bool, len(mask))
	copy(cur, mask)
	for it := 0; it < iterations; it++ {
		next := make([]bool, len(cur))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				i := y*w + x
				if cur[i] {
					next[i] = true
					continue
				}
				if x > 0 && cur[i-1] || x < w-1 && cur[i+1] ||
					y > 0 && cur[i-w] || y < h-1 && cur[i+w] {
					next[i] = true
				}
			}
		}
		cur = next
	}
	return cur
}

// label assigns a connected-component id (1..n) to each true pixel with 4-connectivity (0 for
// background), like scipy.ndimage.label. It returns the label grid and the component count.
func label(mask []bool, w, h int) (labels []int, n int) {
	labels = make([]int, len(mask))
	parent := []int{0} // union-find; index 0 is background sentinel
	find := func(a int) int {
		for parent[a] != a {
			parent[a] = parent[parent[a]]
			a = parent[a]
		}
		return a
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}
	// First pass: provisional labels + equivalences (check left and up neighbours).
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if !mask[i] {
				continue
			}
			left, up := 0, 0
			if x > 0 {
				left = labels[i-1]
			}
			if y > 0 {
				up = labels[i-w]
			}
			switch {
			case left == 0 && up == 0:
				id := len(parent)
				parent = append(parent, id)
				labels[i] = id
			case left != 0 && up != 0:
				labels[i] = left
				union(left, up)
			case left != 0:
				labels[i] = left
			default:
				labels[i] = up
			}
		}
	}
	// Second pass: flatten to root and renumber roots to 1..n.
	remap := make(map[int]int)
	for i, l := range labels {
		if l == 0 {
			continue
		}
		root := find(l)
		id, ok := remap[root]
		if !ok {
			n++
			id = n
			remap[root] = id
		}
		labels[i] = id
	}
	return labels, n
}
