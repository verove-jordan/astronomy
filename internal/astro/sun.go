package astro

import "time"

// SunPosition returns the Sun's apparent equatorial coordinates (RA, Dec in degrees) at t, using the
// low-precision formulae from the Astronomical Almanac (accuracy ~0.01°, valid 1950–2050).
func SunPosition(t time.Time) (raDeg, decDeg float64) {
	obl := sunObliquity(t)
	return eclipticToEquatorial(sunEclipticLongitude(t), 0, obl)
}

// SunAltitude returns the Sun's geometric altitude (degrees) for an observer at latDeg/lonDeg at t.
// Used for twilight, where refraction at twilight depths is irrelevant.
func SunAltitude(t time.Time, latDeg, lonDeg float64) float64 {
	ra, dec := SunPosition(t)
	alt, _ := Horizontal(ra, dec, latDeg, lonDeg, t)
	return alt
}

// sunEclipticLongitude returns the Sun's apparent ecliptic longitude (degrees) at t.
func sunEclipticLongitude(t time.Time) float64 {
	n := JulianDate(t) - J2000
	meanLon := norm360(280.460 + 0.9856474*n)
	meanAnom := norm360(357.528 + 0.9856003*n)
	return norm360(meanLon + 1.915*sinD(meanAnom) + 0.020*sinD(2*meanAnom))
}

// sunObliquity returns the obliquity of the ecliptic (degrees) at t.
func sunObliquity(t time.Time) float64 {
	return 23.439 - 0.0000004*(JulianDate(t)-J2000)
}
