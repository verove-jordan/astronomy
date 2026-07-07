// Package noise implements starlet-transform (à-trous B3-spline wavelet) noise measurement and a
// scale-adaptive, SNR-protected denoiser for the deep-sky / nebula finishing path.
//
// The building block is the à-trous wavelet: a redundant multiscale decomposition whose planes add
// back to the input exactly (Reconstruct is a lossless inverse). Measure derives a robust per-tile
// noise sigma from the first detail plane; Denoise soft-thresholds each detail plane against a
// per-pixel noise estimate while protecting bright, high-SNR structure so stars and nebulae survive.
//
// Everything here is pure Go and self-contained (no Siril, no external deps beyond errgroup). The
// public entry points never panic on degenerate input: Denoise soft-fails and leaves a plane
// unchanged when it cannot safely process it.
package noise

import (
	"runtime"

	"golang.org/x/sync/errgroup"
)

// starletSigma holds, per scale (0-based), the standard deviation of an à-trous B3 starlet detail
// plane computed from a unit-variance white-noise field. Dividing a measured detail-plane sigma by
// starletSigma[j] converts it back to the sigma of the underlying image noise. Regenerated (and
// asserted within 3%) by TestStarlet_SigmaPropagation.
var starletSigma = []float64{0.8907, 0.2007, 0.0855, 0.0412, 0.0205, 0.0107}

const b3Norm = 1.0 / 16.0 // normalization of the [1,4,6,4,1] B3 kernel

// scaleSigma returns the white-noise detail-plane sigma for scale j, extrapolating past the
// tabulated scales (halving roughly each octave) so callers with many scales never panic.
func scaleSigma(j int) float64 {
	if j < 0 {
		j = 0
	}
	if j < len(starletSigma) {
		return starletSigma[j]
	}
	s := starletSigma[len(starletSigma)-1]
	for k := len(starletSigma); k <= j; k++ {
		s *= 0.5
	}
	return s
}

// workers is the concurrency cap for the separable passes: min(NumCPU, 8).
func workers() int {
	n := runtime.NumCPU()
	if n > 8 {
		n = 8
	}
	if n < 1 {
		n = 1
	}
	return n
}

// parallelRows splits [0,h) into up to workers() contiguous bands and runs fn on each concurrently,
// bounded by an errgroup limit. fn owns disjoint output rows so no locking is needed.
func parallelRows(h int, fn func(y0, y1 int)) {
	n := workers()
	if n <= 1 || h <= 1 {
		fn(0, h)
		return
	}
	band := (h + n - 1) / n
	g := new(errgroup.Group)
	g.SetLimit(n)
	for y0 := 0; y0 < h; y0 += band {
		y0, y1 := y0, y0+band
		if y1 > h {
			y1 = h
		}
		g.Go(func() error {
			fn(y0, y1)
			return nil
		})
	}
	_ = g.Wait()
}

// convolveRows applies the à-trous B3 kernel horizontally at the given dilation step (2^j).
func convolveRows(src, dst []float32, w, h, step int) {
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			base := y * w
			for x := 0; x < w; x++ {
				x0 := reflectIndex(x-2*step, w)
				x1 := reflectIndex(x-step, w)
				x3 := reflectIndex(x+step, w)
				x4 := reflectIndex(x+2*step, w)
				dst[base+x] = float32(b3Norm) * (src[base+x0] + 4*src[base+x1] +
					6*src[base+x] + 4*src[base+x3] + src[base+x4])
			}
		}
	})
}

// convolveCols applies the à-trous B3 kernel vertically at the given dilation step (2^j).
func convolveCols(src, dst []float32, w, h, step int) {
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			r0 := reflectIndex(y-2*step, h) * w
			r1 := reflectIndex(y-step, h) * w
			r2 := y * w
			r3 := reflectIndex(y+step, h) * w
			r4 := reflectIndex(y+2*step, h) * w
			for x := 0; x < w; x++ {
				dst[r2+x] = float32(b3Norm) * (src[r0+x] + 4*src[r1+x] +
					6*src[r2+x] + 4*src[r3+x] + src[r4+x])
			}
		}
	})
}

// atrous returns A_j(src): one separable B3 smoothing pass at dilation step (row pass then column).
func atrous(src []float32, w, h, step int) []float32 {
	tmp := make([]float32, len(src))
	out := make([]float32, len(src))
	convolveRows(src, tmp, w, h, step)
	convolveCols(tmp, out, w, h, step)
	return out
}

// Decompose runs the à-trous B3-spline starlet transform to J scales. It returns the coarsest
// smooth cJ and the J detail planes wcoef (wcoef[j] = c_j − c_{j+1}); Reconstruct inverts it exactly.
func Decompose(p []float32, w, h, J int) (cJ []float32, wcoef [][]float32) {
	c := make([]float32, len(p))
	copy(c, p)
	wcoef = make([][]float32, 0, J)
	for j := 0; j < J; j++ {
		cNext := atrous(c, w, h, 1<<j)
		wj := make([]float32, len(c))
		for i := range wj {
			wj[i] = c[i] - cNext[i]
		}
		wcoef = append(wcoef, wj)
		c = cNext
	}
	return c, wcoef
}

// Reconstruct is the exact inverse of Decompose: out = cJ + Σ_j wcoef[j].
func Reconstruct(cJ []float32, wcoef [][]float32) []float32 {
	out := make([]float32, len(cJ))
	copy(out, cJ)
	for _, wj := range wcoef {
		for i := range out {
			out[i] += wj[i]
		}
	}
	return out
}

// reflectIndex maps an out-of-range index into [0,n) using half-sample symmetric reflection
// (…d c b a | a b c d…), i.e. -1 -> 0 and n -> n-1. Kept local so the package stays self-contained.
func reflectIndex(i, n int) int {
	if n <= 1 {
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
