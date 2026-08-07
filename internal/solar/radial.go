package solar

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/imgops"
)

// radial.go models and removes limb darkening — the step that turns a bright disc with a dark rim
// into a flat field where filaments and plage read all the way to the edge.

const (
	// radialBins is how many concentric annuli the profile is sampled in. At a 1200 px radius that
	// is a bin every ~5 px, each holding thousands of pixels.
	radialBins = 256
	// profileSmoothBins is the width of the 1-D smoothing applied to the binned profile.
	profileSmoothBins = 5
	// ldFitLimit is the radius, as a fraction of R, beyond which the profile is no longer trusted:
	// the outermost annuli are part limb, part sky, and part PSF.
	ldFitLimit = 0.985
	// ldFreezeStart is where the correction stops growing and blends to a constant.
	ldFreezeStart = 0.97
	// ldMaxGain caps the correction. Without it the fit's tail divides noise by an ever-smaller
	// number and paints a bright ring exactly where the eye looks first.
	ldMaxGain = 3.0
)

// RadialProfile is the azimuthally-averaged brightness of a disc as a function of radius.
type RadialProfile struct {
	R    float64   // the radius the bins are scaled to
	Bins []float64 // median brightness per annulus, index 0 at the centre
	Peak float64   // brightness at the centre
}

// MeasureRadialProfile bins the disc into annuli and takes the MEDIAN of each.
//
// The median, not the mean, is what makes this a model of limb darkening alone. Plage runs 50–100%
// bright and filaments 40% dark, but both are a minority of any given annulus, so a median steps
// over them where a mean would fold them into the profile and then subtract them from the image.
// Prominences never enter at all, living outside the disc.
func MeasureRadialProfile(p []float32, w, h int, l Limb) RadialProfile {
	if l.R <= 0 {
		return RadialProfile{}
	}
	buckets := make([][]float32, radialBins)
	scale := float64(radialBins) / (ldFitLimit * l.R)
	for y := 0; y < h; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < w; x++ {
			dx := float64(x) - l.CX
			b := int(math.Hypot(dx, dy) * scale)
			if b >= 0 && b < radialBins {
				buckets[b] = append(buckets[b], p[y*w+x])
			}
		}
	}
	prof := RadialProfile{R: l.R, Bins: make([]float64, radialBins)}
	last := 0.0
	for i, vals := range buckets {
		if len(vals) == 0 {
			prof.Bins[i] = last // an empty annulus (a partial disc) carries its neighbour forward
			continue
		}
		prof.Bins[i] = imgops.Percentile(imgops.Subsample(vals, 50000), 50)
		last = prof.Bins[i]
	}
	smoothProfile(prof.Bins)
	prof.Peak = prof.Bins[0]
	return prof
}

// Gain returns the multiplicative correction that flattens radius r, at the given strength.
//
// Beyond ldFreezeStart the correction stops growing and is held at its value there. That freeze is
// what prevents the two failure modes of naive limb-darkening removal: a bright ring at the limb,
// and off-limb sky noise amplified by an unbounded gain. Prominences, all of which sit outside the
// disc, receive one constant multiplier rather than a radius-dependent one.
func (rp RadialProfile) Gain(r, strength float64) float64 {
	if rp.R <= 0 || rp.Peak <= 0 || strength <= 0 {
		return 1
	}
	frac := r / rp.R
	if frac > ldFreezeStart {
		g := rp.scaledGain(ldFreezeStart, strength)
		if frac >= ldFitLimit {
			return g
		}
		// Blend from the live correction to the frozen one across the shoulder.
		t := smoothstep((frac - ldFreezeStart) / (ldFitLimit - ldFreezeStart))
		return rp.scaledGain(frac, strength)*(1-t) + g*t
	}
	return rp.scaledGain(frac, strength)
}

// scaledGain is the correction at a radius fraction, with the strength exponent applied.
//
// Every branch of Gain must go through here. Applying the exponent on only one side of the freeze
// leaves a STEP in the gain at that radius — at the default strength of 0.85 the jump from raw^0.85
// to raw^1 is about ten percent — and a ten percent step in a multiplicative correction draws a
// bright ring just inside the limb with an apparent dark rim beside it. It reads exactly like
// deconvolution ringing, which is the wrong thing to go and fix.
func (rp RadialProfile) scaledGain(frac, strength float64) float64 {
	return math.Pow(rp.rawGain(frac), strength)
}

// rawGain is the uncapped, unblended correction at a radius fraction.
//
// The bin index must be the INVERSE of the one MeasureRadialProfile binned with — it scales radius by
// radialBins/(ldFitLimit·R), so recovering the bin from a radius fraction divides by ldFitLimit. It
// used to multiply. The factor is only ldFitLimit² = 0.970, which is why it survived: every gain was
// simply read three percent too far in, so the disc still came out broadly flat and nothing looked
// obviously wrong. What it left was a systematic under-correction that grows with radius, because the
// profile falls fastest there — a residual dark rim in the last few percent of the disc, on every
// solar image this package has ever produced. The top seven bins were never read at all.
func (rp RadialProfile) rawGain(frac float64) float64 {
	b := frac / ldFitLimit * float64(radialBins)
	i := clampInt(int(b), 0, radialBins-1)
	v := rp.Bins[i]
	if i+1 < radialBins {
		v += (b - float64(i)) * (rp.Bins[i+1] - rp.Bins[i])
	}
	if v <= 1e-9 {
		return ldMaxGain
	}
	return math.Min(rp.Peak/v, ldMaxGain)
}

// FlattenLimbDarkening divides the limb-darkening profile out of a plane, in place.
func FlattenLimbDarkening(p []float32, w, h int, l Limb, strength float64) {
	if strength <= 0 || l.R <= 0 {
		return
	}
	prof := MeasureRadialProfile(p, w, h, l)
	if prof.Peak <= 0 {
		return
	}
	// One gain per radius bin, interpolated per pixel: a table lookup instead of a fit evaluation
	// for every pixel of a multi-megapixel frame.
	lut := make([]float64, radialBins+2)
	for i := range lut {
		lut[i] = prof.Gain(float64(i)/float64(radialBins)*l.R, strength)
	}
	scale := float64(radialBins) / l.R
	for y := 0; y < h; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < w; x++ {
			dx := float64(x) - l.CX
			t := math.Hypot(dx, dy) * scale
			i := clampInt(int(t), 0, radialBins)
			g := lut[i] + (t-float64(i))*(lut[i+1]-lut[i])
			p[y*w+x] *= float32(g)
		}
	}
}

// smoothstep is the standard 3t²−2t³ ease, clamped.
func smoothstep(t float64) float64 {
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	return t * t * (3 - 2*t)
}
