package solar

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// synth_test.go builds a solar disc with known geometry, so every measurement in this package can
// be checked against ground truth instead of against a previous run of itself. It models the four
// things that actually corrupt a real Hα frame: limb darkening (which biases any global threshold),
// Newton's rings from the etalon (concentric about a point that is NOT the disc centre), the
// etalon's sweet-spot gradient (which dims one side of the field), and off-limb prominences.

// sunSpec describes a synthetic frame.
type sunSpec struct {
	w, h     int
	cx, cy   float64
	r        float64
	u1, u2   float64 // limb-darkening coefficients: I(µ)/I(0) = 1 − u1(1−µ) − u2(1−µ)²
	ringAmp  float64 // Newton's-ring modulation depth, as a fraction
	ringPer  float64 // ring period in px² of radius-squared
	ringOffX float64 // ring centre offset from the disc centre
	ringOffY float64
	gradAmp  float64 // sweet-spot gradient depth across the frame
	psfSigma float64 // Gaussian blur applied after the disc is drawn
	proms    int     // number of off-limb prominences
	features int     // on-disc filaments and plage — the detail a sharpness metric must see
	noise    float64 // additive noise amplitude
	sky      float64 // sky background level
	seed     uint64  // seeds the STRUCTURE: prominence and feature layout
	// noiseSeed seeds the noise only. Keeping it separate is what lets a set of frames share one
	// scene and differ only in their noise — which is the whole premise of stacking, and which a
	// single shared seed silently breaks: vary `seed` per frame and every frame gets DIFFERENT
	// filaments, so averaging them destroys the very detail the test is measuring.
	noiseSeed uint64
}

// defaultSun is a plausible full-disc Hα frame.
func defaultSun() sunSpec {
	return sunSpec{
		w: 1400, h: 1200, cx: 703.4, cy: 597.7, r: 430.5,
		u1: 0.45, u2: 0.15, psfSigma: 1.5, sky: 0.02, noise: 0.004,
		ringPer: 45000, ringOffX: 180, ringOffY: -120, seed: 7,
	}
}

// drawSun renders the spec. The disc level is 1.0 at centre before limb darkening.
func drawSun(s sunSpec) *fits.Image {
	im := fits.NewImage(s.w, s.h, 1)
	p := im.Pix[0]
	rng := newLCG(s.seed)
	promAngles, promSize := promLayout(s, rng)
	feats := featureLayout(s, rng)
	nrng := rng
	if s.noiseSeed != 0 {
		nrng = newLCG(s.noiseSeed)
	}

	for y := 0; y < s.h; y++ {
		dy := float64(y) - s.cy
		for x := 0; x < s.w; x++ {
			dx := float64(x) - s.cx
			d := math.Hypot(dx, dy)
			v := s.sky
			if d <= s.r {
				// µ = cos of the heliocentric angle; the profile is near-linear in it.
				mu := math.Sqrt(math.Max(0, 1-(d/s.r)*(d/s.r)))
				v = 1 - s.u1*(1-mu) - s.u2*(1-mu)*(1-mu)
				v *= featureAt(feats, float64(x), float64(y))
			} else if len(promAngles) > 0 {
				v += prominenceAt(dx, dy, d, s, promAngles, promSize)
			}
			p[y*s.w+x] = float32(v)
		}
	}
	applyRings(p, s)
	applyGradient(p, s)
	if s.psfSigma > 0 {
		copy(p, imgops.GaussianBlur(p, s.w, s.h, s.psfSigma))
	}
	if s.noise > 0 {
		for i := range p {
			p[i] += float32(s.noise * (nrng.float() - 0.5))
		}
	}
	return im
}

// promLayout picks prominence positions deterministically.
func promLayout(s sunSpec, rng *lcg) ([]float64, float64) {
	if s.proms <= 0 {
		return nil, 0
	}
	a := make([]float64, s.proms)
	for i := range a {
		a[i] = rng.float() * 2 * math.Pi
	}
	return a, 0.10 * s.r
}

// prominenceAt returns the off-limb prominence contribution at a point, at a couple of percent of
// disc brightness — the real contrast ratio, which is why they need their own stretch later.
func prominenceAt(dx, dy, d float64, s sunSpec, angles []float64, size float64) float64 {
	if d > s.r*1.2 {
		return 0
	}
	ang := math.Atan2(dy, dx)
	var sum float64
	for _, a := range angles {
		da := math.Abs(math.Atan2(math.Sin(ang-a), math.Cos(ang-a))) * s.r
		dr := d - s.r
		sum += 0.025 * math.Exp(-(da*da+dr*dr)/(2*size*size))
	}
	return sum
}

// applyRings multiplies in Newton's rings, concentric about a point offset from the disc centre —
// they come from the etalon, which has no idea where the Sun is.
func applyRings(p []float32, s sunSpec) {
	if s.ringAmp <= 0 || s.ringPer <= 0 {
		return
	}
	rx, ry := s.cx+s.ringOffX, s.cy+s.ringOffY
	for y := 0; y < s.h; y++ {
		dy := float64(y) - ry
		for x := 0; x < s.w; x++ {
			dx := float64(x) - rx
			p[y*s.w+x] *= float32(1 + s.ringAmp*math.Cos(2*math.Pi*(dx*dx+dy*dy)/s.ringPer))
		}
	}
}

// applyGradient multiplies in a linear sweet-spot falloff across the frame.
func applyGradient(p []float32, s sunSpec) {
	if s.gradAmp <= 0 {
		return
	}
	for y := 0; y < s.h; y++ {
		for x := 0; x < s.w; x++ {
			f := 1 - s.gradAmp*(float64(x)/float64(s.w-1))
			p[y*s.w+x] *= float32(f)
		}
	}
}

// lcg is a tiny deterministic generator, so fixtures never depend on the global RNG.
type lcg struct{ state uint64 }

func newLCG(seed uint64) *lcg { return &lcg{state: seed*6364136223846793005 + 1442695040888963407} }

func (l *lcg) float() float64 {
	l.state = l.state*6364136223846793005 + 1442695040888963407
	return float64(l.state>>11) / float64(1<<53)
}

// discFeature is one on-disc structure: a dark filament or a bright plage patch.
type discFeature struct {
	x, y, sx, sy, ang, amp float64
}

// featureLayout scatters filaments and plage over the disc.
//
// Without these the fixture is a smooth limb-darkened disc with nothing on it, and a "detail" metric
// measured against it is really measuring noise — which is exactly the mistake this fixture exists
// to catch. Filaments are drawn as thin dark ellipses, plage as brighter round patches, both before
// the PSF blur so that softening the frame genuinely destroys them.
func featureLayout(s sunSpec, rng *lcg) []discFeature {
	if s.features <= 0 {
		return nil
	}
	out := make([]discFeature, 0, s.features)
	for i := 0; i < s.features; i++ {
		a := rng.float() * 2 * math.Pi
		rr := 0.15 + 0.65*rng.float()
		f := discFeature{
			x:   s.cx + rr*s.r*math.Cos(a),
			y:   s.cy + rr*s.r*math.Sin(a),
			ang: rng.float() * math.Pi,
		}
		if i%2 == 0 { // a filament: long, thin, dark
			f.sx, f.sy, f.amp = 0.10*s.r, 0.008*s.r, -0.45
		} else { // plage: round, bright
			f.sx, f.sy, f.amp = 0.035*s.r, 0.030*s.r, 0.55
		}
		out = append(out, f)
	}
	return out
}

// featureAt returns the multiplicative brightness factor of the features at a point.
func featureAt(feats []discFeature, x, y float64) float64 {
	v := 1.0
	for _, f := range feats {
		dx, dy := x-f.x, y-f.y
		cos, sin := math.Cos(f.ang), math.Sin(f.ang)
		u, w := dx*cos+dy*sin, -dx*sin+dy*cos
		v += f.amp * math.Exp(-(u*u/(2*f.sx*f.sx) + w*w/(2*f.sy*f.sy)))
	}
	return v
}
