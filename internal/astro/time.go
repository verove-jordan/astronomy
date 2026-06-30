package astro

import "time"

// J2000 is the Julian Date of the J2000.0 epoch (2000-01-01 12:00 UT).
const J2000 = 2451545.0

// unixEpochJD is the Julian Date of the Unix epoch (1970-01-01 00:00 UT).
const unixEpochJD = 2440587.5

// nanosPerDay is the number of nanoseconds in one day.
const nanosPerDay = 8.64e13

// JulianDate returns the Julian Date for t, computed from the Unix epoch so it is exact and free of
// calendar-formula edge cases.
func JulianDate(t time.Time) float64 {
	return float64(t.UTC().UnixNano())/nanosPerDay + unixEpochJD
}

// JulianCenturies returns the number of Julian centuries elapsed since J2000.0 for the given JD.
func JulianCenturies(jd float64) float64 {
	return (jd - J2000) / 36525.0
}

// GMST returns the Greenwich Mean Sidereal Time at t, in degrees [0,360).
// Meeus, Astronomical Algorithms (2nd ed.), eq. 12.4.
func GMST(t time.Time) float64 {
	d := JulianDate(t) - J2000
	c := JulianCenturies(JulianDate(t))
	gmst := 280.46061837 + 360.98564736629*d + 0.000387933*c*c - c*c*c/38710000.0
	return norm360(gmst)
}

// LST returns the Local Mean Sidereal Time at t for an observer at the given east-positive
// longitude, in degrees [0,360).
func LST(t time.Time, lonDeg float64) float64 {
	return norm360(GMST(t) + lonDeg)
}
