// Package eclipsegeom answers, for one place and one instant, where the Moon sits on the Sun.
//
// It exists because the picture cannot always be trusted about its own phase. Fitting two circles
// to a crescent works well down the middle of the eclipse but is weakest exactly where a sequence
// most needs it: at 96% obscuration the solar arc is a sliver twenty pixels wide, so the fitted
// centre is poorly determined perpendicular to it, and near first and last contact the occulter's
// bite spans so little of the limb that it may not be fitted at all. The sky, by contrast, is
// exact. Two bodies, known orbits, one clock — the obscuration at a given second is a number, not
// a measurement, and it is monotone on each side of maximum, which is what makes it usable as the
// ORDERING key for a phase sequence.
//
// The measured fit is still what the finish and the masks use — this package never touches pixels.
// It decides which instants to stack, and it supplies the two things no image can give up on its
// own: which side of maximum a frame belongs to, and the true position angle of the Moon, without
// which a hand-held afocal panel cannot be rotated into a sky frame.
//
// Everything here is TOPOCENTRIC. The Moon's parallax reaches nearly a degree — four solar radii —
// so a geocentric answer is not merely imprecise, it is a different eclipse.
package eclipsegeom

import (
	"math"
	"time"

	"github.com/soniakeys/meeus/v3/base"
	"github.com/soniakeys/meeus/v3/deltat"
	"github.com/soniakeys/meeus/v3/julian"
	"github.com/soniakeys/meeus/v3/moonposition"
	"github.com/soniakeys/meeus/v3/nutation"
	"github.com/soniakeys/meeus/v3/solar"

	"github.com/verove-jordan/astronomy/internal/astro"
)

const (
	deg2rad = math.Pi / 180
	rad2deg = 180 / math.Pi

	// Physical radii (km) and the astronomical unit, for the semidiameters.
	sunRadiusKm  = 696000.0
	moonRadiusKm = 1737.4
	auKm         = 149597870.7

	// earthRadiusKm is the equatorial radius the observer vector is scaled by (Meeus ch. 11).
	earthRadiusKm = 6378.14

	// lowSunDeg is the altitude below which differential refraction flattens the disc enough to
	// see. At 18° the vertical axis is compressed 0.3%; at 1.3° — where the last clip of the
	// 12 Aug 2026 session sits, five minutes from sunset — it is compressed about 5%, which is a
	// visibly oval Sun standing next to round ones in a sequence.
	lowSunDeg = 8.0
)

// Site is the observing place. Elevation is metres above the ellipsoid; it moves the answer by far
// less than the latitude does, but it is free to carry.
type Site struct {
	LatDeg float64
	LonDeg float64 // east-positive, matching astro.LST
	ElevM  float64
}

// Circumstance is the two-body geometry at one instant, as seen from one Site.
type Circumstance struct {
	// Obscuration is the fraction of the solar DISC the Moon covers, 0..1 — an area, not a
	// diameter. It is the same quantity solar.OverlapFraction measures off the pixels, so the two
	// can be compared directly and a disagreement is a diagnosis.
	Obscuration float64 `json:"obscuration"`
	// SepArcsec is the topocentric angular distance between the two centres.
	SepArcsec float64 `json:"sep_arcsec"`
	// SunRadiusArcsec and MoonRadiusArcsec are the topocentric semidiameters.
	SunRadiusArcsec  float64 `json:"sun_radius_arcsec"`
	MoonRadiusArcsec float64 `json:"moon_radius_arcsec"`
	// MoonPADeg is the position angle of the Moon's centre seen from the Sun's, measured North
	// through East. This is the number a panel's own measured Sun→Moon direction is compared
	// against to recover how the camera was rolled.
	MoonPADeg float64 `json:"moon_pa_deg"`
	// SunAltDeg is the geometric (unrefracted) altitude of the Sun.
	SunAltDeg float64 `json:"sun_alt_deg"`
	// ParallacticDeg is the angle between celestial North and the local vertical at the Sun —
	// which is what locates the direction refraction squashes the disc along.
	ParallacticDeg float64 `json:"parallactic_deg"`
	// RefractFlatten is the apparent vertical extent over the true one, 1 when the Sun is high.
	RefractFlatten float64 `json:"refract_flatten"`
}

// Eclipsed reports whether the two discs overlap at all.
func (c Circumstance) Eclipsed() bool { return c.Obscuration > 0 }

// Magnitude is how far the Moon has bitten into the solar DIAMETER, 0..1 — the eclipse magnitude of
// the almanacs.
//
// A sequence spaces its panels by this rather than by obscuration because it is what the eye reads.
// Obscuration is an area and it saturates: between 90% and 96% covered the picture changes from a
// crescent a tenth of the radius thick to one a twenty-fifth as thick, a visibly different image,
// while the area moves only six points. Magnitude tracks that change linearly.
func (c Circumstance) Magnitude() float64 {
	if c.SunRadiusArcsec <= 0 {
		return 0
	}
	return clampUnit((c.SunRadiusArcsec + c.MoonRadiusArcsec - c.SepArcsec) / (2 * c.SunRadiusArcsec))
}

// At returns the circumstances at t for the site.
func At(t time.Time, s Site) Circumstance {
	jde := toJDE(t)
	sun, moon := topocentric(jde, t, s)

	sep := angleBetween(sun.vec, moon.vec) * 3600
	rs := math.Asin(sunRadiusKm/sun.dist) * rad2deg * 3600
	rm := math.Asin(moonRadiusKm/moon.dist) * rad2deg * 3600

	alt, _ := astro.Horizontal(sun.raDeg, sun.decDeg, s.LatDeg, s.LonDeg, t)
	return Circumstance{
		Obscuration:      obscuration(sep, rs, rm),
		SepArcsec:        sep,
		SunRadiusArcsec:  rs,
		MoonRadiusArcsec: rm,
		MoonPADeg:        positionAngle(sun.raDeg, sun.decDeg, moon.raDeg, moon.decDeg),
		SunAltDeg:        alt,
		ParallacticDeg:   parallactic(sun.raDeg, sun.decDeg, s, t),
		RefractFlatten:   refractFlatten(alt, rs/3600),
	}
}

// body is one topocentric position: a unit-ish rectangular vector, its distance, and the RA/Dec it
// implies. Carrying all three avoids converting back and forth for the separation, the position
// angle and the altitude, which want different forms of the same answer.
type body struct {
	vec           [3]float64
	dist          float64 // km
	raDeg, decDeg float64
}

// topocentric places the Sun and the Moon as seen from the site, by subtracting the observer's own
// geocentric position from each geocentric vector. Doing it in rectangular coordinates rather than
// through the classical parallax reduction keeps the separation, the semidiameters and the position
// angle all consistent with ONE set of vectors — the reduction formulae correct RA and Dec but not
// the distance, and the semidiameters need the distance.
func topocentric(jde float64, t time.Time, s Site) (sun, moon body) {
	tc := base.J2000Century(jde)
	dPsi, dEps := nutation.Nutation(jde)
	eps := nutation.MeanObliquity(jde).Rad() + dEps.Rad()

	// Sun: meeus' apparent equatorial already carries nutation and aberration.
	sunRA, sunDec := solar.ApparentEquatorial(jde)
	sunDist := solar.Radius(tc) * auKm

	// Moon: ecliptic of date, so nutation in longitude has to be added by hand to match the Sun's
	// frame. It is only ~17", but it is 17" applied to one body and not the other, and near
	// maximum a sequence is choosing between instants a few arcseconds apart.
	lam, bet, moonDist := moonposition.Position(jde)
	moonRA, moonDec := eclipticToEquatorial(lam.Rad()+dPsi.Rad(), bet.Rad(), eps)

	obs := observerVector(t, s)
	sun = geoToTopo(sunRA.Deg(), sunDec.Deg(), sunDist, obs)
	moon = geoToTopo(moonRA, moonDec, moonDist, obs)
	return sun, moon
}

// geoToTopo subtracts the observer from a geocentric position and re-derives RA/Dec from what is
// left.
func geoToTopo(raDeg, decDeg, distKm float64, obs [3]float64) body {
	g := sphericalToRect(raDeg, decDeg, distKm)
	v := [3]float64{g[0] - obs[0], g[1] - obs[1], g[2] - obs[2]}
	d := math.Sqrt(v[0]*v[0] + v[1]*v[1] + v[2]*v[2])
	return body{
		vec:    v,
		dist:   d,
		raDeg:  norm360(math.Atan2(v[1], v[0]) * rad2deg),
		decDeg: math.Asin(v[2]/d) * rad2deg,
	}
}

// observerVector is the site's geocentric equatorial rectangular position in km.
func observerVector(t time.Time, s Site) [3]float64 {
	rhoSin, rhoCos := observerParallax(s.LatDeg, s.ElevM)
	lst := astro.LST(t, s.LonDeg) * deg2rad
	return [3]float64{
		earthRadiusKm * rhoCos * math.Cos(lst),
		earthRadiusKm * rhoCos * math.Sin(lst),
		earthRadiusKm * rhoSin,
	}
}

// observerParallax returns ρ·sinφ′ and ρ·cosφ′ for the site (Meeus ch. 11).
func observerParallax(latDeg, elevM float64) (rhoSin, rhoCos float64) {
	phi := latDeg * deg2rad
	u := math.Atan(0.99664719 * math.Tan(phi))
	rhoSin = 0.99664719*math.Sin(u) + elevM/6378140*math.Sin(phi)
	rhoCos = math.Cos(u) + elevM/6378140*math.Cos(phi)
	return rhoSin, rhoCos
}

// obscuration is the area of the circular lens the two discs share, over the solar disc's own area.
// Radii and separation must share units.
func obscuration(sep, rSun, rMoon float64) float64 {
	switch {
	case rSun <= 0:
		return 0
	case sep >= rSun+rMoon:
		return 0
	case sep <= rMoon-rSun:
		return 1 // total: the Moon swallows the Sun
	case sep <= rSun-rMoon:
		return (rMoon / rSun) * (rMoon / rSun) // annular: the Moon sits wholly inside
	}
	a1 := math.Acos(clamp1((sep*sep + rSun*rSun - rMoon*rMoon) / (2 * sep * rSun)))
	a2 := math.Acos(clamp1((sep*sep + rMoon*rMoon - rSun*rSun) / (2 * sep * rMoon)))
	area := rSun*rSun*(a1-math.Sin(2*a1)/2) + rMoon*rMoon*(a2-math.Sin(2*a2)/2)
	return clampUnit(area / (math.Pi * rSun * rSun))
}

// positionAngle returns the direction from body A to body B, North through East, in degrees.
func positionAngle(raA, decA, raB, decB float64) float64 {
	dra := (raB - raA) * deg2rad
	dA, dB := decA*deg2rad, decB*deg2rad
	y := math.Sin(dra) * math.Cos(dB)
	x := math.Cos(dA)*math.Sin(dB) - math.Sin(dA)*math.Cos(dB)*math.Cos(dra)
	return norm360(math.Atan2(y, x) * rad2deg)
}

// parallactic returns the parallactic angle at the Sun: the angle at the body between the direction
// to the celestial pole and the direction to the zenith.
func parallactic(raDeg, decDeg float64, s Site, t time.Time) float64 {
	h := astro.HourAngleDeg(raDeg, s.LonDeg, t) * deg2rad
	dec := decDeg * deg2rad
	lat := s.LatDeg * deg2rad
	return math.Atan2(math.Sin(h), math.Tan(lat)*math.Cos(dec)-math.Sin(dec)*math.Cos(h)) * rad2deg
}

// refractFlatten returns the apparent vertical extent of the disc over its true extent, by asking
// the refraction model where the top and the bottom of the disc appear. Above lowSunDeg the effect
// is under half a percent and is not worth resampling a panel for, so it is reported as exactly 1.
func refractFlatten(altDeg, radiusDeg float64) float64 {
	if altDeg > lowSunDeg || radiusDeg <= 0 {
		return 1
	}
	top := astro.ApparentAltitude(altDeg + radiusDeg)
	bottom := astro.ApparentAltitude(altDeg - radiusDeg)
	f := (top - bottom) / (2 * radiusDeg)
	if f <= 0 || f > 1 {
		return 1 // below the horizon the model stops meaning anything
	}
	return f
}

func sphericalToRect(raDeg, decDeg, dist float64) [3]float64 {
	ra, dec := raDeg*deg2rad, decDeg*deg2rad
	return [3]float64{
		dist * math.Cos(dec) * math.Cos(ra),
		dist * math.Cos(dec) * math.Sin(ra),
		dist * math.Sin(dec),
	}
}

func eclipticToEquatorial(lam, bet, eps float64) (raDeg, decDeg float64) {
	dec := math.Asin(math.Sin(bet)*math.Cos(eps) + math.Cos(bet)*math.Sin(eps)*math.Sin(lam))
	ra := math.Atan2(math.Sin(lam)*math.Cos(eps)-math.Tan(bet)*math.Sin(eps), math.Cos(lam))
	return norm360(ra * rad2deg), dec * rad2deg
}

func angleBetween(a, b [3]float64) float64 {
	na := math.Sqrt(a[0]*a[0] + a[1]*a[1] + a[2]*a[2])
	nb := math.Sqrt(b[0]*b[0] + b[1]*b[1] + b[2]*b[2])
	if na == 0 || nb == 0 {
		return 0
	}
	dot := (a[0]*b[0] + a[1]*b[1] + a[2]*b[2]) / (na * nb)
	return math.Acos(clamp1(dot)) * rad2deg
}

// toJDE converts civil UTC to a Julian Ephemeris Day. ΔT is about 69 s in this era, and 69 s is
// 35 arcseconds of relative lunar motion — a twentieth of the solar radius, and enough to move a
// contact time by more than a minute.
func toJDE(t time.Time) float64 {
	jd := julian.TimeToJD(t.UTC())
	return jd + deltat.Interp10A(jd).Sec()/86400.0
}

func norm360(d float64) float64 {
	d = math.Mod(d, 360)
	if d < 0 {
		d += 360
	}
	return d
}

func clamp1(x float64) float64 {
	if x > 1 {
		return 1
	}
	if x < -1 {
		return -1
	}
	return x
}

func clampUnit(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}
