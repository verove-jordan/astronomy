package astro

import "time"

// Crossing is a moment a body's altitude crosses a horizon level, tagged by direction.
type Crossing struct {
	Time   time.Time
	Rising bool // true = ascending (a rise); false = descending (a set)
}

// AltitudeCrossings samples altFn over [start,end] at the given step and returns every crossing of
// horizonDeg, each refined to ~1s by bisection. altFn returns an altitude in degrees for a time. It is
// used for the Sun's set/rise (horizon ≈ −0.833°) and the Moon's rise/set within a night window.
func AltitudeCrossings(altFn func(time.Time) float64, start, end time.Time, step time.Duration, horizonDeg float64) []Crossing {
	var out []Crossing
	prevT := start
	prevAlt := altFn(prevT)
	for t := start.Add(step); !t.After(end); t = t.Add(step) {
		alt := altFn(t)
		if (prevAlt-horizonDeg <= 0) != (alt-horizonDeg <= 0) { // sign change brackets a crossing
			out = append(out, Crossing{Time: bisectAlt(altFn, prevT, t, horizonDeg), Rising: alt > prevAlt})
		}
		prevT, prevAlt = t, alt
	}
	return out
}

// bisectAlt refines the time at which altFn crosses horizonDeg within [lo,hi], which must bracket
// exactly one crossing.
func bisectAlt(altFn func(time.Time) float64, lo, hi time.Time, horizonDeg float64) time.Time {
	fLo := altFn(lo) - horizonDeg
	for i := 0; i < 25; i++ {
		mid := lo.Add(hi.Sub(lo) / 2)
		fMid := altFn(mid) - horizonDeg
		if (fLo <= 0) == (fMid <= 0) {
			lo, fLo = mid, fMid
		} else {
			hi = mid
		}
	}
	return lo.Add(hi.Sub(lo) / 2)
}
