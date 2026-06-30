package astro

import (
	"math"
	"time"
)

// Planet identifies a major planet for geocentric position and brightness computation.
type Planet int

// The seven naked-eye-relevant planets (Earth is the observer). Order is heliocentric.
const (
	Mercury Planet = iota
	Venus
	Mars
	Jupiter
	Saturn
	Uranus
	Neptune
)

// Planets is the iteration order used by the events engine.
var Planets = []Planet{Mercury, Venus, Mars, Jupiter, Saturn, Uranus, Neptune}

// String returns the lowercase canonical key (matches the frontend i18n `bodies.*` keys).
func (p Planet) String() string {
	switch p {
	case Mercury:
		return "mercury"
	case Venus:
		return "venus"
	case Mars:
		return "mars"
	case Jupiter:
		return "jupiter"
	case Saturn:
		return "saturn"
	case Uranus:
		return "uranus"
	case Neptune:
		return "neptune"
	}
	return "planet"
}

// PlanetState is a planet's geocentric apparent position and brightness at an instant.
type PlanetState struct {
	RADeg, DecDeg float64
	HelioDistAU   float64 // r: distance from the Sun
	GeoDistAU     float64 // Δ: distance from Earth
	ElongationDeg float64 // angular distance from the Sun (geocentric), 0..180
	PhaseAngleDeg float64 // Sun–planet–Earth angle
	Magnitude     float64 // apparent visual magnitude
}

// schlyterEpoch is Schlyter's day-number epoch: JD of 1999-12-31 00:00 UT (= J2000 − 1.5).
const schlyterEpoch = 2451543.5

// orbit holds the (osculating) orbital elements at a day-number d. Angles are degrees, a is AU.
type orbit struct{ N, i, w, a, e, M float64 }

// PlanetPosition returns the geocentric apparent equatorial position, distances and magnitude of a
// planet at t, using Paul Schlyter's compact perturbed two-body theory (≈1–2′ — naked-eye-event grade,
// ample for conjunction/opposition/elongation detection). Pure stdlib, no ephemeris data files.
func PlanetPosition(p Planet, t time.Time) PlanetState {
	d := JulianDate(t) - schlyterEpoch
	ecl := 23.4393 - 3.563e-7*d

	xh, yh, zh, r := helioRect(planetOrbit(p, d), p, d)
	xs, ys, rs := sunRect(d)
	xg, yg, zg := xh+xs, yh+ys, zh // geocentric ecliptic rectangular (AU)

	ra, dec, delta := eclRectToEqua(xg, yg, zg, ecl)

	// Elongation (Sun–Earth–planet) and phase angle (Sun–planet–Earth) from the triangle Sun/Earth/planet.
	elong := acosd(clamp((rs*rs + delta*delta - r*r) / (2 * rs * delta)))
	phase := acosd(clamp((r*r + delta*delta - rs*rs) / (2 * r * delta)))

	return PlanetState{
		RADeg: ra, DecDeg: dec,
		HelioDistAU: r, GeoDistAU: delta,
		ElongationDeg: elong, PhaseAngleDeg: phase,
		Magnitude: planetMagnitude(p, r, delta, phase),
	}
}

// SunEclipticRect returns the Sun's geocentric ecliptic rectangular coordinates (AU) and the Earth–Sun
// distance at t. Exposed so other two-body bodies (comets) can be placed geocentrically the same way.
func SunEclipticRect(t time.Time) (x, y, r float64) {
	return sunRect(JulianDate(t) - schlyterEpoch)
}

// EclRectToEqua rotates geocentric ecliptic rectangular coordinates (AU) at t into apparent equatorial
// RA/Dec (degrees) and returns the geocentric distance (AU). Shared by the planet and comet paths.
func EclRectToEqua(xg, yg, zg float64, t time.Time) (raDeg, decDeg, distAU float64) {
	ecl := 23.4393 - 3.563e-7*(JulianDate(t)-schlyterEpoch)
	return eclRectToEqua(xg, yg, zg, ecl)
}

func eclRectToEqua(xg, yg, zg, ecl float64) (raDeg, decDeg, distAU float64) {
	xe := xg
	ye := yg*cosD(ecl) - zg*sinD(ecl)
	ze := yg*sinD(ecl) + zg*cosD(ecl)
	raDeg = norm360(atan2d(ye, xe))
	decDeg = atan2d(ze, math.Hypot(xe, ye))
	distAU = math.Sqrt(xg*xg + yg*yg + zg*zg)
	return
}

// sunRect returns the Sun's geocentric ecliptic rectangular coordinates (AU) and Earth–Sun distance.
func sunRect(d float64) (x, y, r float64) {
	w := 282.9404 + 4.70935e-5*d
	e := 0.016709 - 1.151e-9*d
	M := 356.0470 + 0.9856002585*d
	E := solveKepler(M, e)
	xv := cosD(E) - e
	yv := math.Sqrt(1-e*e) * sinD(E)
	v := atan2d(yv, xv)
	r = math.Hypot(xv, yv)
	lon := v + w
	return r * cosD(lon), r * sinD(lon), r
}

// helioRect returns a planet's heliocentric ecliptic rectangular coordinates (AU) and Sun distance,
// applying Schlyter's largest perturbation terms for Jupiter, Saturn and Uranus.
func helioRect(o orbit, p Planet, d float64) (x, y, z, r float64) {
	E := solveKepler(o.M, o.e)
	xv := o.a * (cosD(E) - o.e)
	yv := o.a * math.Sqrt(1-o.e*o.e) * sinD(E)
	v := atan2d(yv, xv)
	r = math.Hypot(xv, yv)

	// 3-D heliocentric ecliptic position from the orbital elements.
	xh := r * (cosD(o.N)*cosD(v+o.w) - sinD(o.N)*sinD(v+o.w)*cosD(o.i))
	yh := r * (sinD(o.N)*cosD(v+o.w) + cosD(o.N)*sinD(v+o.w)*cosD(o.i))
	zh := r * (sinD(v+o.w) * sinD(o.i))
	lonecl := norm360(atan2d(yh, xh))
	latecl := atan2d(zh, math.Hypot(xh, yh))

	// Major mutual perturbations (Schlyter): meaningful for the outer planets (Saturn ≈ 0.5° otherwise).
	if p == Jupiter || p == Saturn || p == Uranus {
		mj := 19.8950 + 0.0830853001*d
		ms := 316.9670 + 0.0334442282*d
		mu := 142.5905 + 0.011725806*d
		switch p {
		case Jupiter:
			lonecl += -0.332*sinD(2*mj-5*ms-67.6) - 0.056*sinD(2*mj-2*ms+21) +
				0.042*sinD(3*mj-5*ms+21) - 0.036*sinD(mj-2*ms) + 0.022*cosD(mj-ms) +
				0.023*sinD(2*mj-3*ms+52) - 0.016*sinD(mj-5*ms-69)
		case Saturn:
			lonecl += 0.812*sinD(2*mj-5*ms-67.6) - 0.229*cosD(2*mj-4*ms-2) +
				0.119*sinD(mj-2*ms-3) + 0.046*sinD(2*mj-6*ms-69) + 0.014*sinD(mj-3*ms+32)
			latecl += -0.020*cosD(2*mj-4*ms-2) + 0.018*sinD(2*mj-6*ms-49)
		case Uranus:
			lonecl += 0.040*sinD(ms-2*mu+6) + 0.035*sinD(ms-3*mu+33) - 0.015*sinD(mj-mu+20)
		}
	}

	x = r * cosD(lonecl) * cosD(latecl)
	y = r * sinD(lonecl) * cosD(latecl)
	z = r * sinD(latecl)
	return
}

// planetOrbit returns a planet's orbital elements at day-number d (Schlyter's mean elements).
func planetOrbit(p Planet, d float64) orbit {
	switch p {
	case Mercury:
		return orbit{48.3313 + 3.24587e-5*d, 7.0047 + 5.00e-8*d, 29.1241 + 1.01444e-5*d, 0.387098, 0.205635 + 5.59e-10*d, 168.6562 + 4.0923344368*d}
	case Venus:
		return orbit{76.6799 + 2.46590e-5*d, 3.3946 + 2.75e-8*d, 54.8910 + 1.38374e-5*d, 0.723330, 0.006773 - 1.302e-9*d, 48.0052 + 1.6021302244*d}
	case Mars:
		return orbit{49.5574 + 2.11081e-5*d, 1.8497 - 1.78e-8*d, 286.5016 + 2.92961e-5*d, 1.523688, 0.093405 + 2.516e-9*d, 18.6021 + 0.5240207766*d}
	case Jupiter:
		return orbit{100.4542 + 2.76854e-5*d, 1.3030 - 1.557e-7*d, 273.8777 + 1.64505e-5*d, 5.20256, 0.048498 + 4.469e-9*d, 19.8950 + 0.0830853001*d}
	case Saturn:
		return orbit{113.6634 + 2.38980e-5*d, 2.4886 - 1.081e-7*d, 339.3939 + 2.97661e-5*d, 9.55475, 0.055546 - 9.499e-9*d, 316.9670 + 0.0334442282*d}
	case Uranus:
		return orbit{74.0005 + 1.3978e-5*d, 0.7733 + 1.9e-8*d, 96.6612 + 3.0565e-5*d, 19.18171 - 1.55e-8*d, 0.047318 + 7.45e-9*d, 142.5905 + 0.011725806*d}
	case Neptune:
		return orbit{131.7806 + 3.0173e-5*d, 1.7700 - 2.55e-7*d, 272.8461 - 6.027e-6*d, 30.05826 + 3.313e-8*d, 0.008606 + 2.15e-9*d, 260.2471 + 0.005995147*d}
	}
	return orbit{}
}

// planetMagnitude returns the apparent visual magnitude (Schlyter's per-planet fits). r is heliocentric
// distance, geo is geocentric distance (AU), fv is the phase angle (degrees). Saturn's ring tilt is
// ignored (a small, slowly-varying term — acceptable for event scoring).
func planetMagnitude(p Planet, r, geo, fv float64) float64 {
	d5 := 5 * math.Log10(r*geo)
	switch p {
	case Mercury:
		return -0.36 + d5 + 0.027*fv + 2.2e-13*math.Pow(fv, 6)
	case Venus:
		return -4.34 + d5 + 0.013*fv + 4.2e-7*fv*fv*fv
	case Mars:
		return -1.51 + d5 + 0.016*fv
	case Jupiter:
		return -9.25 + d5 + 0.014*fv
	case Saturn:
		return -9.0 + d5 + 0.044*fv
	case Uranus:
		return -7.15 + d5 + 0.001*fv
	case Neptune:
		return -6.90 + d5 + 0.001*fv
	}
	return 0
}

// solveKepler solves Kepler's equation for the eccentric anomaly (degrees) given the mean anomaly
// (degrees) and eccentricity, by Newton iteration. Suited to the planets' low eccentricities.
func solveKepler(Mdeg, e float64) float64 {
	m := norm360(Mdeg)
	const r2d = 180 / math.Pi
	eAnom := m + r2d*e*sinD(m)*(1+e*cosD(m))
	for i := 0; i < 12; i++ {
		dE := (eAnom - r2d*e*sinD(eAnom) - m) / (1 - e*cosD(eAnom))
		eAnom -= dE
		if math.Abs(dE) < 1e-10 {
			break
		}
	}
	return eAnom
}

func atan2d(y, x float64) float64 { return math.Atan2(y, x) * 180 / math.Pi }
func acosd(x float64) float64     { return math.Acos(x) * 180 / math.Pi }

func clamp(x float64) float64 {
	if x > 1 {
		return 1
	}
	if x < -1 {
		return -1
	}
	return x
}
