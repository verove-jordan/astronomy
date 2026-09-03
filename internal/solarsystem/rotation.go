package solarsystem

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/astro"
)

// Rotation: where a body's axis points and how far its prime meridian has turned.
//
// The IAU/IAG convention is a north pole given as a right ascension and declination in J2000
// equatorial coordinates, drifting slowly, plus a prime-meridian angle W measured easterly along the
// body's equator from the node Q where that equator crosses the J2000 equator. Those three numbers
// are the whole of a world's orientation, and turning them into a basis is what lets a texture be
// drawn on a globe with the right features facing the right way at the right time.

// Orientation is a body's rotational state at one instant.
type Orientation struct {
	PoleRA  float64 `json:"pole_ra_deg"`  // J2000 equatorial
	PoleDec float64 `json:"pole_dec_deg"` //
	WDeg    float64 `json:"w_deg"`        // prime meridian, wrapped to [0,360)
}

// OrientationAt evaluates the rotational elements at a Julian date. The pole terms are per Julian
// century and the prime meridian is per day, exactly as the IAU report tabulates them.
func (p Pole) OrientationAt(jd float64) Orientation {
	t := astro.JulianCenturies(jd)
	d := jd - astro.J2000
	ra, dec, w := p.RA0+p.RADot*t, p.Dec0+p.DecDot*t, p.W0+p.WDot*d
	if p.Lib != nil {
		n := (p.Lib.Arg0 + p.Lib.ArgDot*t) * math.Pi / 180
		ra += p.Lib.RAAmp * math.Sin(n)
		dec += p.Lib.DecAmp * math.Cos(n)
		w += p.Lib.WAmp * math.Sin(n)
	}
	return Orientation{
		PoleRA:  ra,
		PoleDec: dec,
		WDeg:    math.Mod(math.Mod(w, 360)+360, 360),
	}
}

// Axis returns the body's north pole as a unit vector in the J2000 ecliptic frame — the direction an
// axis marker is drawn along, and the axis the globe spins about.
func (o Orientation) Axis() [3]float64 {
	return EquatorialToEclipticVec(unitVector(o.PoleRA, o.PoleDec))
}

// Basis returns the body-fixed axes expressed in the J2000 ecliptic frame: x through the prime
// meridian on the equator, z through the north pole, y completing a right-handed set. Together they
// are the rotation matrix that carries body coordinates into the scene.
//
// A retrograde rotator needs no special case. Its pole is simply given south of the orbit plane, so
// the same right-handed construction spins it the other way — which is why Venus and Uranus turn
// backwards here without a single sign test.
func (o Orientation) Basis() (x, y, z [3]float64) {
	pole := unitVector(o.PoleRA, o.PoleDec)

	// Q, the ascending node of the body's equator on the J2000 equator, is 90° ahead of the pole in
	// right ascension and lies in the equatorial plane.
	ra := o.PoleRA * math.Pi / 180
	q := [3]float64{-math.Sin(ra), math.Cos(ra), 0}

	// zq = pole × Q completes the frame Q rotates in; W is measured from Q toward it.
	zq := cross(pole, q)
	cw, sw := math.Cos(o.WDeg*math.Pi/180), math.Sin(o.WDeg*math.Pi/180)
	prime := [3]float64{
		q[0]*cw + zq[0]*sw,
		q[1]*cw + zq[1]*sw,
		q[2]*cw + zq[2]*sw,
	}

	x = EquatorialToEclipticVec(prime)
	z = EquatorialToEclipticVec(pole)
	y = cross(z, x)
	return x, y, z
}

// AxialTiltDeg is the angle between the body's rotation axis and the normal to its own orbit — the
// number quoted as "axial tilt", and the reason a world has seasons. Bodies with no orbit of their
// own (the Sun) are measured against the ecliptic instead.
//
// The axis here is the ANGULAR-VELOCITY vector, not the IAU north pole, and the two are opposite for
// a retrograde rotator: the IAU names as "north" whichever pole lies north of the invariable plane,
// so Venus's IAU pole sits only 2.6° from its orbit normal even though Venus turns backwards. Taking
// the sense of rotation instead is what yields the 177° and 98° everyone quotes for Venus and
// Uranus, and it is the number that actually describes the world.
func AxialTiltDeg(b Body, jd float64) float64 {
	axis := b.Pole.OrientationAt(jd).Axis()
	if b.Pole.WDot < 0 {
		axis = [3]float64{-axis[0], -axis[1], -axis[2]}
	}
	normal := [3]float64{0, 0, 1}
	if b.Orbit != nil {
		normal = orbitNormal(*b.Orbit)
	}
	return math.Acos(clampUnit(dot(axis, normal))) * 180 / math.Pi
}

// orbitNormal returns the unit normal of an orbit plane in the J2000 ecliptic frame.
func orbitNormal(s Spec) [3]float64 {
	el := s.ElementsAt(s.EpochJD)
	// In the orbit's own reference plane the normal is +z, inclined by i about the node line.
	ci, si := math.Cos(el.IDeg*math.Pi/180), math.Sin(el.IDeg*math.Pi/180)
	cn, sn := math.Cos(el.NodeDeg*math.Pi/180), math.Sin(el.NodeDeg*math.Pi/180)
	n := [3]float64{si * sn, -si * cn, ci}
	if s.Frame != FrameLaplace {
		return n
	}
	u, v, w := LaplaceBasis(s.PoleRA, s.PoleDec)
	return [3]float64{
		n[0]*u[0] + n[1]*v[0] + n[2]*w[0],
		n[0]*u[1] + n[1]*v[1] + n[2]*w[1],
		n[0]*u[2] + n[1]*v[2] + n[2]*w[2],
	}
}

func cross(a, b [3]float64) [3]float64 {
	return [3]float64{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}

func dot(a, b [3]float64) float64 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }

func clampUnit(x float64) float64 {
	if x > 1 {
		return 1
	}
	if x < -1 {
		return -1
	}
	return x
}
