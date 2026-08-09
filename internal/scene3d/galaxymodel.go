// The Milky Way's structure, as published numbers.
//
// This file is the canonical home of the model: the frontend holds a three-scalar mirror for framing
// and nothing else. Everything here is in GALACTOCENTRIC coordinates and kiloparsecs — the origin is
// the galactic centre, x runs toward the Sun, y in the direction of galactic rotation, z toward the
// north galactic pole — and galactocentric azimuth β is measured from the Sun's direction, increasing
// the way the Galaxy turns. That is Reid et al.'s convention, and it is the single easiest sign here
// to get backwards.
//
// The honesty rule the rest of the package already follows applies with full force: STRUCTURE IS A
// MODEL. The arm loci are fitted to a few hundred masers over a limited range of azimuth; the disc is
// a smooth exponential; the bulge outline is a measured axis ratio with an assumed fall-off inside
// it. The user's own stars carry measured distances. These do not, and the UI says so.
package scene3d

import "math"

const degToRad = math.Pi / 180

// --- scalars -------------------------------------------------------------------------------------

// RSunKpc is the Sun's distance from the galactic centre.
//
// 8.15 rather than the more precise 8.178 ± 0.026 from GRAVITY 2019 (A&A 625, L10), because 8.15 is
// what Reid et al. 2019 (ApJ 885, 131) assumed when fitting the arm loci below. The difference is
// 28 pc — a fifth of the narrowest arm's width — so internal consistency with the arms beats absolute
// accuracy by a wide margin. Mixing the two would push every arm off by that much.
const RSunKpc = 8.15

// ZSunPc is the Sun's height above the galactic plane (Bennett & Bovy 2019, MNRAS 482, 1417).
const ZSunPc = 20.8

// The stellar disc, from Bland-Hawthorn & Gerhard 2016 (ARA&A 54, 529): an exponential in radius and
// an isothermal sheet in height, in two components. The thin disc's scale height is quoted as
// 220–450 pc and its scale length as 2.6 ± 0.5 kpc; the thick disc is shorter and much puffier, and
// holds a few per cent of the local stellar mass.
const (
	thinScaleLengthKpc  = 2.6
	thinScaleHeightKpc  = 0.30
	thickScaleLengthKpc = 2.0
	thickScaleHeightKpc = 0.90
)

// DiscEdgeKpc is where the drawn stellar disc ends, with a cosine fade over the last DiscFadeKpc so
// the rim is not a cut circle. The stellar break is near 13–15 kpc; the gas disc runs considerably
// further, which is worth saying in the legend rather than implying the Galaxy simply stops.
const (
	DiscEdgeKpc = 15
	DiscFadeKpc = 2
)

// The star-forming layer is far thinner than the stellar disc — the molecular gas the arms are traced
// by has a scale height of order 70 pc — so the arms are drawn as a genuinely thin sheet inside a
// thick one. That contrast is most of what makes an edge-on view read as a real disc.
const armScaleHeightKpc = 0.09

// Boxy/peanut bulge semi-axes (Wegg & Gerhard 2013, MNRAS 435, 1874) and the long bar's (Wegg,
// Gerhard & Portail 2015, MNRAS 450, 4050). These bound the drawn shape; the fall-off inside them is
// a model, not a fit.
var (
	bulgeSemiKpc = [3]float64{2.2, 1.4, 1.2}
	barSemiKpc   = [3]float64{5.0, 0.9, 0.18}
)

// barAngleDeg is the angle of the bar to the Sun–centre line, near end at positive galactic
// longitude, so β_bar = +27° (Bland-Hawthorn & Gerhard 2016 give 27° ± 2° for the boxy bulge).
const barAngleDeg = 27

// Boxiness of the two bar components: the exponent of the super-ellipsoid whose surface bounds them.
// 2 would be a plain ellipsoid; the bulge is observed to be boxy/peanut-shaped and the long bar
// flatter still. The values are chosen to render that outline, not measured.
const (
	bulgeBoxiness = 2.6
	barBoxiness   = 3.2
)

// The stellar halo is a broken power law (Bland-Hawthorn & Gerhard 2016): ρ ∝ r^−2.5 inside the break
// near 25 kpc and ρ ∝ r^−3.8 outside it. It carries about a per cent of the stellar mass, and it is
// drawn because it is what "and beyond" actually contains.
const (
	haloInnerIndex = 2.5
	haloOuterIndex = 3.8
	haloBreakKpc   = 25
	haloMinKpc     = 3
	haloMaxKpc     = 30
)

// sunCarveOutPc is a hole in the model around the Sun.
//
// It is an honesty measure, not a rendering trick. Inside this radius the scene already holds the
// run's OWN stars, each at a distance that was measured or estimated from the photograph; dropping
// invented stars in among them would mix a model into a measurement at exactly the scale where the
// user is looking closely. At galaxy scale the hole is four ten-thousandths of the disc radius and
// cannot be seen.
const sunCarveOutPc = 250

// --- spiral arms ---------------------------------------------------------------------------------

// arm is one log-spiral, from Reid et al. 2019 Table 2.
//
// The locus is ln(R / R_kink) = −(β − β_kink)·tan ψ, with a different pitch angle either side of the
// kink. widthKpc is the fitted Gaussian arm width — real, and much narrower than most pictures of the
// Galaxy suggest.
type arm struct {
	key      string
	rKinkKpc float64
	betaKink float64
	psiLow   float64
	psiHigh  float64
	widthKpc float64
	betaMin  float64 // the azimuth range Reid et al. actually MEASURED this arm over
	betaMax  float64
	// The azimuth the arm is DRAWN over: the measured range continued each way until the locus leaves
	// the disc. Filled once at init — finding it walks the locus a couple of hundred times, and the
	// sampler asks for it once per point.
	sweepLo, sweepHi float64
}

// The four major arms plus the Local Spur (where the Sun is) and the Outer arm.
//
// The 3-kpc arm is deliberately absent: it is a bar-driven ring, and drawing it as a spiral would
// misrepresent what it is.
//
// Sampling outside betaMin…betaMax is extrapolation, and for the low-pitch arms it is ruinous:
// Norma's inner pitch is −1°, so swept through a full turn its "spiral" closes into a circle and the
// map fills with concentric rings instead of arms. The sampler stays inside these bounds plus a
// tapered margin.
var arms = []arm{
	{
		key: "norma", betaMin: 5, betaMax: 54,
		rKinkKpc: 4.46, betaKink: 18, psiLow: -1.0, psiHigh: 19.5, widthKpc: 0.14,
	},
	{
		key: "scutum", betaMin: 0, betaMax: 104,
		rKinkKpc: 4.91, betaKink: 23, psiLow: 14.1, psiHigh: 12.1, widthKpc: 0.23,
	},
	{
		key: "sagittarius", betaMin: 2, betaMax: 97,
		rKinkKpc: 6.04, betaKink: 24, psiLow: 17.1, psiHigh: 1.0, widthKpc: 0.27,
	},
	{
		key: "local", betaMin: -8, betaMax: 34,
		rKinkKpc: 8.26, betaKink: 9, psiLow: 11.4, psiHigh: 11.4, widthKpc: 0.31,
	},
	{
		key: "perseus", betaMin: -23, betaMax: 115,
		rKinkKpc: 8.87, betaKink: 40, psiLow: 10.3, psiHigh: 8.7, widthKpc: 0.35,
	},
	{
		key: "outer", betaMin: -16, betaMax: 71,
		rKinkKpc: 12.24, betaKink: 18, psiLow: 3.0, psiHigh: 9.4, widthKpc: 0.65,
	},
}

// Beyond the measured range each arm is CONTINUED as a log spiral, because that is what an arm is and
// a Galaxy drawn with arms over only a third of its azimuth is a worse likeness than one drawn with
// them all the way round. The continuation is explicitly weaker than the fit:
//
//   - armExtSweepDeg bounds how far it may run — it stops of its own accord when the locus leaves the
//     disc, and this is only a backstop against an arm whose pitch is so shallow it never does.
//   - armExtRampDeg is the azimuth over which the weight falls from the fit's to the continuation's, so
//     nothing steps.
//   - armExtWeight is how much of the arm's brightness the continued part carries. Roughly half: the
//     measured portion has to read as the brighter one.
const (
	armExtSweepDeg = 260
	armExtRampDeg  = 20
	armExtWeight   = 0.55
)

// armExtPitch is the pitch angle the continuation uses: the STEEPER of the arm's two fitted pitches.
//
// Not the nearer one, and this is the whole reason the old code refused to extrapolate at all. Norma's
// inner pitch is −1°, so continued with that its "spiral" closes into a circle and the map fills with
// concentric rings instead of arms. The steeper pitch is the one that actually spirals, and every arm
// has one: the shallowest here is the Outer arm's 9.4°.
func armExtPitch(a arm) float64 {
	if math.Abs(a.psiLow) > math.Abs(a.psiHigh) {
		return a.psiLow
	}
	return a.psiHigh
}

// armSegment is one stretch of azimuth over which the pitch angle is constant.
type armSegment struct {
	lo, hi, psi float64
}

// armSegments is the arm's pitch as a function of azimuth: the two fitted values over the range they
// were fitted in, and the continuation's outside it.
func armSegments(a arm) [4]armSegment {
	ext := armExtPitch(a)
	return [4]armSegment{
		{math.Inf(-1), a.betaMin, ext},
		{a.betaMin, a.betaKink, a.psiLow},
		{a.betaKink, a.betaMax, a.psiHigh},
		{a.betaMax, math.Inf(1), ext},
	}
}

// armLocus is the galactocentric radius of an arm's ridge at azimuth β (degrees).
//
// A log spiral is d(ln R)/dβ = −tan ψ, so with ψ piecewise constant the radius is the exponential of a
// piecewise integral from the kink — which is continuous across every breakpoint by construction. Over
// the fitted range this is exactly Reid et al.'s ln(R/R_kink) = −(β − β_kink)·tan ψ.
func armLocus(a arm, betaDeg float64) float64 {
	segs := armSegments(a)
	integral, sign := 0.0, 1.0
	lo, hi := a.betaKink, betaDeg
	if hi < lo {
		lo, hi, sign = hi, lo, -1
	}
	for _, s := range segs {
		from, to := math.Max(lo, s.lo), math.Min(hi, s.hi)
		if to > from {
			integral += sign * (to - from) * degToRad * math.Tan(s.psi*degToRad)
		}
	}
	return a.rKinkKpc * math.Exp(-integral)
}

// armWeight is the share of an arm's brightness carried at azimuth β: all of it over the range Reid et
// al. measured, and armExtWeight beyond, with a ramp between so nothing steps.
func armWeight(a arm, betaDeg float64) float64 {
	d := 0.0
	switch {
	case betaDeg < a.betaMin:
		d = a.betaMin - betaDeg
	case betaDeg > a.betaMax:
		d = betaDeg - a.betaMax
	default:
		return 1
	}
	return armExtWeight + (1-armExtWeight)*cosineFade(d/armExtRampDeg)
}

func init() {
	for i := range arms {
		arms[i].sweepLo, arms[i].sweepHi = computeArmSweep(arms[i])
	}
}

// armSweep is the azimuth range an arm is drawn over.
func armSweep(a arm) (lo, hi float64) {
	return a.sweepLo, a.sweepHi
}

// computeArmSweep continues the measured range each way until the locus leaves the drawn disc.
//
// It stops at the FIRST azimuth that falls outside rather than the last one inside, so the drawn arm is
// one unbroken stretch — an arm that dipped out of the disc and back in would otherwise be sampled on
// both sides of the gap.
func computeArmSweep(a arm) (lo, hi float64) {
	inside := func(beta float64) bool {
		r := armLocus(a, beta)
		return r >= armInnerCutKpc && r <= DiscEdgeKpc
	}
	lo, hi = a.betaMin, a.betaMax
	for d := 1.0; d <= armExtSweepDeg && inside(lo-1); d++ {
		lo--
	}
	for d := 1.0; d <= armExtSweepDeg && inside(hi+1); d++ {
		hi++
	}
	return lo, hi
}

// armInnerCutKpc is where the arms stop: inside it the bar rules and the fitted loci have nothing to
// say.
const armInnerCutKpc = 2.5

// cosineFade takes 1 → 0 over t ∈ [0, 1] with zero slope at both ends.
func cosineFade(t float64) float64 {
	switch {
	case t <= 0:
		return 1
	case t >= 1:
		return 0
	default:
		return 0.5 * (1 + math.Cos(math.Pi*t))
	}
}

// discEdgeFade takes the disc smoothly to nothing over the last DiscFadeKpc.
func discEdgeFade(rKpc float64) float64 {
	return cosineFade((rKpc - (DiscEdgeKpc - DiscFadeKpc)) / DiscFadeKpc)
}

// --- coordinates ---------------------------------------------------------------------------------

// galactocentricToHeliocentric converts galactocentric cylindrical coordinates to HELIOCENTRIC
// galactic cartesian kiloparsecs — x toward the centre, y toward rotation, z toward the north pole —
// which is the frame the point cloud is stored in.
func galactocentricToHeliocentric(rKpc, betaDeg, zKpc float64) (x, y, z float64) {
	b := betaDeg * degToRad
	return RSunKpc - rKpc*math.Cos(b), rKpc * math.Sin(b), zKpc - ZSunPc/1000
}

// heliocentricToGalactocentric is its inverse, taking heliocentric galactic PARSECS in.
//
// The sign of y is the easiest thing in this file to get wrong, because getting it wrong produces a
// Galaxy that still looks like a Galaxy — with every arm's azimuth mirrored. β increases in the
// direction of galactic ROTATION (Reid et al.'s convention, which the arm table is fitted in), and
// the Sun moves toward l = 90°, i.e. toward +y in heliocentric galactic coordinates. So the axis β is
// measured around must be +y, NOT −y — which means the triple (toward the Sun, rotation, north pole)
// is LEFT-handed. Squaring it up into a right-handed frame, which is the natural instinct, reflects
// every arm. Pinned by the W3 test.
func heliocentricToGalactocentric(xPc, yPc, zPc float64) (rKpc, betaDeg, zKpc float64) {
	x := RSunKpc - xPc/1000
	y := yPc / 1000
	z := (zPc + ZSunPc) / 1000
	return math.Hypot(x, y), math.Atan2(y, x) / degToRad, z
}

// nearestArmOffsetKpc is how far a point in heliocentric galactic parsecs lies from the closest arm
// ridge, and which arm that is. Reported in kiloparsecs, negative inside the ridge.
func nearestArmOffsetKpc(xPc, yPc, zPc float64) (string, float64) {
	r, beta, _ := heliocentricToGalactocentric(xPc, yPc, zPc)
	key, best := "", math.Inf(1)
	for _, a := range arms {
		d := r - armLocus(a, beta)
		if math.Abs(d) < math.Abs(best) {
			key, best = a.key, d
		}
	}
	return key, best
}
