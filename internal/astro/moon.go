package astro

import "time"

// MoonState captures the Moon's position and appearance for an observer at a moment.
type MoonState struct {
	RADeg, DecDeg float64
	AltDeg, AzDeg float64 // geometric altitude / compass azimuth (degrees)
	IllumFraction float64 // illuminated fraction, 0..1
	Up            bool    // apparent altitude above the horizon
}

// MoonPosition returns the Moon's geocentric equatorial coordinates (RA, Dec in degrees) at t, from
// the Astronomical Almanac low-precision series (accuracy ~0.3°, valid 1950–2050). Topocentric
// parallax (up to ~1°) is neglected — adequate for visibility scoring.
func MoonPosition(t time.Time) (raDeg, decDeg float64) {
	d := JulianDate(t) - J2000
	lat := 5.13*sinD(93.3+13.229350*d) +
		0.28*sinD(228.2+26.294*d) -
		0.28*sinD(318.3+0.980*d) -
		0.17*sinD(217.6-13.087*d)
	return eclipticToEquatorial(moonEclipticLongitude(t), lat, sunObliquity(t))
}

// moonEclipticLongitude returns the Moon's ecliptic longitude (degrees) at t.
func moonEclipticLongitude(t time.Time) float64 {
	d := JulianDate(t) - J2000
	return norm360(218.32 + 13.176396*d +
		6.29*sinD(134.9+13.064993*d) -
		1.27*sinD(259.2-13.003*d) +
		0.66*sinD(235.7+24.381*d) +
		0.21*sinD(269.9+26.130*d) -
		0.19*sinD(357.5+0.985600*d) -
		0.11*sinD(186.6+26.184*d))
}

// MoonIllumination returns the Moon's illuminated fraction (0..1) at t, from the Sun–Moon elongation
// ψ: k = (1 − cos ψ)/2 (the large Earth–Sun distance makes the phase angle ≈ 180° − ψ; ~1% accurate).
func MoonIllumination(t time.Time) float64 {
	sunRA, sunDec := SunPosition(t)
	moonRA, moonDec := MoonPosition(t)
	psi := AngularSeparation(sunRA, sunDec, moonRA, moonDec)
	return (1 - cosD(psi)) / 2
}

// MoonNow returns the Moon's full state for an observer at latDeg/lonDeg at t.
func MoonNow(t time.Time, latDeg, lonDeg float64) MoonState {
	ra, dec := MoonPosition(t)
	alt, az := Horizontal(ra, dec, latDeg, lonDeg, t)
	return MoonState{
		RADeg: ra, DecDeg: dec,
		AltDeg: alt, AzDeg: az,
		IllumFraction: MoonIllumination(t),
		Up:            ApparentAltitude(alt) > 0,
	}
}

// MoonPhaseAngle returns the Moon's phase angle (degrees): the Moon's ecliptic longitude minus the
// Sun's, in [0,360). 0 = new, 90 = first quarter, 180 = full, 270 = last quarter.
func MoonPhaseAngle(t time.Time) float64 {
	return norm360(moonEclipticLongitude(t) - sunEclipticLongitude(t))
}
