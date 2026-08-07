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
	if l.R <= 0 || im.W < 8 || im.H < 8 {
		return 0
	}
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
	floor := bandEnergy(inner, outer, im.W, im.H, l, 1.15, 1.45)

	var sum float64
	var n int
	var level []float32
	r2 := (qualityRadius * l.R) * (qualityRadius * l.R)
	for y := 0; y < im.H; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < im.W; x++ {
			dx := float64(x) - l.CX
			if dx*dx+dy*dy > r2 {
				continue
			}
			i := y*im.W + x
			d := float64(inner[i] - outer[i])
			sum += d * d
			n++
			level = append(level, p[i])
		}
	}
	if n == 0 {
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
func bandEnergy(inner, outer []float32, w, h int, l Limb, lo, hi float64) float64 {
	lo2, hi2 := (lo*l.R)*(lo*l.R), (hi*l.R)*(hi*l.R)
	var sum float64
	var n int
	for y := 0; y < h; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < w; x++ {
			dx := float64(x) - l.CX
			d2 := dx*dx + dy*dy
			if d2 < lo2 || d2 > hi2 {
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
