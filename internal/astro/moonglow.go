package astro

import (
	"math"
	"time"
)

// moonGlowStrength scales how much a fully lit, overhead Moon suppresses an hour's usefulness for
// faint-object work. It stays below 1 so a moonlit hour still counts for something: the Moon is the
// same everywhere inside one search area, so zeroing those hours would discard real information about
// which site is cloudier.
const moonGlowStrength = 0.8

// moonHorizonDeg is the apparent altitude at which the Moon's limb clears the horizon (semi-diameter
// plus refraction). It matches the value the night-chart sampler uses so rise/set times agree.
const moonHorizonDeg = 0.125

// moonSampleStep is the sampling interval used to bracket the Moon's horizon crossings. The Moon moves
// ~0.5°/h, so 5 minutes cannot miss a crossing.
const moonSampleStep = 5 * time.Minute

// MoonGlowFactor returns how usable a moment is for faint-object work given moonlight alone: 1 when the
// Moon is down or new, falling towards 1-moonGlowStrength for a full Moon near the zenith. It is the
// site-agnostic counterpart of the planner's per-target moon score — there is no target here, so the
// Moon-to-target separation term is dropped and only illumination and altitude remain.
func MoonGlowFactor(t time.Time, latDeg, lonDeg float64) float64 {
	alt := moonAltitude(t, latDeg, lonDeg)
	if alt <= moonHorizonDeg {
		return 1
	}
	glow := MoonIllumination(t) * clampUnit(math.Sin(alt*deg2rad))
	return clampUnit(1 - moonGlowStrength*glow)
}

// MoonUpHours returns how many hours of the window the Moon spends above the horizon — the part of the
// night that moonlight spoils. It walks the Moon's horizon crossings rather than assuming a fixed
// position, because the Moon moves far enough in one night to rise or set inside the window.
func MoonUpHours(w DarkWindow, latDeg, lonDeg float64) float64 {
	if !w.End.After(w.Start) {
		return 0
	}
	altFn := func(t time.Time) float64 { return moonAltitude(t, latDeg, lonDeg) }

	up := 0.0
	spanStart := w.Start
	moonUp := altFn(w.Start) > moonHorizonDeg
	for _, c := range AltitudeCrossings(altFn, w.Start, w.End, moonSampleStep, moonHorizonDeg) {
		if moonUp {
			up += c.Time.Sub(spanStart).Hours()
		}
		spanStart, moonUp = c.Time, c.Rising
	}
	if moonUp {
		up += w.End.Sub(spanStart).Hours()
	}
	return up
}

// moonAltitude returns the Moon's apparent (refraction-corrected) altitude in degrees.
func moonAltitude(t time.Time, latDeg, lonDeg float64) float64 {
	ra, dec := MoonPosition(t)
	alt, _ := Horizontal(ra, dec, latDeg, lonDeg, t)
	return ApparentAltitude(alt)
}

// clampUnit constrains x to [0,1].
func clampUnit(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}
