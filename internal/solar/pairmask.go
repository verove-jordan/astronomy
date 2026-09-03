package solar

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// pairmask.go turns the two-body geometry into the masks every later measurement is defined
// against, and carries that geometry from a frame's own pixels onto the canonical stacking raster.
//
// The three regions are not a partition of convenience — each one exists because some measurement
// in this package silently reads the wrong pixels without it:
//
//   - VisibleSun is where there is Sun to measure. The photometric LUT, the limb-darkening profile,
//     the sharpness ranking and the starlet mask all average "the disc", and on a crescent most of
//     the disc is Moon. Averaging it in drags every one of those towards the occulter's dark level.
//   - Occluded is where a frame carries no signal at all. A stack that treats those pixels as data
//     averages Moon with Sun along the edge's path and renders a grey ramp where there should be a
//     step — the most visible defect of the whole exercise.
//   - Sky is neither, and is the only place a background or a noise floor may honestly be read.
//
// The masks are feathered in OPPOSITE directions on purpose. The solar mask erodes inward from the
// limb and the lunar mask dilates outward from it, so the blurred edge itself — a couple of PSF
// widths of half-Sun, half-Moon — belongs to neither VisibleSun nor Sky. Feathering both the same
// way would leave a ring of edge pixels counted as clean disc, which is exactly the population that
// biases a limb-darkening fit.

// radialMask is 1 inside `inner`, 0 outside `outer`, smooth between. inner > outer is allowed and
// simply inverts the sense, which is how the occulter's dilation is expressed.
func radialMask(w, h int, l Limb, inner, outer float64) []float32 {
	m := make([]float32, w*h)
	if l.R <= 0 {
		return m
	}
	span := outer - inner
	for y := 0; y < h; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < w; x++ {
			dx := float64(x) - l.CX
			d := math.Hypot(dx, dy)
			switch {
			case span <= 0:
				if d <= inner {
					m[y*w+x] = 1
				}
			default:
				m[y*w+x] = float32(1 - smoothstep((d-inner)/span))
			}
		}
	}
	return m
}

// maskFeather resolves the transition width a caller left at zero.
//
// It matches the guard the STACK excluded around the occulter, and that agreement is load-bearing
// rather than tidy. The stack drops coverage out to pairMaskGuard past the occulter's edge, so those
// pixels are empty; a mask that feathered over a narrower band than that would call empty canvas
// "visible Sun" and hand it to whatever averaged it. On a thin crescent that is most of what the
// mask selects, and the level measured through it comes back zero.
func (p Pair) maskFeather(feather float64) float64 {
	if feather > 0 {
		return feather
	}
	return math.Max(pairMaskGuardPx, pairMaskGuardFrac*p.Sun.R)
}

// VisibleSunMask is 1 where there is un-occluded Sun to measure and 0 elsewhere.
//
// With no occulter it is DiscMask's answer exactly, so a caller can hand this to code that predates
// the eclipse work without changing what that code sees on an ordinary frame.
func (p Pair) VisibleSunMask(w, h int, feather float64) []float32 {
	f := p.maskFeather(feather)
	m := radialMask(w, h, p.Sun, p.Sun.R-f, p.Sun.R)
	if !p.Eclipsed() {
		return m
	}
	occ := radialMask(w, h, p.Moon, p.Moon.R, p.Moon.R+f)
	for i := range m {
		m[i] *= 1 - occ[i]
	}
	return m
}

// OccludedMask is 1 inside the occulting body, dilated by the feather so the edge's own blur counts
// as occluded rather than as Sun. It is all zero when nothing is occulting.
func (p Pair) OccludedMask(w, h int, feather float64) []float32 {
	if !p.Eclipsed() {
		return make([]float32, w*h)
	}
	f := p.maskFeather(feather)
	return radialMask(w, h, p.Moon, p.Moon.R, p.Moon.R+f)
}

// SkyMask is 1 where neither body is: the only region a background level or a noise floor may be
// read from. It excludes the occulter as well as the Sun, because near maximum the Moon overhangs
// the solar limb and that overhang is not sky.
func (p Pair) SkyMask(w, h int, feather float64) []float32 {
	f := p.maskFeather(feather)
	sun := radialMask(w, h, p.Sun, p.Sun.R, p.Sun.R+f)
	m := make([]float32, w*h)
	for i := range sun {
		m[i] = 1 - sun[i]
	}
	if !p.Eclipsed() {
		return m
	}
	occ := radialMask(w, h, p.Moon, p.Moon.R, p.Moon.R+f)
	for i := range m {
		m[i] *= 1 - occ[i]
	}
	return m
}

// ToCanonical carries a pair measured in a frame's own pixels onto the canonical stacking raster.
//
// The transform warpCovered inverts is a similarity, so both circles map to circles: the centres go
// through the same rotation and scaling the sampler applies, and the radii scale. Doing it
// analytically rather than by warping a rendered mask matters — a resampled mask would carry
// interpolation fuzz into a boundary that the stack then uses as a hard include/exclude decision.
func (p Pair) ToCanonical(t Transform, side int, drizzle float64) Pair {
	out := Pair{Obscuration: p.Obscuration}
	out.Sun = circleToCanonical(p.Sun, t, side, drizzle)
	if p.Eclipsed() {
		out.Moon = circleToCanonical(p.Moon, t, side, drizzle)
	}
	return out
}

// circleToCanonical is the forward similarity — the inverse of the coordinate map warpCovered walks.
func circleToCanonical(l Limb, t Transform, side int, drizzle float64) Limb {
	if drizzle <= 0 {
		drizzle = 1
	}
	s := t.Scale * drizzle
	half := float64(side-1) / 2
	rad := t.RotDeg * math.Pi / 180
	cos, sin := math.Cos(rad), math.Sin(rad)
	dx, dy := l.CX-t.CX, l.CY-t.CY
	return Limb{
		CX: half + s*(dx*cos+dy*sin),
		CY: half + s*(-dx*sin+dy*cos),
		R:  l.R * s,
	}
}

// markOccluded sets every canonical pixel the occulter covers, so a caller can accumulate the whole
// window's sweep into one mask.
//
// The stack applies the result to COVERAGE rather than to the pixels, which is what makes it exact:
// an occluded pixel is not dimmed, not down-weighted and not interpolated over — it simply is not a
// sample, exactly like a pixel that fell outside the frame. Where the whole window is occluded the
// canvas stays empty and the stack says so, which is the honest answer.
func markOccluded(occ []bool, side int, moon Limb, guard float64) {
	if moon.R <= 0 {
		return
	}
	r := moon.R + guard
	r2 := r * r
	y0, y1 := clampInt(int(moon.CY-r), 0, side-1), clampInt(int(moon.CY+r)+1, 0, side-1)
	x0, x1 := clampInt(int(moon.CX-r), 0, side-1), clampInt(int(moon.CX+r)+1, 0, side-1)
	for y := y0; y <= y1; y++ {
		dy := float64(y) - moon.CY
		for x := x0; x <= x1; x++ {
			dx := float64(x) - moon.CX
			if dx*dx+dy*dy <= r2 {
				occ[y*side+x] = true
			}
		}
	}
}

// visibleSunAt returns a per-pixel test for un-occluded Sun inside innerFrac of the solar radius.
//
// It is a PREDICATE rather than a rendered mask because the callers are the per-frame measurements —
// the sharpness ranking, the transparency level, the photometric curve — which run on every frame of
// a thirty-thousand-frame clip. A rendered mask would be a megabyte or two of float32 allocated and
// thrown away per frame; two distance tests cost nothing and allocate nothing.
//
// With no occulter it is exactly the disc test each of those callers already applied, so an ordinary
// solar frame measures identically.
func (p Pair) visibleSunAt(innerFrac float64) func(x, y int) bool {
	if p.Sun.R <= 0 {
		return func(int, int) bool { return true }
	}
	if !p.Eclipsed() {
		r2 := (innerFrac * p.Sun.R) * (innerFrac * p.Sun.R)
		return func(x, y int) bool {
			dx, dy := float64(x)-p.Sun.CX, float64(y)-p.Sun.CY
			return dx*dx+dy*dy <= r2
		}
	}
	// THE INTERIOR FRACTION IS DROPPED WHEN THERE IS AN OCCULTER, and replaced by a fixed inset from
	// each limb. Its callers pass 0.85 or 0.90 to keep the limb's own blur and the steepest limb
	// darkening out of a whole-disc average, which is right for a whole disc and unusable on a
	// crescent: past about seventy percent obscuration the surviving Sun lies ENTIRELY outside 0.85 R,
	// so the bound leaves the mask empty and every measurement built on it quietly returns zero. That
	// is how the transparency gate came to read a deep eclipse as total cloud, and how the sharpness
	// ranking came to score a soft frame above a sharp one.
	//
	// What those callers actually want is to stand clear of the edges, so that is what is measured —
	// a few pixels in from the solar limb and a few pixels out from the occulter's, both scaled for a
	// large disc. On a crescent that is the whole of the available Sun, which is the honest answer.
	sunInset := math.Max(pairLimbInsetPx, pairLimbInsetFrac*p.Sun.R)
	r2 := (p.Sun.R - sunInset) * (p.Sun.R - sunInset)
	guard := math.Max(pairMaskGuardPx, pairMaskGuardFrac*p.Sun.R)
	m2 := (p.Moon.R + guard) * (p.Moon.R + guard)
	return func(x, y int) bool {
		dx, dy := float64(x)-p.Sun.CX, float64(y)-p.Sun.CY
		if dx*dx+dy*dy > r2 {
			return false
		}
		mx, my := float64(x)-p.Moon.CX, float64(y)-p.Moon.CY
		return mx*mx+my*my > m2
	}
}

const (
	// pairMaskGuardPx and pairMaskGuardFrac are the margin kept around the OCCULTER in a per-pixel
	// test, in pixels and as a fraction of the solar radius.
	//
	// They have to clear three sigma of the point spread function, and the number is set by what
	// happens when they do not. The occulter's edge is the largest step in the frame — full disc to
	// sky in a couple of pixels — so a sliver of it inside the measurement region carries more
	// band-pass energy than the entire crescent's worth of real detail. Measured on fixtures drawn at
	// sigma 1.0 and 2.6 with a three-pixel guard: the SOFTER frame scored four times the sharper one,
	// purely because its edge blur reached further past the guard. A sharpness metric that ranks blur
	// above detail is worse than no metric, so the guard is set generously.
	pairMaskGuardPx   = 8.0
	pairMaskGuardFrac = 0.02
	// pairLimbInsetPx and pairLimbInsetFrac are how far inside the SOLAR limb a two-body measurement
	// starts. Comparable to the occulter's guard for the same reason, plus limb darkening is already
	// falling before the limb arrives and the chromosphere carries a skirt past it.
	pairLimbInsetPx   = 6.0
	pairLimbInsetFrac = 0.02
)

// MaskedMedian is the median of a plane over a 0..1 mask, weighting nothing below half.
//
// It is the primitive the masked measurements share. A weighted median over a feathered mask would
// be more elegant and is not worth it: every caller wants "the level of the region", the feather is
// a couple of pixels wide, and a hard cut at the half-way point keeps the answer independent of how
// wide the feather happens to be.
func MaskedMedian(p []float32, mask []float32) float64 {
	if len(mask) != len(p) {
		return medianOfPlane(p)
	}
	vals := make([]float32, 0, len(p)/4+1)
	for i, v := range p {
		if mask[i] >= 0.5 {
			vals = append(vals, v)
		}
	}
	if len(vals) == 0 {
		return 0
	}
	return medianOfPlane(vals)
}

// firstPlaneImage wraps a plane as a single-channel image, the shape every measurement here takes.
func firstPlaneImage(p []float32, w, h int) *fits.Image {
	return &fits.Image{W: w, H: h, C: 1, Pix: [][]float32{p}}
}
