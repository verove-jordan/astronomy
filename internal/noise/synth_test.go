package noise

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// xorshift32 is a tiny deterministic RNG so tests never depend on the math/rand global state.
type xorshift32 struct{ s uint32 }

func newRNG(seed uint32) *xorshift32 {
	if seed == 0 {
		seed = 0x9e3779b9
	}
	return &xorshift32{s: seed}
}

func (r *xorshift32) next() uint32 {
	x := r.s
	x ^= x << 13
	x ^= x >> 17
	x ^= x << 5
	r.s = x
	return x
}

// unit returns a uniform sample in (0,1).
func (r *xorshift32) unit() float64 { return (float64(r.next()) + 1) / 4294967297.0 }

// gauss returns a standard-normal sample via Box-Muller.
func (r *xorshift32) gauss() float64 {
	return math.Sqrt(-2*math.Log(r.unit())) * math.Cos(2*math.Pi*r.unit())
}

// newPlane allocates a w*h plane filled with a constant.
func newPlane(w, h int, fill float32) []float32 {
	p := make([]float32, w*h)
	for i := range p {
		p[i] = fill
	}
	return p
}

// monoImage wraps a plane as a single-channel fits.Image (sharing the backing slice).
func monoImage(w, h int, p []float32) *fits.Image {
	return &fits.Image{W: w, H: h, C: 1, Pix: [][]float32{p}}
}

// addNoise adds zero-mean Gaussian noise of the given sigma to every pixel.
func addNoise(p []float32, rng *xorshift32, sigma float64) {
	for i := range p {
		p[i] += float32(rng.gauss() * sigma)
	}
}

// addGaussian adds an isotropic Gaussian PSF of peak amplitude amp and width sig at (cx,cy).
func addGaussian(p []float32, w, h, cx, cy int, amp, sig float64) {
	rad := int(math.Ceil(3.5 * sig))
	for dy := -rad; dy <= rad; dy++ {
		yy := cy + dy
		if yy < 0 || yy >= h {
			continue
		}
		for dx := -rad; dx <= rad; dx++ {
			xx := cx + dx
			if xx < 0 || xx >= w {
				continue
			}
			d2 := float64(dx*dx + dy*dy)
			p[yy*w+xx] += float32(amp * math.Exp(-d2/(2*sig*sig)))
		}
	}
}

// addGradient adds a horizontal linear ramp from 0 (left) to amp (right).
func addGradient(p []float32, w, h int, amp float64) {
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			p[y*w+x] += float32(amp * float64(x) / float64(w-1))
		}
	}
}

// scatterStars sprinkles n PSF stars with amplitudes/widths in the given ranges at pseudo-random spots.
func scatterStars(p []float32, w, h, n int, rng *xorshift32, ampLo, ampHi, sigLo, sigHi float64) {
	for i := 0; i < n; i++ {
		cx := int(rng.unit() * float64(w))
		cy := int(rng.unit() * float64(h))
		amp := ampLo + rng.unit()*(ampHi-ampLo)
		sig := sigLo + rng.unit()*(sigHi-sigLo)
		addGaussian(p, w, h, cx, cy, amp, sig)
	}
}

// stdCrop returns the population standard deviation of the interior of a plane, cropping `margin`
// pixels off every edge to avoid wavelet boundary bias.
func stdCrop(p []float32, w, h, margin int) float64 {
	var sum, sumsq float64
	var n int
	for y := margin; y < h-margin; y++ {
		for x := margin; x < w-margin; x++ {
			v := float64(p[y*w+x])
			sum += v
			sumsq += v * v
			n++
		}
	}
	if n == 0 {
		return 0
	}
	mean := sum / float64(n)
	v := sumsq/float64(n) - mean*mean
	if v < 0 {
		v = 0
	}
	return math.Sqrt(v)
}

// medianOf returns the median of a copy of vals (vals is not modified).
func medianOf(vals []float64) float64 { return median64(append([]float64(nil), vals...)) }
