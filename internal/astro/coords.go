package astro

import (
	"math"
	"time"
)

// HourAngleDeg returns the local hour angle (degrees, normalized to (-180,180], positive west) of an
// object at right ascension raDeg, for an observer at east-positive lonDeg at time t.
func HourAngleDeg(raDeg, lonDeg float64, t time.Time) float64 {
	return norm180(LST(t, lonDeg) - raDeg)
}

// Horizontal converts equatorial coordinates (RA/Dec, degrees) to GEOMETRIC horizontal coordinates
// (altitude, azimuth in degrees) for an observer at latDeg/lonDeg at time t. Azimuth is the compass
// convention: measured from North, increasing eastward (N=0, E=90, S=180, W=270). The transform uses
// the tan-free atan2 form so it is stable at the poles. Atmospheric refraction is not applied here —
// callers that need apparent altitude use ApparentAltitude.
func Horizontal(raDeg, decDeg, latDeg, lonDeg float64, t time.Time) (altDeg, azDeg float64) {
	h := HourAngleDeg(raDeg, lonDeg, t) * deg2rad
	lat := latDeg * deg2rad
	dec := decDeg * deg2rad

	sinAlt := math.Sin(lat)*math.Sin(dec) + math.Cos(lat)*math.Cos(dec)*math.Cos(h)
	alt := math.Asin(clamp1(sinAlt)) * rad2deg

	// Azimuth from South (positive toward West), then rotated to from-North/east-positive.
	y := math.Sin(h) * math.Cos(dec)
	x := math.Cos(h)*math.Sin(lat)*math.Cos(dec) - math.Sin(dec)*math.Cos(lat)
	az := norm360(math.Atan2(y, x)*rad2deg + 180)
	return alt, az
}

// Equatorial is the exact inverse of Horizontal: it converts geometric horizontal coordinates back
// to equatorial RA/Dec (degrees, RA in [0,360)) for an observer at latDeg/lonDeg at time t. Azimuth
// follows the same compass convention (N=0, increasing eastward), and refraction is likewise not
// applied. This is the transform that turns a phone's compass-and-gravity reading into a place on
// the sky, which is what lets a hand-framed session be grouped and placed without plate solving.
func Equatorial(altDeg, azDeg, latDeg, lonDeg float64, t time.Time) (raDeg, decDeg float64) {
	alt := altDeg * deg2rad
	az := azDeg * deg2rad
	lat := latDeg * deg2rad

	sinDec := math.Sin(alt)*math.Sin(lat) + math.Cos(alt)*math.Cos(lat)*math.Cos(az)
	dec := math.Asin(clamp1(sinDec))

	// Hour angle in the same tan-free atan2 form Horizontal uses (both sides carry a common cos(dec)
	// factor, which atan2 ignores), so the pair round-trips exactly and stays stable at the poles.
	y := -math.Sin(az) * math.Cos(alt)
	x := math.Sin(alt)*math.Cos(lat) - math.Sin(lat)*math.Cos(alt)*math.Cos(az)
	ha := math.Atan2(y, x) * rad2deg
	return norm360(LST(t, lonDeg) - ha), dec * rad2deg
}

// AngularSeparation returns the great-circle angle (degrees) between two equatorial positions, using
// the haversine form, which stays accurate for the small separations that matter for target–Moon
// distance (plain acos loses precision there).
func AngularSeparation(ra1, dec1, ra2, dec2 float64) float64 {
	dRA := (ra2 - ra1) * deg2rad
	dDec := (dec2 - dec1) * deg2rad
	a := math.Sin(dDec/2)*math.Sin(dDec/2) +
		math.Cos(dec1*deg2rad)*math.Cos(dec2*deg2rad)*math.Sin(dRA/2)*math.Sin(dRA/2)
	return 2 * math.Asin(clamp1(math.Sqrt(a))) * rad2deg
}

// Refraction returns the atmospheric refraction (arcminutes) to ADD to a geometric (true) altitude
// to obtain the apparent altitude. Saemundsson's formula (true→apparent). Returns 0 well below the
// horizon, where the model is not meaningful.
func Refraction(trueAltDeg float64) float64 {
	if trueAltDeg < -1 {
		return 0
	}
	return 1.02 / math.Tan((trueAltDeg+10.3/(trueAltDeg+5.11))*deg2rad)
}

// ApparentAltitude applies refraction to a geometric altitude (degrees).
func ApparentAltitude(trueAltDeg float64) float64 {
	return trueAltDeg + Refraction(trueAltDeg)/60
}

// eclipticToEquatorial rotates ecliptic coordinates (lon, lat) by obliquity obl (all degrees) to
// equatorial RA/Dec (degrees; RA in [0,360)).
func eclipticToEquatorial(lonDeg, latDeg, oblDeg float64) (raDeg, decDeg float64) {
	lon := lonDeg * deg2rad
	lat := latDeg * deg2rad
	obl := oblDeg * deg2rad
	x := math.Cos(lat) * math.Cos(lon)
	y := math.Cos(obl)*math.Cos(lat)*math.Sin(lon) - math.Sin(obl)*math.Sin(lat)
	z := math.Sin(obl)*math.Cos(lat)*math.Sin(lon) + math.Cos(obl)*math.Sin(lat)
	return norm360(math.Atan2(y, x) * rad2deg), math.Asin(clamp1(z)) * rad2deg
}
