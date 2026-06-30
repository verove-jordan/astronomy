package astro

import "time"

// DarkWindow is the contiguous span of darkness around a solar midnight.
type DarkWindow struct {
	Start, End  time.Time // UTC; Start < End
	Kind        string    // "astronomical" | "nautical" | "civil" | "best_effort"
	NoAstroDark bool      // true when the requested twilight depth was not reached (fell back to a shallower one)
}

// Hours returns the length of the window in hours.
func (w DarkWindow) Hours() float64 { return w.End.Sub(w.Start).Hours() }

// twilightDepths maps a darkness kind to the sun-below-horizon angle (negative degrees), deepest first.
var twilightDepths = []struct {
	kind string
	deg  float64
}{
	{"astronomical", -18},
	{"nautical", -12},
	{"civil", -6},
}

// NightWindow finds the dark window for the night bracketing `at` for an observer at latDeg/lonDeg.
// It tries the requested twilight depth first (sunBelowDeg, e.g. -18 or -12) and falls back to
// shallower depths at high latitudes where deeper darkness never occurs. If even civil twilight is
// never reached (polar day), it returns a best-effort window of ±2h around solar midnight.
func NightWindow(at time.Time, latDeg, lonDeg, sunBelowDeg float64) DarkWindow {
	midnight := SolarMidnight(at, latDeg, lonDeg)
	// If we are already past this night's dawn, roll forward to the next solar midnight.
	if w, ok := darkSpan(midnight, latDeg, lonDeg, sunBelowDeg); ok && at.After(w.End) {
		midnight = SolarMidnight(midnight.Add(24*time.Hour), latDeg, lonDeg)
	}
	for _, td := range twilightDepths {
		if td.deg < sunBelowDeg { // deeper than requested → skip
			continue
		}
		if w, ok := darkSpan(midnight, latDeg, lonDeg, td.deg); ok {
			w.Kind = td.kind
			w.NoAstroDark = td.deg != sunBelowDeg
			return w
		}
	}
	return DarkWindow{
		Start:       midnight.Add(-2 * time.Hour),
		End:         midnight.Add(2 * time.Hour),
		Kind:        "best_effort",
		NoAstroDark: true,
	}
}

// SolarMidnight returns the Sun's lower culmination (anti-transit) nearest to `at`.
func SolarMidnight(at time.Time, latDeg, lonDeg float64) time.Time {
	ra, _ := SunPosition(at)
	ha := HourAngleDeg(ra, lonDeg, at) // (-180,180]
	d := norm180(ha - 180)             // signed degrees from anti-transit
	dtHours := -d / siderealDegPerHour
	return at.Add(time.Duration(dtHours * float64(time.Hour)))
}

// darkSpan finds the contiguous span around `midnight` during which the Sun is below `threshold`
// (negative degrees), by sampling ±12h at 5-minute steps and refining each crossing by bisection.
// ok is false when no proper dusk/dawn bracketing midnight is found.
func darkSpan(midnight time.Time, latDeg, lonDeg, threshold float64) (DarkWindow, bool) {
	const step = 5 * time.Minute
	start := midnight.Add(-12 * time.Hour)
	end := midnight.Add(12 * time.Hour)

	var dusk, dawn time.Time
	haveDusk, haveDawn := false, false
	prevT := start
	prevAlt := SunAltitude(prevT, latDeg, lonDeg)
	for t := start.Add(step); !t.After(end); t = t.Add(step) {
		alt := SunAltitude(t, latDeg, lonDeg)
		if prevAlt > threshold && alt <= threshold && !t.After(midnight) { // descending → dusk (latest before midnight)
			dusk = bisectCrossing(prevT, t, threshold, latDeg, lonDeg)
			haveDusk = true
		}
		if prevAlt <= threshold && alt > threshold && t.After(midnight) && !haveDawn { // ascending → dawn (earliest after)
			dawn = bisectCrossing(prevT, t, threshold, latDeg, lonDeg)
			haveDawn = true
		}
		prevT, prevAlt = t, alt
	}

	if haveDusk && haveDawn && dusk.Before(dawn) {
		return DarkWindow{Start: dusk, End: dawn}, true
	}
	if !haveDusk && !haveDawn && SunAltitude(midnight, latDeg, lonDeg) <= threshold { // continuously dark (polar night)
		return DarkWindow{Start: start, End: end}, true
	}
	return DarkWindow{}, false
}

// bisectCrossing refines the time at which the Sun's altitude crosses `threshold` within [lo,hi],
// which must bracket exactly one crossing.
func bisectCrossing(lo, hi time.Time, threshold, latDeg, lonDeg float64) time.Time {
	fLo := SunAltitude(lo, latDeg, lonDeg) - threshold
	for i := 0; i < 25; i++ {
		mid := lo.Add(hi.Sub(lo) / 2)
		fMid := SunAltitude(mid, latDeg, lonDeg) - threshold
		if (fLo <= 0) == (fMid <= 0) {
			lo, fLo = mid, fMid
		} else {
			hi = mid
		}
	}
	return lo.Add(hi.Sub(lo) / 2)
}
