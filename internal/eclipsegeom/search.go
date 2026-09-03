package eclipsegeom

import (
	"math"
	"time"
)

// coarseStep is the grid the unimodal search starts on. The obscuration curve has one hump per
// eclipse and changes by at most a few tenths of a percent per second, so a minute is fine enough
// to bracket the peak and cheap enough to sweep a whole afternoon.
const coarseStep = time.Minute

// Maximum finds the instant of greatest obscuration inside [t0, t1] and its circumstances.
//
// It scans on a coarse grid first rather than going straight to a golden-section search, because
// away from the eclipse the curve is FLAT AT ZERO — the two discs simply do not touch — and every
// interval-shrinking method needs a bracket where the middle beats both ends before it can start.
func Maximum(t0, t1 time.Time, s Site) (time.Time, Circumstance) {
	best, bestC := t0, At(t0, s)
	for t := t0; !t.After(t1); t = t.Add(coarseStep) {
		if c := At(t, s); c.Obscuration > bestC.Obscuration {
			best, bestC = t, c
		}
	}
	if bestC.Obscuration <= 0 {
		return best, bestC
	}
	return refineMax(best.Add(-coarseStep), best.Add(coarseStep), s)
}

// refineMax golden-sections the peak down to a second.
func refineMax(lo, hi time.Time, s Site) (time.Time, Circumstance) {
	const phi = 0.6180339887
	span := hi.Sub(lo)
	a, b := lo.Add(time.Duration(float64(span)*(1-phi))), lo.Add(time.Duration(float64(span)*phi))
	fa, fb := At(a, s).Obscuration, At(b, s).Obscuration
	for hi.Sub(lo) > time.Second {
		if fa < fb {
			lo, a, fa = a, b, fb
			b = lo.Add(time.Duration(float64(hi.Sub(lo)) * phi))
			fb = At(b, s).Obscuration
			continue
		}
		hi, b, fb = b, a, fa
		a = lo.Add(time.Duration(float64(hi.Sub(lo)) * (1 - phi)))
		fa = At(a, s).Obscuration
	}
	mid := lo.Add(hi.Sub(lo) / 2)
	return mid, At(mid, s)
}

// Contacts returns first and last contact around a known maximum — the instants the discs start and
// stop touching. It walks outward a minute at a time until the obscuration falls to zero, then
// bisects. searchSpan bounds the walk: a partial eclipse runs a little over an hour each side, and
// three hours is comfortably past any of them.
func Contacts(max time.Time, s Site) (first, last time.Time, ok bool) {
	if At(max, s).Obscuration <= 0 {
		return time.Time{}, time.Time{}, false
	}
	const searchSpan = 3 * time.Hour
	first, ok = edge(max, -coarseStep, searchSpan, s)
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	last, ok = edge(max, coarseStep, searchSpan, s)
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	return first, last, true
}

// edge walks away from an eclipsed instant in the given direction until the discs part, then
// bisects the crossing to the second.
func edge(from time.Time, step, limit time.Duration, s Site) (time.Time, bool) {
	inside := from
	for elapsed := time.Duration(0); elapsed < limit; elapsed += absDur(step) {
		next := inside.Add(step)
		if At(next, s).Obscuration <= 0 {
			return bisect(inside, next, s, func(o float64) bool { return o > 0 }), true
		}
		inside = next
	}
	return time.Time{}, false
}

// TimeAtObscuration returns when the obscuration first reaches f on the requested side of maximum.
//
// The obscuration is strictly monotone from each contact to maximum, which is what makes a plain
// bisection valid here and is the reason a sequence indexes phases by TIME rather than by the
// measured crescent: one number, one instant, no ambiguity about which half of the eclipse it
// belongs to.
func TimeAtObscuration(f float64, ingress bool, s Site, max time.Time) (time.Time, bool) {
	peak := At(max, s)
	if f <= 0 || f > peak.Obscuration {
		return time.Time{}, false
	}
	first, last, ok := Contacts(max, s)
	if !ok {
		return time.Time{}, false
	}
	// Bisect outward FROM maximum, which is the end that always satisfies the predicate — walking
	// in from a contact does not, since the obscuration there is zero.
	contact := first
	if !ingress {
		contact = last
	}
	return bisect(max, contact, s, func(o float64) bool { return o >= f }), true
}

// bisect narrows [holds, fails] — the first argument satisfies the predicate, the second does not —
// down to a second and returns the last instant that still satisfies it.
func bisect(holds, fails time.Time, s Site, pred func(float64) bool) time.Time {
	for absDur(fails.Sub(holds)) > time.Second {
		mid := holds.Add(fails.Sub(holds) / 2)
		if pred(At(mid, s).Obscuration) {
			holds = mid
			continue
		}
		fails = mid
	}
	return holds
}

func absDur(d time.Duration) time.Duration {
	return time.Duration(math.Abs(float64(d)))
}

// TimeAtMagnitude is TimeAtObscuration in the coordinate a sequence actually spaces panels by.
func TimeAtMagnitude(m float64, ingress bool, s Site, max time.Time) (time.Time, bool) {
	if m <= 0 || m > At(max, s).Magnitude() {
		return time.Time{}, false
	}
	first, last, ok := Contacts(max, s)
	if !ok {
		return time.Time{}, false
	}
	contact := first
	if !ingress {
		contact = last
	}
	return bisectBy(max, contact, s, func(c Circumstance) bool { return c.Magnitude() >= m }), true
}

// bisectBy is bisect over the whole Circumstance rather than the obscuration alone.
func bisectBy(holds, fails time.Time, s Site, pred func(Circumstance) bool) time.Time {
	for absDur(fails.Sub(holds)) > time.Second {
		mid := holds.Add(fails.Sub(holds) / 2)
		if pred(At(mid, s)) {
			holds = mid
			continue
		}
		fails = mid
	}
	return holds
}
