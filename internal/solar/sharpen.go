package solar

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/noise"
)

// sharpen.go is where the detail comes from: a deconvolution against a PSF we actually know, then a
// multi-scale contrast lift.
//
// The two are split by band rather than stacked. Richardson-Lucy is the primary sharpener here
// because the point spread function is genuinely known a priori — a 40 mm aperture at 656 nm is
// diffraction-limited, so the width follows from optics rather than from taste, which is a luxury
// most astrophotography does not have. The starlet pass then does the two things deconvolution does
// not: threshold the noise-dominated finest scale, and lift local contrast at the scales the eye
// reads structure on. Running both at full strength over the same band double-counts and gives the
// embossed, plastic look with dark halos around every filament.

const (
	// rlEpsilon guards the RL ratio against dividing by an all-but-empty pixel.
	rlEpsilon = 1e-4
	// rlDampSigmas is how many noise sigmas a residual must exceed before it drives the iteration.
	// Below that the update is damped towards 1, so RL stops sharpening noise once it has explained
	// the signal — the classic failure of undamped RL is that it never stops.
	rlDampSigmas = 3.0
	// discExtendMin is the shortest blend, in pixels, over which the off-limb sky is replaced by a
	// continuation of the disc before deconvolution. The actual reach scales with the PSF — see
	// discReach.
	discExtendMin = 4.0
	// discExtendSigmas is how many PSF sigmas the blend must span. The deconvolution kernel reaches
	// three sigma, so the extension has to cover at least that or the kernel still sees the limb step
	// it exists to hide.
	discExtendSigmas = 3.5
	// starletScales is the decomposition depth. Beyond five scales the planes describe structure
	// already handled by the flat and the limb-darkening model.
	starletScales = 5
)

// SharpenOptions tunes the two sharpening stages.
type SharpenOptions struct {
	// DeconvSigma is the Gaussian PSF width in pixels. 0 disables deconvolution.
	DeconvSigma float64
	DeconvIters int
	// Gains and Thresholds are per starlet scale, finest first. Thresholds are in units of the
	// measured noise sigma.
	Gains      []float64
	Thresholds []float64
}

// DefaultSharpen returns the tuning for a drizzled Hα stack.
//
// The gains follow from the sampling. At an effective PSF of ~3–4 px on the output raster, the
// finest starlet plane (structure below ~2.4 px) sits under the diffraction limit and holds noise
// and resampling residue rather than signal, so it is suppressed rather than boosted; scales two
// and three carry the real detail; the coarsest are left near unity because they describe the
// large-scale brightness the flat and the radial model already own.
func DefaultSharpen(deconvSigma float64) SharpenOptions {
	return SharpenOptions{
		DeconvSigma: deconvSigma,
		DeconvIters: 12,
		Gains:       []float64{0.8, 1.15, 1.35, 1.25, 1.10},
		Thresholds:  []float64{4.0, 2.0, 1.0, 0, 0},
	}
}

// gaussianKernel builds a normalised 1-D FIR Gaussian.
//
// It exists instead of imgops.GaussianBlur, which approximates a Gaussian with three box passes and
// therefore quantises sigma hard — 0.5 is an exact no-op, 1.0 comes out at 0.816, and 1.3 and 1.4
// both land on 1.414. Deconvolution sigma lives squarely in that range, and an 18% PSF-width error
// maps straight into over- or under-sharpening. Box blur with clamped edges is also not
// self-adjoint at the borders, which quietly breaks RL's flux conservation; a symmetric FIR with
// mirror padding is its own adjoint by construction.
func gaussianKernel(sigma float64) []float32 {
	if sigma <= 0 {
		return []float32{1}
	}
	r := int(math.Ceil(3 * sigma))
	k := make([]float32, 2*r+1)
	var sum float64
	for i := -r; i <= r; i++ {
		v := math.Exp(-float64(i*i) / (2 * sigma * sigma))
		k[i+r] = float32(v)
		sum += v
	}
	for i := range k {
		k[i] = float32(float64(k[i]) / sum)
	}
	return k
}

// blurFIR applies a separable kernel with mirror padding, returning a new plane.
func blurFIR(p []float32, w, h int, k []float32) []float32 {
	r := len(k) / 2
	tmp := make([]float32, len(p))
	out := make([]float32, len(p))
	for y := 0; y < h; y++ {
		row := y * w
		for x := 0; x < w; x++ {
			var s float32
			for i := -r; i <= r; i++ {
				s += k[i+r] * p[row+mirror(x+i, w)]
			}
			tmp[row+x] = s
		}
	}
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			var s float32
			for i := -r; i <= r; i++ {
				s += k[i+r] * tmp[mirror(y+i, h)*w+x]
			}
			out[y*w+x] = s
		}
	}
	return out
}

// mirror reflects an index back inside [0, n).
func mirror(i, n int) int {
	if n <= 1 {
		return 0
	}
	for i < 0 || i >= n {
		if i < 0 {
			i = -i
		}
		if i >= n {
			i = 2*(n-1) - i
		}
	}
	return i
}

// RichardsonLucy deconvolves a plane against a Gaussian PSF, returning a new plane.
//
// The limb is a step of order a hundred to one, and undamped RL rings at it badly — a dark rim
// inside and a bright halo outside, both growing with every iteration. Three things keep that from
// happening: the disc is extended outward so the step is not in the deconvolved data at all, the
// update is damped where the residual is within the noise, and the iteration count is kept modest.
// The true off-limb data, prominences included, is restored afterwards untouched: it is far too
// faint to deconvolve without simply sharpening its noise.
func RichardsonLucy(p []float32, w, h int, l Limb, sigma float64, iters int, noiseSigma float64) []float32 {
	if sigma <= 0 || iters <= 0 {
		return append([]float32(nil), p...)
	}
	k := gaussianKernel(sigma)
	reach := discReach(sigma)
	y := extendDisc(p, w, h, l, reach)
	eps := float32(rlEpsilon * math.Max(medianOfPlane(y), 1e-6))
	damp := float32(rlDampSigmas * math.Max(noiseSigma, 1e-9))

	x := append([]float32(nil), y...)
	ratio := make([]float32, len(y))
	for it := 0; it < iters; it++ {
		c := blurFIR(x, w, h, k)
		for i := range ratio {
			den := c[i]
			if den < eps {
				den = eps
			}
			r := y[i] / den
			// Damped update (Snyder/White): a residual buried in the noise must not keep driving the
			// iteration, or RL spends its later passes sharpening the noise it cannot explain.
			if d := absF32(y[i] - c[i]); d < damp && damp > 0 {
				t := float32(smoothstep(float64(d / damp)))
				r = 1 + (r-1)*t
			}
			ratio[i] = r
		}
		corr := blurFIR(ratio, w, h, k)
		for i := range x {
			x[i] *= corr[i]
			if x[i] < 0 {
				x[i] = 0
			}
		}
	}
	restoreOffLimb(x, p, w, h, l, reach)
	return x
}

// discReach is how far inside the limb the extension blend starts.
//
// It scales with the PSF because the thing it has to hide is the limb step, and how far that step
// reaches into the deconvolution is set by the kernel — three sigma, by construction. Fixed pixels
// were fine while the width was a constant near 1.4; once the width is measured per capture it
// ranges to 4 px and beyond, whose kernel reaches twelve, and an eight-pixel blend leaves the step
// sitting inside it. What that produced was a bright rim around the whole disc: RL ringing on an
// edge it was supposed to have been shielded from.
func discReach(sigma float64) float64 {
	return math.Max(discExtendMin, discExtendSigmas*sigma)
}

// discBlendAt is the weight given to the extended disc at a signed distance from the limb: 0 at
// reach inside it, 1 at the limb and everywhere beyond.
//
// The blend finishes AT the limb rather than straddling it. Straddling was the subtler half of the
// same bug: with the old bounds the weight at the limb was only a quarter, so the profile there was
// three parts real limb — already half-fallen — to one part disc level, and it dipped before rising
// back to the disc level further out. That trough is not something the optics ever produced, and
// deconvolution amplifies it exactly as enthusiastically as it would a real feature.
func discBlendAt(d, reach float64) float32 {
	if reach <= 0 {
		reach = discExtendMin
	}
	return float32(smoothstep((d + reach) / reach))
}

// extendDisc replaces the off-limb sky with a smooth continuation of the disc, so the limb step
// never enters the deconvolution.
func extendDisc(p []float32, w, h int, l Limb, reach float64) []float32 {
	out := append([]float32(nil), p...)
	if l.R <= 0 {
		return out
	}
	prof := MeasureRadialProfile(p, w, h, l)
	if prof.Peak <= 0 {
		return out
	}
	edge := float32(prof.Bins[radialBins-1]) // the disc's own brightness just inside the limb
	for y := 0; y < h; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < w; x++ {
			dx := float64(x) - l.CX
			d := math.Hypot(dx, dy) - l.R
			if d < -reach {
				continue
			}
			i := y*w + x
			t := discBlendAt(d, reach)
			out[i] = out[i]*(1-t) + edge*t
		}
	}
	return out
}

// restoreOffLimb puts the original sky and prominences back after deconvolution, feathered so the
// join does not become an edge of its own.
func restoreOffLimb(dst, src []float32, w, h int, l Limb, reach float64) {
	if l.R <= 0 {
		return
	}
	for y := 0; y < h; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < w; x++ {
			dx := float64(x) - l.CX
			d := math.Hypot(dx, dy) - l.R
			if d < -reach {
				continue
			}
			i := y*w + x
			t := discBlendAt(d, reach)
			dst[i] = dst[i]*(1-t) + src[i]*t
		}
	}
}

// StarletSharpen applies per-scale thresholds and gains, returning a new plane.
//
// Thresholding happens BEFORE the gain, not after: a scale is denoised at its own measured noise
// level and only then amplified, so the gain multiplies signal rather than the noise that survived.
// discMask, when supplied, confines the change to the disc, leaving off-limb sky and prominences at
// their original amplitude rather than boosting sky noise behind them.
func StarletSharpen(p []float32, w, h int, o SharpenOptions, noiseSigma float64, discMask []float32) []float32 {
	if len(o.Gains) == 0 {
		return append([]float32(nil), p...)
	}
	j := len(o.Gains)
	if j > starletScales {
		j = starletScales
	}
	cJ, coeffs := noise.Decompose(p, w, h, j)
	for s := 0; s < len(coeffs) && s < j; s++ {
		gain := o.Gains[s]
		thr := 0.0
		if s < len(o.Thresholds) {
			thr = o.Thresholds[s] * noiseSigma * scaleNoise(s)
		}
		plane := coeffs[s]
		for i := range plane {
			v := float64(plane[i])
			if thr > 0 {
				v = softThreshold(v, thr)
			}
			plane[i] = float32(v * gain)
		}
	}
	out := noise.Reconstruct(cJ, coeffs)
	if discMask != nil && len(discMask) == len(p) {
		for i := range out {
			out[i] = p[i] + (out[i]-p[i])*discMask[i]
		}
	}
	return out
}

// scaleNoise is how white noise attenuates at each à-trous scale, so a threshold stated in sigmas
// means the same thing at every scale.
func scaleNoise(s int) float64 {
	table := []float64{0.890, 0.201, 0.086, 0.041, 0.020, 0.010}
	if s < len(table) {
		return table[s]
	}
	return table[len(table)-1]
}

// softThreshold shrinks a coefficient towards zero, which suppresses noise without the hard edges
// (and the ringing that follows them) that a hard threshold leaves behind.
func softThreshold(v, t float64) float64 {
	switch {
	case v > t:
		return v - t
	case v < -t:
		return v + t
	default:
		return 0
	}
}

// DiscMask builds a feathered 0..1 mask of the disc, used to confine sharpening to it.
func DiscMask(w, h int, l Limb, feather float64) []float32 {
	m := make([]float32, w*h)
	if l.R <= 0 {
		for i := range m {
			m[i] = 1
		}
		return m
	}
	if feather <= 0 {
		feather = 0.01 * l.R
	}
	for y := 0; y < h; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < w; x++ {
			dx := float64(x) - l.CX
			d := math.Hypot(dx, dy)
			m[y*w+x] = float32(1 - smoothstep((d-(l.R-feather))/feather))
		}
	}
	return m
}

func absF32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// medianOfPlane is a cheap median over a subsample.
func medianOfPlane(p []float32) float64 {
	if len(p) == 0 {
		return 0
	}
	step := len(p)/100000 + 1
	vals := make([]float64, 0, len(p)/step+1)
	for i := 0; i < len(p); i += step {
		vals = append(vals, float64(p[i]))
	}
	return median(vals)
}
