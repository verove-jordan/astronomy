package solarsystem

import (
	"math"
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"
)

// The scene at one instant.
//
// This is the authoritative readout: what the engine says is true at a moment, computed once, in
// double precision, from the same models the planner pages use. The browser animates from the
// elements themselves and agrees with this to the last decimal because both run the same arithmetic
// — but when a number is printed on screen for someone to trust, it comes from here.

// Site is the observer's position on Earth.
type Site struct {
	LatDeg     float64 `json:"lat_deg"`
	LonDeg     float64 `json:"lon_deg"`
	ElevationM float64 `json:"elevation_m,omitempty"`
}

// BodyState is one body at one instant.
type BodyState struct {
	Key  string `json:"key"`
	Kind Kind   `json:"kind"`

	Helio [3]float64  `json:"helio_au"`           // heliocentric, J2000 ecliptic
	Local *[3]float64 `json:"local_au,omitempty"` // relative to its parent, for moons

	HelioDistAU float64 `json:"helio_dist_au"`
	GeoDistAU   float64 `json:"geo_dist_au"`

	RADeg   float64 `json:"ra_deg"` // geocentric, apparent equinox of date — as the planner quotes it
	DecDeg  float64 `json:"dec_deg"`
	AltDeg  float64 `json:"alt_deg"`
	AzDeg   float64 `json:"az_deg"`
	Up      bool    `json:"up"`
	Airmass float64 `json:"airmass,omitempty"`

	Magnitude         float64 `json:"magnitude"`
	AngularDiamArcsec float64 `json:"angular_diameter_arcsec"`
	PhaseAngleDeg     float64 `json:"phase_angle_deg"`
	IllumFraction     float64 `json:"illum_fraction"`
	ElongationDeg     float64 `json:"elongation_deg"`

	Orientation  Orientation `json:"orientation"`
	AxialTiltDeg float64     `json:"axial_tilt_deg"`
	RingOpenDeg  float64     `json:"ring_open_deg,omitempty"` // ring-plane tilt toward Earth; 0 is edge-on
}

// Snapshot is every body at one instant, for one observing site.
type Snapshot struct {
	TimeMs int64       `json:"time_ms"`
	JD     float64     `json:"jd"`
	Site   Site        `json:"site"`
	Bodies []BodyState `json:"bodies"`
}

// sunMagnitude is the Sun's apparent visual magnitude from Earth — a constant, not a computation.
const sunMagnitude = -26.74

// StateAt computes the whole scene at t for an observer at site.
func StateAt(t time.Time, site Site) Snapshot {
	jd := astro.JulianDate(t)
	earth := heliocentricOf(mustFind("earth"), jd)

	snap := Snapshot{TimeMs: t.UTC().UnixMilli(), JD: jd, Site: site, Bodies: make([]BodyState, 0, len(All()))}
	for _, b := range All() {
		snap.Bodies = append(snap.Bodies, bodyStateAt(b, jd, t, earth, site))
	}
	return snap
}

// HeliocentricAt returns a body's heliocentric position in AU at a Julian date, following the chain
// up through its parent when it is a moon.
func HeliocentricAt(key string, jd float64) [3]float64 {
	b, ok := Find(key)
	if !ok {
		return [3]float64{}
	}
	return heliocentricOf(b, jd)
}

// LocalAt returns a moon's position relative to the body it orbits, in AU. It is the zero vector for
// anything that orbits the Sun.
func LocalAt(b Body, jd float64) [3]float64 {
	switch {
	case b.Series == SeriesMoonAA:
		x, y, z := astro.MoonEclipticJ2000(astro.TimeFromJD(jd))
		return [3]float64{x, y, z}
	case b.Orbit != nil && b.Orbit.Centre != "sun":
		x, y, z := b.Orbit.PositionAt(jd)
		return [3]float64{x, y, z}
	}
	return [3]float64{}
}

func heliocentricOf(b Body, jd float64) [3]float64 {
	if b.Kind == KindStar {
		return [3]float64{}
	}
	if b.Parent != "" {
		parent, ok := Find(b.Parent)
		if !ok {
			return [3]float64{}
		}
		p := heliocentricOf(parent, jd)
		l := LocalAt(b, jd)
		return [3]float64{p[0] + l[0], p[1] + l[1], p[2] + l[2]}
	}
	if b.Orbit == nil {
		return [3]float64{}
	}
	x, y, z := b.Orbit.PositionAt(jd)
	return [3]float64{x, y, z}
}

func bodyStateAt(b Body, jd float64, t time.Time, earth [3]float64, site Site) BodyState {
	helio := heliocentricOf(b, jd)
	geo := [3]float64{helio[0] - earth[0], helio[1] - earth[1], helio[2] - earth[2]}

	st := BodyState{
		Key:         b.Key,
		Kind:        b.Kind,
		Helio:       helio,
		HelioDistAU: norm(helio),
		GeoDistAU:   norm(geo),
		Orientation: b.Pole.OrientationAt(jd),
	}
	if b.Parent != "" {
		l := LocalAt(b, jd)
		st.Local = &l
	}
	st.AxialTiltDeg = AxialTiltDeg(b, jd)

	raJ2000, decJ2000, _ := astro.EclipticJ2000ToEquatorial(geo[0], geo[1], geo[2])
	st.RADeg, st.DecDeg = astro.PrecessFromJ2000(raJ2000, decJ2000, t)
	st.AltDeg, st.AzDeg = astro.Horizontal(st.RADeg, st.DecDeg, site.LatDeg, site.LonDeg, t)
	apparent := astro.ApparentAltitude(st.AltDeg)
	st.Up = apparent > 0
	if st.Up {
		st.Airmass = astro.Airmass(apparent)
	}

	// The phase angle is the angle at the body between the Sun and the Earth; the elongation is the
	// angle at the Earth between the Sun and the body. Both fall straight out of the three vectors,
	// with no need for the law of cosines and its sign ambiguities.
	toSun := [3]float64{-helio[0], -helio[1], -helio[2]}
	toEarth := [3]float64{-geo[0], -geo[1], -geo[2]}
	if b.Kind == KindStar {
		st.PhaseAngleDeg, st.ElongationDeg, st.IllumFraction = 0, 0, 1
		st.Magnitude = sunMagnitude
	} else {
		st.PhaseAngleDeg = angleBetween(toSun, toEarth)
		st.IllumFraction = (1 + math.Cos(st.PhaseAngleDeg*math.Pi/180)) / 2
		st.ElongationDeg = angleBetween([3]float64{-earth[0], -earth[1], -earth[2]}, geo)
		st.Magnitude = apparentMagnitude(b, t, st)
	}

	st.AngularDiamArcsec = angularDiameterArcsec(b.RadiusKm, st.GeoDistAU)
	if b.Ring != nil {
		st.RingOpenDeg = ringOpeningDeg(st.Orientation, geo)
	}
	return st
}

// apparentMagnitude keeps the planets on exactly the brightness the planner quotes by asking the
// planner's own model, rather than growing a second set of photometric fits that could disagree with
// it. Bodies that model does not cover fall back to the standard H–G law.
func apparentMagnitude(b Body, t time.Time, st BodyState) float64 {
	for _, p := range astro.Planets {
		if p.String() == b.Key {
			return astro.PlanetPosition(p, t).Magnitude
		}
	}
	if b.Key == "moon" {
		// Allen's phase law. The Moon darkens far faster than its lit fraction shrinks — the quartic
		// term is the opposition surge in reverse — so a plain area law would call a thin crescent
		// three magnitudes brighter than it is.
		p := st.PhaseAngleDeg
		return -12.73 + 0.026*p + 4e-9*p*p*p*p
	}
	return hgMagnitude(absoluteMagnitude(b), st.HelioDistAU, st.GeoDistAU, st.PhaseAngleDeg)
}

// absoluteMagnitude is H, the body's brightness at 1 AU from both the Sun and the observer at zero
// phase angle.
func absoluteMagnitude(b Body) float64 {
	if b.Key == "pluto" {
		return -0.45
	}
	return 99
}

// hgMagnitude is the IAU two-parameter H–G law with the standard slope, the photometric model every
// minor body is catalogued with.
func hgMagnitude(h, r, delta, phaseDeg float64) float64 {
	if h >= 90 || r <= 0 || delta <= 0 {
		return 99
	}
	const g = 0.15
	half := math.Tan(phaseDeg * math.Pi / 180 / 2)
	phi1 := math.Exp(-3.33 * math.Pow(half, 0.63))
	phi2 := math.Exp(-1.87 * math.Pow(half, 1.22))
	return h + 5*math.Log10(r*delta) - 2.5*math.Log10((1-g)*phi1+g*phi2)
}

// angularDiameterArcsec is the full angular diameter a sphere of the given radius subtends.
func angularDiameterArcsec(radiusKm, distAU float64) float64 {
	distKm := distAU * kmPerAU
	if distKm <= 0 || radiusKm <= 0 {
		return 0
	}
	return 2 * math.Asin(math.Min(1, radiusKm/distKm)) * 180 / math.Pi * 3600
}

// ringOpeningDeg is how far the ring plane is tilted toward the Earth: the elevation of the Earth
// above the plane, as seen from the planet. Zero is edge-on, and the sign says which face is lit.
func ringOpeningDeg(o Orientation, geo [3]float64) float64 {
	pole := o.Axis()
	toEarth := [3]float64{-geo[0], -geo[1], -geo[2]}
	n := norm(toEarth)
	if n == 0 {
		return 0
	}
	return math.Asin(clampUnit(dot(pole, [3]float64{toEarth[0] / n, toEarth[1] / n, toEarth[2] / n}))) * 180 / math.Pi
}

// kmPerAU is the astronomical unit in kilometres.
const kmPerAU = 149597870.7

func norm(v [3]float64) float64 { return math.Sqrt(dot(v, v)) }

func angleBetween(a, b [3]float64) float64 {
	na, nb := norm(a), norm(b)
	if na == 0 || nb == 0 {
		return 0
	}
	return math.Acos(clampUnit(dot(a, b)/(na*nb))) * 180 / math.Pi
}

func mustFind(key string) Body {
	b, ok := Find(key)
	if !ok {
		panic("solarsystem: missing body " + key)
	}
	return b
}
