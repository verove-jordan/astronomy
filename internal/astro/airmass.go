package astro

import "math"

// Airmass returns the relative optical airmass for an apparent altitude (degrees), using the
// Kasten & Young (1989) formula. It returns +Inf below the horizon. Reference values: X(90°)=1.0,
// X(30°)≈1.995, X(10°)≈5.6, X(0°)≈37.9.
func Airmass(apparentAltDeg float64) float64 {
	if apparentAltDeg < 0 {
		return math.Inf(1)
	}
	return 1 / (sinD(apparentAltDeg) + 0.50572*math.Pow(apparentAltDeg+6.07995, -1.6364))
}
