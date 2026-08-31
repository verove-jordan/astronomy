package eclipsegeom

import (
	"math"
	"time"
)

// band is a stretch of eclipse magnitude the recording covers on one side of maximum, with the
// instants its ends correspond to. Working in magnitude rather than in time is what lets the two
// sides be compared: the clock runs the same way on both, the phase does not.
type band struct {
	lo, hi     float64
	loAt, hiAt time.Time
}

// sides turns the covered wall-clock spans into the magnitude bands available before and after
// maximum. A span that straddles maximum is split, because a single band running through the peak
// would claim phases on the far side that the ingress half cannot supply.
func sides(spans []Span, s Site, max time.Time) (in, eg []band) {
	first, last, ok := Contacts(max, s)
	if !ok {
		return nil, nil
	}
	for _, sp := range spans {
		if b, ok := bandFor(sp, first, max, true, s); ok {
			in = append(in, b)
		}
		if b, ok := bandFor(sp, max, last, false, s); ok {
			eg = append(eg, b)
		}
	}
	return in, eg
}

// bandFor clips one span to one side of maximum and measures the magnitudes at its ends.
func bandFor(sp Span, from, to time.Time, ingress bool, s Site) (band, bool) {
	a, b := latest(sp.From, from), earliest(sp.To, to)
	if !a.Before(b) {
		return band{}, false
	}
	ma, mb := At(a, s).Magnitude(), At(b, s).Magnitude()
	if ingress {
		return band{lo: ma, hi: mb, loAt: a, hiAt: b}, mb > ma
	}
	// Past maximum the magnitude falls, so the span's later end is the band's lower edge.
	return band{lo: mb, hi: ma, loAt: b, hiAt: a}, ma > mb
}

// nearest returns the magnitude on this side closest to m, and the instant it happens.
//
// Inside a band the answer is exact — the phase is solved for, not snapped to a frame — because the
// bands are continuous stretches of recording and any instant inside one has frames. Outside every
// band the nearest edge is returned, and it is the caller's business to decide whether that is close
// enough to still call a pair.
func nearest(bands []band, m float64, ingress bool, s Site, max time.Time) (float64, time.Time, bool) {
	if len(bands) == 0 {
		return 0, time.Time{}, false
	}
	for _, b := range bands {
		if m >= b.lo && m <= b.hi {
			at, ok := TimeAtMagnitude(m, ingress, s, max)
			if !ok {
				continue
			}
			return m, at, true
		}
	}
	bestMag, bestAt, bestDist := 0.0, time.Time{}, math.Inf(1)
	for _, b := range bands {
		for _, e := range [...]struct {
			mag float64
			at  time.Time
		}{{b.lo, b.loAt}, {b.hi, b.hiAt}} {
			if d := math.Abs(e.mag - m); d < bestDist {
				bestMag, bestAt, bestDist = e.mag, e.at, d
			}
		}
	}
	return bestMag, bestAt, !bestAt.IsZero()
}

func latest(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func earliest(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
