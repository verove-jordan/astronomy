package sim

import (
	"math"
	"math/rand"
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/deepstars"
)

// Rendering one simulated exposure. The point of doing this properly — real catalogue stars, a
// physical blur circle, Poisson-ish noise — is that everything downstream is then exercised for
// real: the frame plate-solves, stars have measurable HFD, and defocus moves that HFD by the amount
// the optics say it should.

// zeroPointADU is the ADU a magnitude-0 star would deposit in one second at gain 1. Chosen so a
// mag-10 star in a 10 s sub lands a few thousand ADU above the sky — a realistic, non-saturated
// working range for the ASI1600 at unity gain.
const zeroPointADU = 2.5e6

// filterThroughput scales flux per filter: luminance passes most of the light, narrowband very
// little. This is what makes an Ha sub in the simulator legitimately need longer exposures.
var filterThroughput = map[string]float64{
	"L": 1.0, "R": 0.30, "G": 0.32, "B": 0.26, "Ha": 0.05, "OIII": 0.05, "SII": 0.04,
}

// renderParams is everything the renderer needs, snapshotted under the world lock.
type renderParams struct {
	raDeg, decDeg float64
	paDeg         float64
	width, height int
	bin           int
	scaleArcsecPx float64
	exposureSec   float64
	gain          int64
	offset        int64
	filter        string
	seeingArcsec  float64
	focusOffsetUm float64
	fRatio        float64
	pixelUm       float64
	skyMagPerAsec float64
	readNoiseADU  float64
	hotPixels     int
	faintPerDeg2  float64
	// flatPanelADUPerSec > 0 puts a flat panel over the aperture instead of the sky.
	flatPanelADUPerSec float64
	epoch              time.Time
	seed               int64
	extra              []SyntheticStar
}

// renderFrame paints one exposure into a fresh buffer.
func renderFrame(p renderParams) []uint16 {
	rng := rand.New(rand.NewSource(p.seed))
	pix := make([]float64, p.width*p.height)

	// A flat panel covers the aperture: no sky, no stars, just even illumination through the optics.
	// Simulating it is what makes the flat-exposure wizard testable, and the vignetting it carries is
	// exactly the signal a real flat exists to record.
	if p.flatPanelADUPerSec > 0 {
		drawFlatPanel(pix, p)
		addNoise(pix, p, rng, meanOf(pix))
		addHotPixels(pix, p, rng)
		return toUint16(pix, p)
	}

	sky := skyLevelADU(p)
	for i := range pix {
		pix[i] = sky
	}
	drawStars(pix, p)
	addNoise(pix, p, rng, sky)
	addHotPixels(pix, p, rng)

	return toUint16(pix, p)
}

// toUint16 applies the offset pedestal and clips into the sensor's 16-bit range.
func toUint16(pix []float64, p renderParams) []uint16 {
	out := make([]uint16, len(pix))
	for i, v := range pix {
		v += float64(p.offset)
		switch {
		case v <= 0:
			out[i] = 0
		case v >= 65535:
			out[i] = 65535
		default:
			out[i] = uint16(v)
		}
	}
	return out
}

// drawFlatPanel fills the frame with even illumination shaped by the optics. The cos⁴ falloff is the
// real vignetting law for an unobstructed refractor, so a simulated flat carries the same gradient a
// real one would — which is what makes it worth dividing by.
func drawFlatPanel(pix []float64, p renderParams) {
	gainFactor := math.Max(float64(p.gain), 1) / 100
	level := p.flatPanelADUPerSec * p.exposureSec * gainFactor

	cx, cy := float64(p.width)/2, float64(p.height)/2
	// The field half-angle at the frame corner, from the plate scale; cos⁴ of it sets the corner
	// brightness relative to the centre.
	cornerRad := math.Hypot(cx, cy) * p.scaleArcsecPx / 206265
	for y := 0; y < p.height; y++ {
		for x := 0; x < p.width; x++ {
			r := math.Hypot(float64(x)-cx, float64(y)-cy) / math.Hypot(cx, cy)
			theta := r * cornerRad
			c := math.Cos(theta)
			pix[y*p.width+x] = level * c * c * c * c
		}
	}
}

// meanOf is the average signal, used as the shot-noise base for a flat.
func meanOf(pix []float64) float64 {
	if len(pix) == 0 {
		return 0
	}
	var sum float64
	for _, v := range pix {
		sum += v
	}
	return sum / float64(len(pix))
}

// psfSigmaPx is the Gaussian width of a star: seeing and the geometric defocus blur added in
// quadrature. The defocus term is the textbook relation — blur diameter = focuser offset ÷ focal
// ratio — divided by the pixel pitch, which is exactly what the focus meter inverts.
func psfSigmaPx(p renderParams) float64 {
	seeingPx := p.seeingArcsec / math.Max(p.scaleArcsecPx, 1e-6)
	defocusPx := 0.0
	if p.fRatio > 0 && p.pixelUm > 0 {
		defocusPx = math.Abs(p.focusOffsetUm) / (p.fRatio * p.pixelUm)
	}
	fwhm := math.Hypot(seeingPx, defocusPx)
	if fwhm < 0.8 {
		fwhm = 0.8 // never below the pixel grid — a real optic is never a delta function
	}
	return fwhm / 2.3548
}

// drawStars projects the catalogue onto the sensor and stamps a Gaussian for each star.
func drawStars(pix []float64, p renderParams) {
	// Field radius to the sensor corner, plus a margin so stars just outside still bleed in.
	halfDiagDeg := math.Hypot(float64(p.width), float64(p.height)) / 2 * p.scaleArcsecPx / 3600
	stars := deepstars.InField(p.raDeg, p.decDeg, halfDiagDeg*1.15, 0, p.epoch)
	// The synthetic population is what makes a rendered frame solvable; without it a field holds a
	// handful of stars and Siril gives up at its six-star minimum.
	stars = append(stars, faintStars(p.raDeg, p.decDeg, halfDiagDeg*1.15, p.faintPerDeg2)...)
	for _, s := range p.extra {
		stars = append(stars, deepstars.Star{RADeg: s.RADeg, DecDeg: s.DecDeg, Mag: s.Mag})
	}

	sigma := psfSigmaPx(p)
	radius := int(math.Ceil(3.5 * sigma))
	if radius > 256 {
		radius = 256 // a wildly defocused star is a disc; cap the work per star
	}
	throughput := filterThroughput[p.filter]
	if throughput == 0 {
		throughput = 1
	}
	gainFactor := math.Max(float64(p.gain), 1) / 100
	sinPA, cosPA := math.Sincos(p.paDeg * math.Pi / 180)
	norm := 1 / (2 * math.Pi * sigma * sigma)

	for _, st := range stars {
		xi, eta, ok := astro.TangentPlane(p.raDeg, p.decDeg, st.RADeg, st.DecDeg)
		if !ok {
			continue
		}
		// Inverse of the planner's frame rotation, then to pixels: east is left, north is up.
		u := xi*cosPA - eta*sinPA
		v := xi*sinPA + eta*cosPA
		cx := float64(p.width)/2 - u*3600/p.scaleArcsecPx
		cy := float64(p.height)/2 - v*3600/p.scaleArcsecPx
		if cx < -float64(radius) || cy < -float64(radius) ||
			cx > float64(p.width+radius) || cy > float64(p.height+radius) {
			continue
		}
		flux := zeroPointADU * math.Pow(10, -0.4*st.Mag) * p.exposureSec * throughput * gainFactor
		if flux < 1 {
			continue
		}
		stamp(pix, p.width, p.height, cx, cy, sigma, radius, flux*norm)
	}
}

// stamp adds one Gaussian PSF, clipped to the sensor.
func stamp(pix []float64, w, h int, cx, cy, sigma float64, radius int, peak float64) {
	x0, x1 := int(cx)-radius, int(cx)+radius
	y0, y1 := int(cy)-radius, int(cy)+radius
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 >= w {
		x1 = w - 1
	}
	if y1 >= h {
		y1 = h - 1
	}
	inv2s2 := 1 / (2 * sigma * sigma)
	for y := y0; y <= y1; y++ {
		dy := float64(y) - cy
		row := y * w
		for x := x0; x <= x1; x++ {
			dx := float64(x) - cx
			pix[row+x] += peak * math.Exp(-(dx*dx+dy*dy)*inv2s2)
		}
	}
}

// skyLevelADU is the background from sky brightness — magnitudes per square arcsecond over the solid
// angle one pixel subtends.
func skyLevelADU(p renderParams) float64 {
	arcsec2 := p.scaleArcsecPx * p.scaleArcsecPx
	gainFactor := math.Max(float64(p.gain), 1) / 100
	return zeroPointADU * math.Pow(10, -0.4*p.skyMagPerAsec) * arcsec2 * p.exposureSec * gainFactor
}

// addNoise adds shot noise on the accumulated signal plus a read-noise floor. The shot term uses a
// Gaussian approximation (√signal), which is indistinguishable from Poisson at these levels and far
// cheaper over 16 megapixels.
func addNoise(pix []float64, p renderParams, rng *rand.Rand, sky float64) {
	_ = sky
	for i, v := range pix {
		shot := 0.0
		if v > 0 {
			shot = rng.NormFloat64() * math.Sqrt(v)
		}
		pix[i] = v + shot + rng.NormFloat64()*p.readNoiseADU
	}
}

// addHotPixels sprinkles the sensor's fixed defects. They sit at fixed positions (seeded by the
// world, not the frame) so dithering genuinely decorrelates them — which is what the pipeline's
// rejection is supposed to exploit.
func addHotPixels(pix []float64, p renderParams, _ *rand.Rand) {
	if p.hotPixels <= 0 {
		return
	}
	fixed := rand.New(rand.NewSource(p.seed ^ 0x5eed))
	for i := 0; i < p.hotPixels; i++ {
		x := fixed.Intn(p.width)
		y := fixed.Intn(p.height)
		pix[y*p.width+x] += 20000 + fixed.Float64()*40000
	}
}
