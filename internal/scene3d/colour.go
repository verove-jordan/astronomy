package scene3d

import "math"

// A star's colour, computed rather than sampled.
//
// The obvious source is the pixel the star was detected on, and that is what the 2D overlay uses —
// but annotate's starHex deliberately lifts every colour toward white (floor 0.45) so a marker ring
// stays legible on a dark frame. Feeding that into a 3D scene gives a field of pastel dots. Worse,
// the sampled colour carries the stack's own colour balance, the stretch and any residual gradient.
//
// So the scene derives colour from physics instead: the star's B−V colour index gives an effective
// temperature, and a blackbody at that temperature has exactly one colour. It is the same quantity
// the hover card already shows, so the dot and the number can never disagree.

// bvToTemperatureK is Ballesteros' formula (2012), the same one frontend/src/utils/starInfo.ts uses.
// Kept in step with it deliberately: a star's rendered colour and the temperature written beside it
// must come from one relation, not two that drift.
//
// T = 4600·[1/(0.92·BV + 1.7) + 1/(0.92·BV + 0.62)]. The guard is on the INPUT, not on the resulting
// temperature: the fit was made over roughly −0.4…2.0 in B−V, and outside that it degrades smoothly
// into numbers that still look like plausible temperatures (B−V 5 returns 1611 K — no star, but
// nothing about 1611 K announces that). Bounding what goes in is the honest test.
const (
	bvFitMin = -0.4
	bvFitMax = 2.0
)

func bvToTemperatureK(bv float64) (float64, bool) {
	if bv < bvFitMin || bv > bvFitMax {
		return 0, false
	}
	a := 0.92 * bv
	if a+0.62 == 0 || a+1.7 == 0 {
		return 0, false
	}
	t := 4600 * (1/(a+1.7) + 1/(a+0.62))
	if math.IsNaN(t) || t <= 1000 || t >= 60000 {
		return 0, false
	}
	return t, true
}

// Planck constants in SI, for the spectral radiance below.
const (
	planckH = 6.62607015e-34 // J·s
	lightC  = 2.99792458e8   // m/s
	boltzK  = 1.380649e-23   // J/K
)

// planck is spectral radiance per unit wavelength at wavelength λ (metres) and temperature T.
// The absolute scale is irrelevant here — the result is normalised to a hue — so no constants are
// carried beyond what the shape needs.
func planck(lambda, t float64) float64 {
	x := planckH * lightC / (lambda * boltzK * t)
	if x > 700 { // exp would overflow; the radiance there is zero to any precision that matters
		return 0
	}
	return 1 / (math.Pow(lambda, 5) * (math.Exp(x) - 1))
}

// gaussLobe is the piecewise Gaussian used by the colour-matching fits: one width below the peak and
// another above it, which is what lets two lobes reproduce a curve as asymmetric as x̄.
func gaussLobe(x, mu, sigmaLo, sigmaHi float64) float64 {
	s := sigmaLo
	if x >= mu {
		s = sigmaHi
	}
	d := (x - mu) / s
	return math.Exp(-0.5 * d * d)
}

// cieXYZ returns the CIE 1931 colour-matching values at wavelength λ (nanometres), using the
// multi-lobe Gaussian fits of Wyman, Sloan & Shirley (2013). Accurate to about a percent across the
// visible band — far inside the error of the B−V the temperature came from — and it needs no
// tabulated data, so there is nothing to ship or keep in step.
func cieXYZ(nm float64) (x, y, z float64) {
	x = 1.056*gaussLobe(nm, 599.8, 37.9, 31.0) +
		0.362*gaussLobe(nm, 442.0, 16.0, 26.7) -
		0.065*gaussLobe(nm, 501.1, 20.4, 26.2)
	y = 0.821*gaussLobe(nm, 568.8, 46.9, 40.5) +
		0.286*gaussLobe(nm, 530.9, 16.3, 31.1)
	z = 1.217*gaussLobe(nm, 437.0, 11.8, 36.0) +
		0.681*gaussLobe(nm, 459.0, 26.0, 13.8)
	return x, y, z
}

// Visible band the integration runs over, in nanometres.
const (
	lambdaMinNm  = 380
	lambdaMaxNm  = 780
	lambdaStepNm = 5
)

// blackbodyRGB is the sRGB colour of a blackbody at temperature T: integrate Planck's spectrum
// against the colour-matching functions, convert CIE XYZ to linear sRGB, clip the out-of-gamut
// negatives, normalise to full brightness and gamma-encode.
//
// Normalising to the brightest channel is the point — this returns a HUE. How bright the star is
// drawn comes from its magnitude and its distance from the camera, and folding luminosity into the
// colour as well would double-count it.
func blackbodyRGB(t float64) (uint8, uint8, uint8) {
	var X, Y, Z float64
	for nm := float64(lambdaMinNm); nm <= lambdaMaxNm; nm += lambdaStepNm {
		p := planck(nm*1e-9, t)
		x, y, z := cieXYZ(nm)
		X += p * x
		Y += p * y
		Z += p * z
	}
	if X+Y+Z <= 0 {
		return 255, 255, 255
	}

	// CIE XYZ → linear sRGB (D65 primaries).
	r := 3.2406*X - 1.5372*Y - 0.4986*Z
	g := -0.9689*X + 1.8758*Y + 0.0415*Z
	b := 0.0557*X - 0.2040*Y + 1.0570*Z

	// A blackbody's chromaticity can fall outside the sRGB gamut at the extremes; clipping the
	// negative channel is the standard concession and only affects the deepest reds and blues.
	r, g, b = math.Max(0, r), math.Max(0, g), math.Max(0, b)
	max := math.Max(r, math.Max(g, b))
	if max <= 0 {
		return 255, 255, 255
	}

	enc := func(v float64) uint8 {
		v /= max
		// sRGB transfer function.
		if v <= 0.0031308 {
			v *= 12.92
		} else {
			v = 1.055*math.Pow(v, 1/2.4) - 0.055
		}
		return uint8(math.Round(math.Min(1, math.Max(0, v)) * 255))
	}
	return enc(r), enc(g), enc(b)
}

// starColour picks the best colour available for one star, best first:
//
//  1. the catalogue's own B−V — a real measurement of this star;
//  2. the B−V this frame's fitted colour relation implies for it — an estimate, but one calibrated
//     against the catalogued stars in this very image;
//  3. the sampled pixel colour, white-lift and all, as a last resort;
//  4. white.
//
// Returning the tier alongside the colour lets the caller record how much of the field is physical.
func starColour(hex string, catalogueCI *float64, fit colourFit) (r, g, b uint8, physical bool) {
	if catalogueCI != nil {
		if t, ok := bvToTemperatureK(*catalogueCI); ok {
			r, g, b = blackbodyRGB(t)
			return r, g, b, true
		}
	}
	if ci, ok := fit.colourIndex(hex); ok {
		if t, ok := bvToTemperatureK(ci); ok {
			r, g, b = blackbodyRGB(t)
			return r, g, b, true
		}
	}
	if hr, hg, hb, ok := parseHex(hex); ok {
		return uint8(hr * 255), uint8(hg * 255), uint8(hb * 255), false
	}
	return 255, 255, 255, false
}
