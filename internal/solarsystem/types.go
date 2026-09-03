// Package solarsystem describes the solar system as a scene: what each world physically is, where it
// is at an instant, which way its axis points and how far round its rotation has turned.
//
// It is deliberately split from internal/astro. astro answers "where in MY sky is this, right now",
// which is what the planner needs; this package answers "where is everything, in one fixed frame,
// with real radii and real axes", which is what a 3-D map needs — and it hands the browser the
// elements themselves so the animation can run at 60 fps without a round trip per frame.
//
// One propagator serves every orbit here (planets, moons, dwarf planets, comets); they differ only in
// where their elements come from and which plane those elements are referred to.
package solarsystem

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/astro"
)

// Kind groups bodies for the UI and decides how one is drawn.
type Kind string

// The kinds of body the scene holds.
const (
	KindStar   Kind = "star"
	KindPlanet Kind = "planet"
	KindMoon   Kind = "moon"
	KindDwarf  Kind = "dwarf"
	KindComet  Kind = "comet"
)

// Tier is the honesty ladder the 3-D field map already uses, applied here to how a body's POSITION
// is obtained. Every body's physical facts — radius, mass, pole direction — are published
// measurements regardless of tier; it is the motion that varies in pedigree.
type Tier string

// The honesty tiers, best first.
const (
	// TierFitted is a theory fitted to the numerically-integrated ephemeris: arcminute-class over the
	// span it was fitted for. The planets, and the Moon.
	TierFitted Tier = "fitted"
	// TierMean is a mean-element set: the right period, the right plane and a phase good to a fraction
	// of a degree over decades, but not an ephemeris. The moons and the dwarf planets.
	TierMean Tier = "mean"
	// TierSampled is a synthetic population drawn from published statistics — the belt clouds. No
	// particle in it is a real object.
	TierSampled Tier = "sampled"
)

// Frame names the plane an orbit's inclination and node are measured in.
type Frame string

// The two reference planes any orbit here uses.
const (
	// FrameEcliptic is the mean ecliptic and equinox of J2000.
	FrameEcliptic Frame = "ecliptic"
	// FrameLaplace is a satellite's local Laplace plane, given by its pole in J2000 equatorial
	// coordinates. For the close-in major moons this plane is the planet's equator, so the pole is
	// what sets the orbit and the node barely matters; for the distant ones it is tilted toward the
	// planet's own orbit and both are published.
	FrameLaplace Frame = "laplace"
)

// AUPerKm converts kilometres to astronomical units. Every distance in a Spec is AU, including the
// satellites', so the propagator never has to ask which unit it is holding.
const AUPerKm = 1.0 / 149597870.7

// Spec is everything needed to propagate and draw one orbit. Rates are per day and the elements are
// evaluated linearly from EpochJD, which is exact for the mean-element sets used here and keeps the
// browser's mirror of this propagator to a dozen lines.
type Spec struct {
	Centre  string  `json:"centre"` // "sun", or the parent body's key
	Frame   Frame   `json:"frame"`
	PoleRA  float64 `json:"pole_ra_deg,omitempty"`  // Laplace-plane pole, J2000 equatorial
	PoleDec float64 `json:"pole_dec_deg,omitempty"` //
	EpochJD float64 `json:"epoch_jd"`

	A    float64 `json:"a_au"`            // semi-major axis
	ADot float64 `json:"a_dot,omitempty"` // AU per day
	E    float64 `json:"e"`               //
	EDot float64 `json:"e_dot,omitempty"` // per day
	I    float64 `json:"i_deg"`           // inclination to Frame
	IDot float64 `json:"i_dot,omitempty"` // degrees per day
	Node float64 `json:"node_deg"`        // longitude of the ascending node Ω
	NDot float64 `json:"node_dot,omitempty"`
	Peri float64 `json:"peri_deg"` // argument of periapsis ω
	PDot float64 `json:"peri_dot,omitempty"`
	M    float64 `json:"m_deg"` // mean anomaly at EpochJD
	N    float64 `json:"n_deg"` // mean motion, degrees per day

	PeriodDays float64 `json:"period_days"`
}

// ElementsAt evaluates the element set at a Julian date.
func (s Spec) ElementsAt(jd float64) astro.Elements {
	d := jd - s.EpochJD
	return astro.Elements{
		A:       s.A + s.ADot*d,
		E:       s.E + s.EDot*d,
		IDeg:    s.I + s.IDot*d,
		NodeDeg: s.Node + s.NDot*d,
		PeriDeg: s.Peri + s.PDot*d,
		MDeg:    s.M + s.N*d,
	}
}

// PositionAt returns the body's position relative to the centre of its orbit, in AU, in the mean
// ecliptic and equinox of J2000 — the one frame the whole scene is drawn in.
func (s Spec) PositionAt(jd float64) (x, y, z float64) {
	px, py, pz := astro.ElementsPosition(s.ElementsAt(jd))
	if s.Frame != FrameLaplace {
		return px, py, pz
	}
	u, v, w := LaplaceBasis(s.PoleRA, s.PoleDec)
	return px*u[0] + py*v[0] + pz*w[0],
		px*u[1] + py*v[1] + pz*w[1],
		px*u[2] + py*v[2] + pz*w[2]
}

// LaplaceBasis builds the orthonormal frame a satellite's elements live in, given its Laplace-plane
// pole in J2000 equatorial coordinates. The returned vectors are expressed in the J2000 ecliptic
// frame: w is the plane's north pole, u is its ascending node on the ecliptic (the direction the
// node angle Ω is measured from), and v completes the right-handed set.
func LaplaceBasis(poleRA, poleDec float64) (u, v, w [3]float64) {
	w = EquatorialToEclipticVec(unitVector(poleRA, poleDec))

	// u = ẑ_ecliptic × w, the plane's ascending node on the ecliptic. When the plane IS the ecliptic
	// that cross product vanishes and any node direction is as good as another, so fall back to the
	// equinox rather than dividing by zero.
	u = [3]float64{-w[1], w[0], 0}
	if n := math.Hypot(u[0], u[1]); n > 1e-12 {
		u[0], u[1] = u[0]/n, u[1]/n
	} else {
		u = [3]float64{1, 0, 0}
	}
	v = [3]float64{w[1]*u[2] - w[2]*u[1], w[2]*u[0] - w[0]*u[2], w[0]*u[1] - w[1]*u[0]}
	return u, v, w
}

// unitVector turns a right-ascension/declination pair into a unit vector in that same frame.
func unitVector(raDeg, decDeg float64) [3]float64 {
	cd := math.Cos(decDeg * math.Pi / 180)
	return [3]float64{
		cd * math.Cos(raDeg*math.Pi/180),
		cd * math.Sin(raDeg*math.Pi/180),
		math.Sin(decDeg * math.Pi / 180),
	}
}

// obliquityJ2000 is the mean obliquity of the ecliptic at J2000. The scene's frame is J2000 by
// definition, so this never varies with time.
const obliquityJ2000 = 23.43928 * math.Pi / 180

// EquatorialToEclipticVec rotates a J2000 equatorial vector into the J2000 ecliptic frame — the
// inverse of the rotation astro.EclipticJ2000ToEquatorial applies.
func EquatorialToEclipticVec(p [3]float64) [3]float64 {
	c, s := math.Cos(obliquityJ2000), math.Sin(obliquityJ2000)
	return [3]float64{p[0], p[1]*c + p[2]*s, -p[1]*s + p[2]*c}
}

// EclipticToEquatorialVec rotates a J2000 ecliptic vector into J2000 equatorial coordinates.
func EclipticToEquatorialVec(p [3]float64) [3]float64 {
	c, s := math.Cos(obliquityJ2000), math.Sin(obliquityJ2000)
	return [3]float64{p[0], p[1]*c - p[2]*s, p[1]*s + p[2]*c}
}
