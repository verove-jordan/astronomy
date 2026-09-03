package solar

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// quality.go ranks frames by how much REAL detail they carry.
//
// This is the single most important measurement in lucky imaging, and the obvious way to do it is
// wrong. A plain gradient or Laplacian energy — mean |∇²I| over the disc — is dominated by noise,
// not by detail: a noisy frame scores higher than a clean sharp one, so "keep the sharpest 30%"
// silently becomes "keep the noisiest 30%", and the stack ends up built from the worst frames
// available. The symptom is a stack that looks flatter than its own inputs, which is exactly
// backwards from the point of stacking.
//
// So the metric here subtracts the noise contribution explicitly. It measures energy in a
// band-pass — the spatial scales where solar detail lives — and removes the part of that energy
// which white noise alone would have produced, leaving detail above the noise floor.

const (
	// bandInner/bandOuter are the box radii whose difference forms the band-pass. The 3×3 minus 5×5
	// difference isolates roughly 2-5 px structure, which is where filaments, plage edges and
	// spicules sit at these plate scales.
	bandInner, bandOuter = 1, 2
	// qualityRadius bounds the measurement to the disc interior, clear of the limb — whose step edge
	// would otherwise dominate every frame's score equally and wash out the ranking.
	qualityRadius = 0.85
)

// FrameSharpness is the noise-corrected band-pass detail of a disc, normalised by its own
// brightness so frames at different exposures stay comparable. Higher is sharper; a frame with no
// detail above its own noise floor scores zero rather than scoring its noise.
func FrameSharpness(im *fits.Image, l Limb) float64 {
	return FrameSharpnessPair(im, Pair{Sun: l})
}

// FrameSharpnessPair is FrameSharpness with an occulter accounted for.
//
// THE OCCULTER'S EDGE IS DELIBERATELY LEFT IN, which is the opposite of what this function was first
// written to do, and the measurement is why. Masking the Moon out looks obviously right — most of
// what lies inside 0.85 R is then a flat, signal-free disc — but the Moon's limb is an opaque body
// against the Sun, which makes it a true knife edge: no limb darkening, no chromospheric skirt, no
// prominences, nothing but the system's own blur. It is the cleanest focus probe anywhere in the
// frame. Measured on fixtures at sigma 1.0 against 2.2, keeping it separates the two frames by a
// factor of 2.1 at every obscuration, while masking it drops that to 1.35 and, once the crescent
// thins past the point where it carries any detail of its own, to 0.92 — an inversion, ranking the
// blurred frame first.
//
// What IS masked is the brightness the score is normalised by. That has to come from the visible Sun
// alone, or it falls as the occultation deepens and every frame's score inflates for a reason that
// has nothing to do with focus — which matters because selection ranks a whole clip at once, and on
// the seventeen-minute clip that spans maximum it would quietly prefer the deepest frames.
func FrameSharpnessPair(im *fits.Image, g Pair) float64 {
	l := g.Sun
	if l.R <= 0 || im.W < 8 || im.H < 8 {
		return 0
	}
	inMask := Pair{Sun: l}.visibleSunAt(qualityRadius)
	levelMask := g.visibleSunAt(qualityRadius)
	p := im.Pix[0]
	inner := imgops.GaussianBlur(p, im.W, im.H, float64(bandInner))
	outer := imgops.GaussianBlur(p, im.W, im.H, float64(bandOuter))

	// The noise floor is measured in the SAME band-pass, on the sky outside the limb, where there is
	// no signal to confuse it with. Measuring it the same way rather than deriving a coefficient for
	// how much noise the two kernels pass means it needs no constant that could be wrong for a
	// different sensor or a differently sampled disc.
	//
	// It is NOT a complete correction, and the gap matters when comparing images of different noise
	// levels. It assumes the noise on the disc matches the noise on the sky. On a raw sensor that is
	// nearly true. Through a video codec it is wildly false: a codec spends its bits where there is
	// signal, so its quantisation error concentrates on the bright textured disc and all but vanishes
	// on the flat dark sky. Measured on real iPhone HEVC frames the on-disc noise was 265x the sky
	// estimate, and roughly two thirds of a single frame's apparent detail was codec noise sitting in
	// this very band.
	//
	// So this ranks frames of LIKE noise correctly — which is what selection needs, since frames from
	// one clip share a codec — but it systematically flatters a noisy image against a clean one. Do
	// not read "the stack scores below its sharpest frame" as lost detail without first checking the
	// noise; a stack has averaged its codec noise away and the single frame has not.
	floor := bandEnergy(inner, outer, im.W, im.H, g, 1.15, 1.45)

	// Running sums, not collected slices. This runs on EVERY frame of a clip that can hold thirty
	// thousand of them, so an allocation the size of the disc per frame is not a detail.
	var sum float64
	var n int
	var level []float32
	for y := 0; y < im.H; y++ {
		for x := 0; x < im.W; x++ {
			if !inMask(x, y) {
				continue
			}
			i := y*im.W + x
			d := float64(inner[i] - outer[i])
			sum += d * d
			n++
			if levelMask(x, y) {
				level = append(level, p[i])
			}
		}
	}
	if n == 0 || len(level) == 0 {
		return 0
	}
	med := imgops.Percentile(imgops.Subsample(level, 100000), 50)
	if med <= 1e-9 {
		return 0
	}
	// Subtract the noise floor measured in the same band. It is a FLOOR, not an exact figure: the
	// disc is brighter than the sky, so where noise is photon-limited it carries slightly more than
	// the sky does and a little survives the subtraction. That under-correction is harmless — the
	// ranking only has to be monotone in detail, and it is far better than the over-correction that
	// a wrong analytic coefficient produces, which zeroes every frame equally.
	signal := sum/float64(n) - floor
	if signal <= 0 {
		return 0
	}
	return math.Sqrt(signal) / med
}

// bandEnergy is the mean squared band-pass response over an annulus, as a fraction of the radius.
//
// The occulter is excluded from the annulus as well as from the disc. Near maximum the Moon overhangs
// the solar limb by most of its own radius, so a sky annulus taken on geometry alone is partly Moon —
// and the Moon is darker and smoother than sky, which would push the measured noise floor DOWN and
// flatter every frame's detail by the same amount.
func bandEnergy(inner, outer []float32, w, h int, g Pair, lo, hi float64) float64 {
	l := g.Sun
	var sum float64
	var n int
	occluded := func(int, int) bool { return false }
	if g.Eclipsed() {
		guard := math.Max(pairMaskGuardPx, pairMaskGuardFrac*l.R)
		m2 := (g.Moon.R + guard) * (g.Moon.R + guard)
		occluded = func(x, y int) bool {
			mx, my := float64(x)-g.Moon.CX, float64(y)-g.Moon.CY
			return mx*mx+my*my <= m2
		}
	}
	lo2, hi2 := (lo*l.R)*(lo*l.R), (hi*l.R)*(hi*l.R)
	for y := 0; y < h; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < w; x++ {
			dx := float64(x) - l.CX
			d2 := dx*dx + dy*dy
			if d2 < lo2 || d2 > hi2 || occluded(x, y) {
				continue
			}
			i := y*w + x
			d := float64(inner[i] - outer[i])
			sum += d * d
			n++
		}
	}
	if n < 256 {
		return 0 // no sky in frame to calibrate against; better to under-correct than to invent one
	}
	return sum / float64(n)
}
