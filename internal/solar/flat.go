package solar

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/imgops"
)

// flat.go removes the instrument's own multiplicative signature: Newton's rings from the etalon,
// dust motes, and the etalon's sweet-spot gradient across the field.
//
// Two things about where this sits in the pipeline are load-bearing.
//
// It runs PER FRAME, before registration. Those artefacts are fixed in the SENSOR while the Sun
// drifts across it, so a flat estimated after stacking would be trying to divide out a pattern that
// has already been smeared into arcs by the very alignment that made the stack.
//
// It runs BEFORE deconvolution. The instrument response multiplies the image after the optics have
// already formed it (y = flat · A·x), so it is not part of what the PSF acted on, and deconvolving
// it would be modelling a scene that never existed.

const (
	// fieldBlurFrac is the smoothing radius of the instrument field, as a fraction of the disc
	// radius. It has to be wide enough to carry no solar detail and narrow enough to follow ring
	// structure; a tenth of the disc sits between the two.
	fieldBlurFrac = 0.10
	// fieldClamp bounds the correction, so a mis-estimated field can dim or brighten a region but
	// never invent one.
	fieldClamp = 0.5
)

// InstrumentField estimates the multiplicative response over a frame, normalised to a median of 1.
//
// The estimate is taken from the residual left after the radial limb-darkening model is divided
// out. Blurring the disc directly would mostly recover limb darkening — it is by far the largest
// smooth structure present — and dividing by that would flatten the disc to a pancake while leaving
// the rings exactly where they were.
func InstrumentField(p []float32, w, h int, l Limb) []float32 {
	if l.R <= 0 {
		return nil
	}
	prof := MeasureRadialProfile(p, w, h, l)
	if prof.Peak <= 0 {
		return nil
	}
	resid := make([]float32, len(p))
	mask := make([]float32, len(p))
	inner := l.R * onDiscRadius
	for y := 0; y < h; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < w; x++ {
			dx := float64(x) - l.CX
			i := y*w + x
			r := math.Hypot(dx, dy)
			if r > inner {
				continue // off-limb pixels carry no instrument signal we can separate from sky
			}
			model := prof.Peak / math.Max(prof.rawGain(r/l.R), 1e-6)
			if model <= 1e-9 {
				continue
			}
			resid[i] = float32(float64(p[i]) / model)
			mask[i] = 1
		}
	}
	// Normalised convolution: blurring the masked residual alone would drag the field down towards
	// zero near the limb, where half the kernel covers pixels that were never measured. Dividing by
	// the identically-blurred mask removes exactly that bias.
	sigma := fieldBlurFrac * l.R
	num := imgops.GaussianBlur(resid, w, h, sigma)
	den := imgops.GaussianBlur(mask, w, h, sigma)
	field := make([]float32, len(p))
	var vals []float32
	for i := range field {
		if den[i] > 1e-3 {
			field[i] = num[i] / den[i]
			if mask[i] > 0 {
				vals = append(vals, field[i])
			}
		}
	}
	norm := imgops.Percentile(imgops.Subsample(vals, 200000), 50)
	if norm <= 1e-9 {
		return nil
	}
	for i := range field {
		if field[i] <= 1e-9 {
			field[i] = 1 // outside the measured region the correction is the identity
			continue
		}
		field[i] = float32(clampF(float64(field[i])/norm, 1-fieldClamp, 1+fieldClamp))
	}
	return field
}

// ApplyInstrumentField divides a frame by its instrument response, in place, at the given strength.
// strength 0 leaves the frame alone; 1 removes the whole estimated field.
func ApplyInstrumentField(p []float32, field []float32, strength float64) {
	if field == nil || strength <= 0 || len(field) != len(p) {
		return
	}
	strength = clampF(strength, 0, 1)
	for i, f := range field {
		g := 1 / float64(f)
		p[i] = float32(float64(p[i]) * (1 + strength*(g-1)))
	}
}

// Deflat estimates and removes the instrument response in one step.
func Deflat(p []float32, w, h int, l Limb, strength float64) {
	if strength <= 0 {
		return
	}
	ApplyInstrumentField(p, InstrumentField(p, w, h, l), strength)
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
