package skyevents

import (
	"fmt"
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"
)

// conjunction/appulse thresholds (degrees) — how close two bodies must come to count as an event.
const (
	planetPairThresholdDeg = 5.0
	planetMoonThresholdDeg = 4.0
	oppositionMinElongDeg  = 150.0
)

func planetEvents(prm Params) []Event {
	var out []Event
	out = append(out, conjunctionEvents(prm)...)
	out = append(out, oppositionElongationEvents(prm)...)
	out = append(out, planetMoonEvents(prm)...)
	return out
}

// conjunctionEvents finds close approaches between every pair of planets.
func conjunctionEvents(prm Params) []Event {
	var out []Event
	ps := astro.Planets
	for i := 0; i < len(ps); i++ {
		for j := i + 1; j < len(ps); j++ {
			a, b := ps[i], ps[j]
			sep := func(t time.Time) float64 {
				pa, pb := astro.PlanetPosition(a, t), astro.PlanetPosition(b, t)
				return astro.AngularSeparation(pa.RADeg, pa.DecDeg, pb.RADeg, pb.DecDeg)
			}
			for _, tm := range minimaTimes(sep, prm.From, prm.To, 12*time.Hour, planetPairThresholdDeg) {
				pa, pb := astro.PlanetPosition(a, tm), astro.PlanetPosition(b, tm)
				bright, faint := a, b
				if pb.Magnitude < pa.Magnitude {
					bright, faint = b, a
				}
				bp := astro.PlanetPosition(bright, tm)
				ev := Event{
					Kind:          "conjunction",
					PeakUTCMs:     tm.UnixMilli(),
					Bodies:        []string{bright.String(), faint.String()},
					SeparationDeg: sep(tm),
					Magnitude:     maxF(pa.Magnitude, pb.Magnitude), // must see the fainter of the two
					HasMag:        true,
					RADeg:         bp.RADeg, DecDeg: bp.DecDeg, HasPosition: true,
					Title:   fmt.Sprintf("%s–%s conjunction", titleCase(bright.String()), titleCase(faint.String())),
					Notable: true,
				}
				ev.applyObs(observeNight(bp.RADeg, bp.DecDeg, tm, prm))
				out = append(out, ev)
			}
		}
	}
	return out
}

// oppositionElongationEvents finds oppositions of the outer planets and greatest elongations of
// Mercury and Venus (both are the best apparitions to observe each planet).
func oppositionElongationEvents(prm Params) []Event {
	var out []Event
	for _, p := range astro.Planets {
		elong := func(t time.Time) float64 { return astro.PlanetPosition(p, t).ElongationDeg }
		if p == astro.Mercury || p == astro.Venus {
			for _, tm := range maximaTimes(elong, prm.From, prm.To, 12*time.Hour, 10) {
				st := astro.PlanetPosition(p, tm)
				sunRA, _ := astro.SunPosition(tm)
				side := "morning"
				if norm180(st.RADeg-sunRA) > 0 {
					side = "evening"
				}
				ev := Event{
					Kind: "elongation", Subtype: side,
					PeakUTCMs: tm.UnixMilli(),
					Bodies:    []string{p.String()},
					Magnitude: st.Magnitude, HasMag: true,
					RADeg: st.RADeg, DecDeg: st.DecDeg, HasPosition: true,
					Title:     fmt.Sprintf("%s at greatest elongation", titleCase(p.String())),
					ExtraText: fmt.Sprintf("%.0f° from the Sun (%s sky)", st.ElongationDeg, side),
					Notable:   true,
				}
				ev.applyObs(observeNight(st.RADeg, st.DecDeg, tm, prm))
				out = append(out, ev)
			}
			continue
		}
		for _, tm := range maximaTimes(elong, prm.From, prm.To, 24*time.Hour, oppositionMinElongDeg) {
			st := astro.PlanetPosition(p, tm)
			ev := Event{
				Kind:      "opposition",
				PeakUTCMs: tm.UnixMilli(),
				Bodies:    []string{p.String()},
				Magnitude: st.Magnitude, HasMag: true,
				RADeg: st.RADeg, DecDeg: st.DecDeg, HasPosition: true,
				Title:   fmt.Sprintf("%s at opposition", titleCase(p.String())),
				Notable: true,
			}
			ev.applyObs(observeNight(st.RADeg, st.DecDeg, tm, prm))
			out = append(out, ev)
		}
	}
	return out
}

// planetMoonEvents finds close approaches between each planet and the Moon.
func planetMoonEvents(prm Params) []Event {
	var out []Event
	for _, p := range astro.Planets {
		sep := func(t time.Time) float64 {
			pp := astro.PlanetPosition(p, t)
			mr, md := astro.MoonPosition(t)
			return astro.AngularSeparation(pp.RADeg, pp.DecDeg, mr, md)
		}
		for _, tm := range minimaTimes(sep, prm.From, prm.To, 6*time.Hour, planetMoonThresholdDeg) {
			st := astro.PlanetPosition(p, tm)
			ev := Event{
				Kind:          "planet_moon",
				PeakUTCMs:     tm.UnixMilli(),
				Bodies:        []string{p.String(), "moon"},
				SeparationDeg: sep(tm),
				Magnitude:     st.Magnitude, HasMag: true,
				RADeg: st.RADeg, DecDeg: st.DecDeg, HasPosition: true,
				Title:   fmt.Sprintf("Moon near %s", titleCase(p.String())),
				Notable: true,
			}
			ev.applyObs(observeNight(st.RADeg, st.DecDeg, tm, prm))
			out = append(out, ev)
		}
	}
	return out
}

// minimaTimes returns the times of local minima of f below `below`, sampling at `step` and refining each.
func minimaTimes(f func(time.Time) float64, from, to time.Time, step time.Duration, below float64) []time.Time {
	var out []time.Time
	var v0, v1 float64
	var t0, t1 time.Time
	n := 0
	for t := from; !t.After(to); t = t.Add(step) {
		v := f(t)
		if n >= 2 && v1 < v0 && v1 <= v && v1 < below {
			out = append(out, refineExtreme(f, t0, t, true))
		}
		v0, t0, v1, t1 = v1, t1, v, t
		_ = t1
		n++
	}
	return out
}

// maximaTimes returns the times of local maxima of f above `above`.
func maximaTimes(f func(time.Time) float64, from, to time.Time, step time.Duration, above float64) []time.Time {
	neg := func(t time.Time) float64 { return -f(t) }
	return minimaTimes(neg, from, to, step, -above)
}

// refineExtreme ternary-searches [a,b] for a min (or max) of f to ~1-minute precision.
func refineExtreme(f func(time.Time) float64, a, b time.Time, findMin bool) time.Time {
	for b.Sub(a) > time.Minute {
		third := b.Sub(a) / 3
		m1, m2 := a.Add(third), a.Add(2*third)
		f1, f2 := f(m1), f(m2)
		better := f1 < f2
		if !findMin {
			better = f1 > f2
		}
		if better {
			b = m2
		} else {
			a = m1
		}
	}
	return a.Add(b.Sub(a) / 2)
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// norm180 wraps an angle (deg) to (−180, 180].
func norm180(d float64) float64 {
	for d > 180 {
		d -= 360
	}
	for d <= -180 {
		d += 360
	}
	return d
}
