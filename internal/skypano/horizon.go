package skypano

// horizon.go adds the frame the arch panorama is drawn in: azimuth and altitude, as the sky stood
// over the site at ONE chosen instant.
//
// This is what turns a sky mosaic into a picture of a night. The panels were shot over two hours, so
// the sky rotated about 30 degrees underneath them; each is solved in equatorial coordinates, where
// that rotation does not appear because equatorial coordinates turn with the sky. Ask instead where
// everything was RELATIVE TO THE GROUND and the question is ill-posed — it has a different answer for
// every panel. Naming one instant makes it well-posed again: the arch is drawn as it stood at, say,
// half past two, and every panel is placed where its stars were at that moment. Stars shot later are
// therefore drawn where they had been earlier, which is the only self-consistent choice and is what a
// single wide exposure would have recorded.
//
// The conversion is written as explicit spherical trigonometry rather than with cross products. Both
// work, but a cross-product basis silently encodes a handedness, and getting it backwards mirrors the
// whole panorama east for west — a mistake that looks entirely plausible in the result. The formulae
// below can be checked against cases with known answers instead, and are.

import "math"

const deg = math.Pi / 180

// horizonToEquatorial converts a direction given as azimuth and altitude, for an observer at latitude
// latDeg when the local sidereal time is lstDeg, into an equatorial unit vector.
//
// Azimuth is measured from due north through due east, which is the convention the phone's compass
// reports in and therefore the one the panels arrive in.
func horizonToEquatorial(azDeg, altDeg, latDeg, lstDeg float64) [3]float64 {
	a, h, phi := azDeg*deg, altDeg*deg, latDeg*deg
	sinDec := math.Sin(h)*math.Sin(phi) + math.Cos(h)*math.Cos(phi)*math.Cos(a)
	sinDec = clamp1(sinDec)
	dec := math.Asin(sinDec)
	// The hour angle, from its sine and cosine so the quadrant is never ambiguous.
	y := -math.Cos(h) * math.Sin(a)
	x := math.Sin(h)*math.Cos(phi) - math.Cos(h)*math.Sin(phi)*math.Cos(a)
	ha := math.Atan2(y, x)
	ra := lstDeg - ha/deg
	return lonLatToVec(ra, dec/deg)
}

// equatorialToHorizon is the inverse: where an equatorial direction stood over the site at that
// instant, in degrees, with azimuth in [0, 360).
func equatorialToHorizon(v [3]float64, latDeg, lstDeg float64) (azDeg, altDeg float64) {
	ra, dec := vecToLonLat(v)
	ha := (lstDeg - ra) * deg
	d, phi := dec*deg, latDeg*deg
	sinAlt := math.Sin(d)*math.Sin(phi) + math.Cos(d)*math.Cos(phi)*math.Cos(ha)
	alt := math.Asin(clamp1(sinAlt))
	y := -math.Cos(d) * math.Sin(ha)
	x := math.Sin(d)*math.Cos(phi) - math.Cos(d)*math.Sin(phi)*math.Cos(ha)
	az := math.Atan2(y, x) / deg
	if az < 0 {
		az += 360
	}
	return az, alt / deg
}
