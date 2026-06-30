package skyevents

import (
	"time"

	"github.com/soniakeys/meeus/v3/deltat"
	"github.com/soniakeys/meeus/v3/julian"
)

// jdeToTime converts a Julian Ephemeris Day (Dynamical Time, as meeus returns) to civil UTC, applying
// ΔT so eclipse/phase/season instants land on the correct clock time (ΔT ≈ 69 s in this era).
func jdeToTime(jde float64) time.Time {
	dt := deltat.Interp10A(jde) // ΔT as unit.Time (seconds)
	return julian.JDToTime(jde - dt.Sec()/86400.0).UTC()
}

// timeToJDE converts a civil UTC time to a Julian Ephemeris Day (adds ΔT), for feeding meeus routines
// that take a JDE (e.g. topocentric parallax).
func timeToJDE(t time.Time) float64 {
	jd := julian.TimeToJD(t.UTC())
	return jd + deltat.Interp10A(jd).Sec()/86400.0
}

// decimalYear returns the fractional year of t (e.g. 2026.4521…), the argument meeus' nearest-event
// search functions take.
func decimalYear(t time.Time) float64 {
	t = t.UTC()
	yStart := time.Date(t.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	yEnd := time.Date(t.Year()+1, 1, 1, 0, 0, 0, 0, time.UTC)
	return float64(t.Year()) + t.Sub(yStart).Seconds()/yEnd.Sub(yStart).Seconds()
}

// inRange reports whether t is within [from, to].
func inRange(t, from, to time.Time) bool {
	return !t.Before(from) && !t.After(to)
}

// dayKey buckets a UTC ms to an integer day, for deduping repeated nearest-event hits.
func dayKey(utcMs int64) int64 { return utcMs / 86_400_000 }
