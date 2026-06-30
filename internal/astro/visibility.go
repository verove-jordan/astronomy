package astro

import (
	"math"
	"time"
)

// siderealDegPerHour is the Earth's sidereal rotation rate in degrees of hour angle per solar hour.
const siderealDegPerHour = 360.98564736629 / 24 // ≈ 15.0410686

// siderealDayHours is one sidereal day expressed in solar hours (var, not const, so the Duration
// conversion below is a runtime — not a forbidden non-integer constant — conversion).
var siderealDayHours = 360.0 / 360.98564736629 * 24

// siderealDay is the length of one sidereal day in solar time.
var siderealDay = time.Duration(siderealDayHours * float64(time.Hour))

// AltCrossing classifies whether an object reaches a given altitude during its diurnal circle.
type AltCrossing int

const (
	// Crosses: the object rises above and sets below the target altitude during the day.
	Crosses AltCrossing = iota
	// AlwaysAbove: the object never drops below the target altitude (circumpolar above it).
	AlwaysAbove
	// AlwaysBelow: the object never rises to the target altitude.
	AlwaysBelow
)

// TransitAltitude returns the altitude (degrees) of an object at upper culmination for an observer at
// latDeg: 90 − |lat − dec|.
func TransitAltitude(latDeg, decDeg float64) float64 {
	return 90 - math.Abs(latDeg-decDeg)
}

// HourAngleForAltitude returns the hour-angle magnitude (degrees, ≥0) at which an object at decDeg,
// seen from latDeg, has altitude altDeg, together with a classification. When status==Crosses the
// object is above altDeg for hour angles in [−ha, +ha].
func HourAngleForAltitude(altDeg, latDeg, decDeg float64) (haDeg float64, status AltCrossing) {
	lat := latDeg * deg2rad
	dec := decDeg * deg2rad
	denom := math.Cos(lat) * math.Cos(dec)
	if math.Abs(denom) < 1e-12 { // observer at a pole, or object at a celestial pole: altitude is constant
		if math.Sin(lat)*math.Sin(dec) >= sinD(altDeg) {
			return 0, AlwaysAbove
		}
		return 0, AlwaysBelow
	}
	cosH := (sinD(altDeg) - math.Sin(lat)*math.Sin(dec)) / denom
	if cosH < -1 {
		return 0, AlwaysAbove
	}
	if cosH > 1 {
		return 0, AlwaysBelow
	}
	return math.Acos(cosH) * rad2deg, Crosses
}

// TransitTimeUTC returns the time of upper culmination (hour angle 0) nearest to ref for an object at
// raDeg seen from east-positive lonDeg.
func TransitTimeUTC(raDeg, lonDeg float64, ref time.Time) time.Time {
	ha := HourAngleDeg(raDeg, lonDeg, ref) // (-180,180], positive = past transit
	dtHours := -ha / siderealDegPerHour    // nearest transit within ±12h
	return ref.Add(time.Duration(dtHours * float64(time.Hour)))
}

// MaxAltitudeInWindow returns the maximum GEOMETRIC altitude (degrees) an object at raDeg/decDeg
// reaches for an observer at latDeg/lonDeg during [w.Start, w.End]. Altitude is unimodal between
// culminations, so the maximum is at the transit (when it falls inside the window) or at an endpoint.
func MaxAltitudeInWindow(raDeg, decDeg, latDeg, lonDeg float64, w DarkWindow) float64 {
	mid := w.Start.Add(w.End.Sub(w.Start) / 2)
	transit := TransitTimeUTC(raDeg, lonDeg, mid)
	cands := []time.Time{w.Start, w.End}
	if !transit.Before(w.Start) && !transit.After(w.End) {
		cands = append(cands, transit)
	}
	max := math.Inf(-1)
	for _, t := range cands {
		alt, _ := Horizontal(raDeg, decDeg, latDeg, lonDeg, t)
		if alt > max {
			max = alt
		}
	}
	return max
}

// HoursAboveAltInWindow returns how many hours an object at raDeg/decDeg spends above altDeg for an
// observer at latDeg/lonDeg within [w.Start, w.End]. The object is above altDeg for an interval of
// ±(ha/rate) around each transit; the adjacent-day transits are included so a window straddling
// midnight is fully covered.
func HoursAboveAltInWindow(raDeg, decDeg, altDeg, latDeg, lonDeg float64, w DarkWindow) float64 {
	ha, status := HourAngleForAltitude(altDeg, latDeg, decDeg)
	switch status {
	case AlwaysBelow:
		return 0
	case AlwaysAbove:
		return w.Hours()
	}
	half := time.Duration(ha / siderealDegPerHour * float64(time.Hour))
	mid := w.Start.Add(w.End.Sub(w.Start) / 2)
	base := TransitTimeUTC(raDeg, lonDeg, mid)
	var total time.Duration
	for k := -1; k <= 1; k++ {
		c := base.Add(time.Duration(k) * siderealDay)
		total += overlap(c.Add(-half), c.Add(half), w.Start, w.End)
	}
	return total.Hours()
}

// overlap returns the duration of the intersection of intervals [aStart,aEnd] and [bStart,bEnd].
func overlap(aStart, aEnd, bStart, bEnd time.Time) time.Duration {
	start := aStart
	if bStart.After(start) {
		start = bStart
	}
	end := aEnd
	if bEnd.Before(end) {
		end = bEnd
	}
	if end.After(start) {
		return end.Sub(start)
	}
	return 0
}
