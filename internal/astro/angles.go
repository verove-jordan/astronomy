// Package astro provides hand-rolled positional-astronomy primitives (sidereal time, horizontal
// coordinates, Sun/Moon positions, airmass, culmination and twilight) used to decide which deep-sky
// objects are worth imaging tonight. It depends only on the standard library (math, time) — the
// project pins an old Go toolchain and avoids heavy dependencies, and these low-precision formulae
// (Meeus / Astronomical Almanac) are accurate to a fraction of a degree, which is ample for planning.
package astro

import "math"

const (
	deg2rad = math.Pi / 180
	rad2deg = 180 / math.Pi
)

// Degree-based trig helpers keep the formulae below readable (most almanac formulae are in degrees).
func sinD(d float64) float64 { return math.Sin(d * deg2rad) }
func cosD(d float64) float64 { return math.Cos(d * deg2rad) }

// clamp1 constrains x to [-1,1] so asin/acos never see an out-of-domain argument from rounding.
func clamp1(x float64) float64 {
	if x > 1 {
		return 1
	}
	if x < -1 {
		return -1
	}
	return x
}

// norm360 wraps an angle into [0,360).
func norm360(d float64) float64 {
	d = math.Mod(d, 360)
	if d < 0 {
		d += 360
	}
	return d
}

// norm180 wraps an angle into (-180,180].
func norm180(d float64) float64 {
	d = norm360(d)
	if d > 180 {
		d -= 360
	}
	return d
}
