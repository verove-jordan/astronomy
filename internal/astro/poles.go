package astro

import (
	"math"
	"time"
)

// Pole-star J2000.0 positions (degrees). Polaris is α Ursae Minoris (the northern pole star); σ
// Octantis is the faint southern pole star used on southern polar-scope reticles.
const (
	PolarisRAJ2000   = 37.954
	PolarisDecJ2000  = 89.264
	SigmaOctRAJ2000  = 317.195
	SigmaOctDecJ2000 = -88.956
)

// PrecessFromJ2000 precesses equatorial coordinates from J2000.0 to the epoch of t using the IAU 1976
// precession angles (ζ, z, θ; Meeus, Astronomical Algorithms 2nd ed., eq. 21.4). The declination uses
// the atan2 form, so it stays accurate arbitrarily close to the pole — where the linear annual-
// precession formula (Δδ with n·sin α·tan δ) blows up. Inputs/outputs are degrees; RA is in [0,360).
func PrecessFromJ2000(raDeg, decDeg float64, t time.Time) (raOut, decOut float64) {
	tc := JulianCenturies(JulianDate(t))
	// IAU 1976 precession angles in arcseconds → degrees. The reference epoch is J2000, so the terms in
	// the starting epoch T0 vanish and only the powers of tc remain.
	zeta := (2306.2181*tc + 0.30188*tc*tc + 0.017998*tc*tc*tc) / 3600
	z := (2306.2181*tc + 1.09468*tc*tc + 0.018203*tc*tc*tc) / 3600
	theta := (2004.3109*tc - 0.42665*tc*tc - 0.041833*tc*tc*tc) / 3600

	a := cosD(decDeg) * sinD(raDeg+zeta)
	b := cosD(theta)*cosD(decDeg)*cosD(raDeg+zeta) - sinD(theta)*sinD(decDeg)
	c := sinD(theta)*cosD(decDeg)*cosD(raDeg+zeta) + cosD(theta)*sinD(decDeg)

	raOut = norm360(math.Atan2(a, b)*rad2deg + z)
	decOut = math.Atan2(c, math.Hypot(a, b)) * rad2deg
	return raOut, decOut
}

// PoleStar returns the precessed RA/Dec (degrees, epoch of t) and display name of the relevant pole
// star for the hemisphere: Polaris in the north, σ Octantis in the south.
func PoleStar(north bool, t time.Time) (raDeg, decDeg float64, name string) {
	if north {
		ra, dec := PrecessFromJ2000(PolarisRAJ2000, PolarisDecJ2000, t)
		return ra, dec, "Polaris"
	}
	ra, dec := PrecessFromJ2000(SigmaOctRAJ2000, SigmaOctDecJ2000, t)
	return ra, dec, "σ Octantis"
}
