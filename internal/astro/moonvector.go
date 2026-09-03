package astro

import (
	"math"
	"time"
)

// The Moon as a vector rather than as a direction.
//
// MoonPosition gives where the Moon is on the sky, which is all the planner ever needed. A 3-D scene
// also needs how far away it is, so this adds the Astronomical Almanac's companion parallax series —
// the same four arguments the longitude terms in moon.go already use, which is what makes the two
// consistent — and assembles the whole thing into a geocentric vector in the scene's fixed frame.

// earthRadiusKm is the Earth's equatorial radius, the baseline horizontal parallax is measured from.
const earthRadiusKm = 6378.137

// kmPerAU is the astronomical unit.
const kmPerAU = 149597870.7

// MoonParallaxDeg returns the Moon's equatorial horizontal parallax at t, from the Astronomical
// Almanac low-precision series (the distance counterpart of moonEclipticLongitude).
func MoonParallaxDeg(t time.Time) float64 {
	d := JulianDate(t) - J2000
	return 0.9508 +
		0.0518*cosD(134.9+13.064993*d) +
		0.0095*cosD(259.2-13.003*d) +
		0.0078*cosD(235.7+24.381*d) +
		0.0028*cosD(269.9+26.130*d)
}

// MoonDistanceAU returns the geocentric distance to the Moon at t, in AU. Accuracy follows the
// series it comes from: a few hundred kilometres, well under a percent.
func MoonDistanceAU(t time.Time) float64 {
	return earthRadiusKm / sinD(MoonParallaxDeg(t)) / kmPerAU
}

// MoonEclipticLatitudeDeg returns the Moon's ecliptic latitude at t. Exported because a 3-D scene
// needs the out-of-plane component that MoonPosition folds away into RA/Dec.
func MoonEclipticLatitudeDeg(t time.Time) float64 {
	d := JulianDate(t) - J2000
	return 5.13*sinD(93.3+13.229350*d) +
		0.28*sinD(228.2+26.294*d) -
		0.28*sinD(318.3+0.980*d) -
		0.17*sinD(217.6-13.087*d)
}

// MoonEclipticJ2000 returns the Moon's geocentric position in AU, referred to the mean ecliptic and
// equinox of J2000 — the frame the solar-system scene is drawn in.
//
// The series behind it is referred to the ecliptic OF DATE, so the vector is carried through the
// equator of date and precessed back to J2000 rather than being used as if the two frames were the
// same. Over 1800–2050 that correction reaches three quarters of a degree, which is more than the
// series' own error and would show as the Moon's orbit slowly swinging away from Earth's.
func MoonEclipticJ2000(t time.Time) (x, y, z float64) {
	lon := moonEclipticLongitude(t)
	lat := MoonEclipticLatitudeDeg(t)
	r := MoonDistanceAU(t)

	raDate, decDate := eclipticToEquatorial(lon, lat, sunObliquity(t))
	ra, dec := PrecessToJ2000(raDate, decDate, t)

	// Equatorial J2000 unit vector, then back into the ecliptic frame.
	cd := cosD(dec)
	xe, ye, ze := r*cd*cosD(ra), r*cd*sinD(ra), r*sinD(dec)
	const obl = 23.43928
	return xe, ye*cosD(obl) + ze*sinD(obl), -ye*sinD(obl) + ze*cosD(obl)
}

// MoonAngularRadiusDeg returns the Moon's apparent angular radius at t, which follows directly from
// its distance and its physical radius.
func MoonAngularRadiusDeg(t time.Time) float64 {
	const moonRadiusKm = 1737.4
	distKm := MoonDistanceAU(t) * kmPerAU
	return math.Asin(moonRadiusKm/distKm) * rad2deg
}
