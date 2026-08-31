package meteor

// linebuf.go is the machinery the line opening stands on: walking an image along one direction, and
// taking a running extremum along that walk in constant time per pixel.
//
// The image is partitioned, for a given direction, into walks that each step one pixel at a time
// along it. Every pixel belongs to EXACTLY ONE walk, which is what makes the whole-image erosion a
// single pass rather than a per-pixel gather: the walk through a pixel is the structuring element
// centred on it, so a 1-D operator run along the walk erodes the whole image at once.
//
// The extremum itself uses van Herk / Gil-Werman: two passes over the array, one accumulating
// forwards inside blocks of the element's length and one backwards, after which any window of that
// length is the extremum of one value from each. Three comparisons per pixel however long the element
// is. Without it the detector's cost would grow with the shortest streak it can find, which is
// exactly backwards.

import "math"

type lineBuf struct {
	idx  []int32
	vals []float32
	res  []float32
	pad  []float32
	pref []float32
	suff []float32
}

func newLineBuf(w, h int) *lineBuf {
	n := w
	if h > n {
		n = h
	}
	return &lineBuf{
		idx:  make([]int32, n),
		vals: make([]float32, n),
		res:  make([]float32, n),
	}
}

// apply runs a 1-D extremum of length k along every walk in direction theta, writing each pixel's
// result back to its own position in dst. src and dst may not alias.
func (b *lineBuf) apply(src, dst []float32, w, h int, theta float64, k int, useMax bool) {
	b.eachWalk(w, h, theta, func(n int) {
		for j := 0; j < n; j++ {
			b.vals[j] = src[b.idx[j]]
		}
		b.extreme(n, k, useMax)
		for j := 0; j < n; j++ {
			dst[b.idx[j]] = b.res[j]
		}
	})
}

// eachWalk fills idx[:n] with the pixels of each walk in direction theta and calls fn for it. The
// walks partition the image: every pixel is handed to fn exactly once, which is what lets apply treat
// a whole-image erosion as one pass and is pinned by a test.
func (b *lineBuf) eachWalk(w, h int, theta float64, fn func(n int)) {
	xMajor, slope := walkAxis(theta)
	long, cross := w, h // steps along a walk, and the axis walks are indexed on
	if !xMajor {
		long, cross = h, w
	}
	// offset[s] is how far the walk has drifted across after s steps. Computed once so the gather and
	// the scatter cannot disagree about which pixels a walk owns.
	off := make([]int, long)
	lo, hi := 0, 0
	for s := 0; s < long; s++ {
		off[s] = int(math.Round(slope * float64(s)))
		if off[s] < lo {
			lo = off[s]
		}
		if off[s] > hi {
			hi = off[s]
		}
	}
	for a := -hi; a <= cross-1-lo; a++ {
		n := 0
		for s := 0; s < long; s++ {
			c := a + off[s]
			if c < 0 || c >= cross {
				continue
			}
			var i int
			if xMajor {
				i = c*w + s
			} else {
				i = s*w + c
			}
			b.idx[n] = int32(i)
			n++
		}
		if n == 0 {
			continue
		}
		fn(n)
	}
}

// extreme fills res[:n] with the k-long centred extremum of vals[:n]. Edges replicate, so a streak
// touching the border is not punished for it.
func (b *lineBuf) extreme(n, k int, useMax bool) {
	if k < 1 {
		k = 1
	}
	if k%2 == 0 {
		k++
	}
	if k == 1 || n == 1 {
		copy(b.res[:n], b.vals[:n])
		return
	}
	if k > n {
		k = n
		if k%2 == 0 {
			k--
		}
	}
	r := k / 2
	m := n + 2*r
	if cap(b.pad) < m {
		b.pad = make([]float32, m)
		b.pref = make([]float32, m)
		b.suff = make([]float32, m)
	}
	pad, pref, suff := b.pad[:m], b.pref[:m], b.suff[:m]
	for i := 0; i < m; i++ {
		j := i - r
		if j < 0 {
			j = 0
		} else if j >= n {
			j = n - 1
		}
		pad[i] = b.vals[j]
	}
	better := func(x, y float32) bool { return x < y }
	if useMax {
		better = func(x, y float32) bool { return x > y }
	}
	for s := 0; s < m; s += k {
		e := s + k
		if e > m {
			e = m
		}
		pref[s] = pad[s]
		for i := s + 1; i < e; i++ {
			pref[i] = pref[i-1]
			if better(pad[i], pref[i]) {
				pref[i] = pad[i]
			}
		}
		suff[e-1] = pad[e-1]
		for i := e - 2; i >= s; i-- {
			suff[i] = suff[i+1]
			if better(pad[i], suff[i]) {
				suff[i] = pad[i]
			}
		}
	}
	for j := 0; j < n; j++ {
		v := suff[j]
		if p := pref[j+k-1]; better(p, v) {
			v = p
		}
		b.res[j] = v
	}
}

// walkAxis reports whether direction theta is walked along x, and how far it drifts across per step.
// Choosing the axis the direction is most aligned with keeps that drift at or below one pixel, which
// is what makes the walk an 8-connected approximation of the line rather than a dotted one.
func walkAxis(theta float64) (xMajor bool, slope float64) {
	dx, dy := math.Cos(theta), math.Sin(theta)
	if math.Abs(dx) >= math.Abs(dy) {
		return true, dy / dx
	}
	return false, dx / dy
}
