package astro

import (
	"math"
	"time"
)

// Heliocentric positions of the major bodies in a FIXED frame — the mean ecliptic and equinox of
// J2000 — which is what a 3-D scene needs and what planets.go deliberately does not provide: its
// helioRect is referred to the ecliptic OF DATE, so a solar system drawn from it would slowly rotate
// under the camera as the equinox precesses.
//
// The model is Standish's "Keplerian Elements for Approximate Positions of the Major Planets"
// (JPL Solar System Dynamics), Table 1: six elements and six per-century rates per body, fitted for
// 1800 AD – 2050 AD. Accuracy over that span is arcminute-class — the same grade as the Schlyter
// theory in planets.go, and heliocentric_test.go pins the two against each other so the 3-D map and
// the planner can never describe different skies.
//
// Outside [ElementsFrom, ElementsTo] the fit is not defended: callers are expected to refuse the
// instant rather than draw a position the table cannot stand behind.

// ElementsFrom and ElementsTo bound the years over which the element table is valid.
const (
	ElementsFrom = 1800
	ElementsTo   = 2050
)

// Body identifies a body in the approximate-element table. It is deliberately a separate enum from
// Planet: this set contains the Earth-Moon barycentre (the observer, which Planet has no room for)
// and Pluto, and omits nothing in between.
type Body int

// The bodies Standish Table 1 covers, in heliocentric order.
const (
	BodyMercury Body = iota
	BodyVenus
	BodyEarth // strictly the Earth-Moon barycentre
	BodyMars
	BodyJupiter
	BodySaturn
	BodyUranus
	BodyNeptune
	BodyPluto
)

// Bodies is the iteration order of the element table.
var Bodies = []Body{BodyMercury, BodyVenus, BodyEarth, BodyMars, BodyJupiter, BodySaturn, BodyUranus, BodyNeptune, BodyPluto}

// String returns the lowercase canonical key, matching Planet.String() where the two sets overlap.
func (b Body) String() string {
	switch b {
	case BodyMercury:
		return "mercury"
	case BodyVenus:
		return "venus"
	case BodyEarth:
		return "earth"
	case BodyMars:
		return "mars"
	case BodyJupiter:
		return "jupiter"
	case BodySaturn:
		return "saturn"
	case BodyUranus:
		return "uranus"
	case BodyNeptune:
		return "neptune"
	case BodyPluto:
		return "pluto"
	}
	return "body"
}

// PlanetBody maps a Planet onto its Body. Both enums name the same seven worlds.
func PlanetBody(p Planet) Body {
	switch p {
	case Mercury:
		return BodyMercury
	case Venus:
		return BodyVenus
	case Mars:
		return BodyMars
	case Jupiter:
		return BodyJupiter
	case Saturn:
		return BodySaturn
	case Uranus:
		return BodyUranus
	case Neptune:
		return BodyNeptune
	}
	return BodyEarth
}

// ElementSet is one row of the table: each element and its rate of change per Julian century.
// Angles are degrees, a is AU, and the angles are the LONGITUDE forms Standish tabulates —
// mean longitude L and longitude of perihelion ϖ, not mean anomaly and argument of perihelion.
type ElementSet struct {
	A, ADot          float64 // semi-major axis
	E, EDot          float64 // eccentricity (dimensionless)
	IDeg, IDot       float64 // inclination to the J2000 ecliptic
	LDeg, LDot       float64 // mean longitude
	PeriDeg, PeriDot float64 // longitude of perihelion ϖ
	NodeDeg, NodeDot float64 // longitude of the ascending node Ω
}

// Elements is a body's Keplerian element set evaluated at an instant, in the form an orbit is drawn
// and propagated from: argument of perihelion and mean anomaly rather than the longitude forms.
type Elements struct {
	A       float64 // semi-major axis (AU)
	E       float64 // eccentricity
	IDeg    float64 // inclination to the J2000 ecliptic
	NodeDeg float64 // longitude of the ascending node Ω
	PeriDeg float64 // argument of perihelion ω = ϖ − Ω
	MDeg    float64 // mean anomaly, wrapped to (−180,180]
}

// ElementTable returns the raw element row for a body. Exposed so the browser can be handed the same
// numbers the engine propagates from, rather than a sampled trajectory it would have to interpolate.
func ElementTable(b Body) ElementSet {
	switch b {
	case BodyMercury:
		return ElementSet{0.38709927, 0.00000037, 0.20563593, 0.00001906, 7.00497902, -0.00594749,
			252.25032350, 149472.67411175, 77.45779628, 0.16047689, 48.33076593, -0.12534081}
	case BodyVenus:
		return ElementSet{0.72333566, 0.00000390, 0.00677672, -0.00004107, 3.39467605, -0.00078890,
			181.97909950, 58517.81538729, 131.60246718, 0.00268329, 76.67984255, -0.27769418}
	case BodyEarth:
		return ElementSet{1.00000261, 0.00000562, 0.01671123, -0.00004392, -0.00001531, -0.01294668,
			100.46457166, 35999.37244981, 102.93768193, 0.32327364, 0.0, 0.0}
	case BodyMars:
		return ElementSet{1.52371034, 0.00001847, 0.09339410, 0.00007882, 1.84969142, -0.00813131,
			-4.55343205, 19140.30268499, -23.94362959, 0.44441088, 49.55953891, -0.29257343}
	case BodyJupiter:
		return ElementSet{5.20288700, -0.00011607, 0.04838624, -0.00013253, 1.30439695, -0.00183714,
			34.39644051, 3034.74612775, 14.72847983, 0.21252668, 100.47390909, 0.20469106}
	case BodySaturn:
		return ElementSet{9.53667594, -0.00125060, 0.05386179, -0.00050991, 2.48599187, 0.00193609,
			49.95424423, 1222.49362201, 92.59887831, -0.41897216, 113.66242448, -0.28867794}
	case BodyUranus:
		return ElementSet{19.18916464, -0.00196176, 0.04725744, -0.00004397, 0.77263783, -0.00242939,
			313.23810451, 428.48202785, 170.95427630, 0.40805281, 74.01692503, 0.04240589}
	case BodyNeptune:
		return ElementSet{30.06992276, 0.00026291, 0.00859048, 0.00005105, 1.77004347, 0.00035372,
			-55.12002969, 218.45945325, 44.96476227, -0.32241464, 131.78422574, -0.00508664}
	case BodyPluto:
		return ElementSet{39.48211675, -0.00031596, 0.24882730, 0.00005170, 17.14001206, 0.00004818,
			238.92903833, 145.20780515, 224.06891629, -0.04062942, 110.30393684, -0.01183482}
	}
	return ElementSet{}
}

// OrbitElements evaluates a body's elements at t. The returned set is what both the position and the
// drawn ellipse come from, so the dot on the orbit can never sit off the curve it is drawn on.
func OrbitElements(b Body, t time.Time) Elements {
	s := ElementTable(b)
	T := JulianCenturies(JulianDate(t))
	peri := s.PeriDeg + s.PeriDot*T
	node := s.NodeDeg + s.NodeDot*T
	return Elements{
		A:       s.A + s.ADot*T,
		E:       s.E + s.EDot*T,
		IDeg:    s.IDeg + s.IDot*T,
		NodeDeg: norm360(node),
		PeriDeg: norm360(peri - node),
		MDeg:    norm180(s.LDeg + s.LDot*T - peri),
	}
}

// HelioEclipticJ2000 returns a body's heliocentric position in AU, referred to the mean ecliptic and
// equinox of J2000: +x toward the J2000 equinox, +z toward the ecliptic north pole.
func HelioEclipticJ2000(b Body, t time.Time) (x, y, z float64) {
	return ElementsPosition(OrbitElements(b, t))
}

// ElementsPosition solves Kepler's equation for an element set and rotates the orbital-plane
// position into the J2000 ecliptic frame. Shared by the planets, the dwarf planets and the moons,
// which differ only in where their elements come from.
func ElementsPosition(el Elements) (x, y, z float64) {
	e := solveKepler(el.MDeg, el.E)
	// Position in the orbital plane, perifocus on the +u axis.
	u := el.A * (cosD(e) - el.E)
	v := el.A * math.Sqrt(1-el.E*el.E) * sinD(e)

	cw, sw := cosD(el.PeriDeg), sinD(el.PeriDeg)
	cn, sn := cosD(el.NodeDeg), sinD(el.NodeDeg)
	ci, si := cosD(el.IDeg), sinD(el.IDeg)

	x = (cw*cn-sw*sn*ci)*u + (-sw*cn-cw*sn*ci)*v
	y = (cw*sn+sw*cn*ci)*u + (-sw*sn+cw*cn*ci)*v
	z = (sw*si)*u + (cw*si)*v
	return
}

// EclipticJ2000ToEquatorial rotates a J2000 ecliptic vector into J2000 equatorial coordinates and
// reports the vector's length. The obliquity is the fixed J2000 value, not the obliquity of date —
// the frame is J2000 by definition, so nothing here varies with t.
func EclipticJ2000ToEquatorial(x, y, z float64) (raDeg, decDeg, dist float64) {
	const obliquityJ2000 = 23.43928
	xe := x
	ye := y*cosD(obliquityJ2000) - z*sinD(obliquityJ2000)
	ze := y*sinD(obliquityJ2000) + z*cosD(obliquityJ2000)
	raDeg = norm360(atan2d(ye, xe))
	decDeg = atan2d(ze, math.Hypot(xe, ye))
	dist = math.Sqrt(x*x + y*y + z*z)
	return
}
