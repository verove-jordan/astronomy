package skyevents

import (
	"time"

	"github.com/soniakeys/meeus/v3/apsis"
	"github.com/soniakeys/meeus/v3/moonphase"
	"github.com/soniakeys/meeus/v3/perihelion"
	"github.com/soniakeys/meeus/v3/solstice"
	"github.com/verove-jordan/astronomy/internal/astro"
)

// supermoonWindowMs is how close a full Moon must fall to lunar perigee to count as a "supermoon".
const supermoonWindowMs = 14 * 3600 * 1000

// moonEvents generates the lunar phases in the window and promotes perigee full Moons to supermoons.
func moonEvents(prm Params) []Event {
	var out []Event
	add := func(times []time.Time, sub string) {
		for _, t := range times {
			o := observeWith(func(tt time.Time) (float64, float64) { return astro.MoonPosition(tt) }, t, prm)
			ra, dec := astro.MoonPosition(t)
			ev := Event{
				Kind: "moon_phase", Subtype: sub,
				PeakUTCMs: t.UnixMilli(),
				Bodies:    []string{"moon"},
				RADeg:     ra, DecDeg: dec, HasPosition: true,
				Title:   titleCase(sub),
				Notable: sub == "full",
			}
			ev.applyObs(o)
			ev.MoonIllum = astro.MoonIllumination(t)
			ev.MoonSepDeg = 0 // the Moon is the object
			out = append(out, ev)
		}
	}
	add(phaseTimes(moonphase.New, prm), "new")
	add(phaseTimes(moonphase.First, prm), "first_quarter")
	add(phaseTimes(moonphase.Full, prm), "full")
	add(phaseTimes(moonphase.Last, prm), "last_quarter")
	markSupermoons(out, prm)
	return out
}

// phaseTimes enumerates a single lunar phase across the window. meeus returns the phase nearest a
// decimal year, so we step finely (~weekly) and dedupe by day.
func phaseTimes(fn func(float64) float64, prm Params) []time.Time {
	var out []time.Time
	seen := map[int64]bool{}
	y0, y1 := decimalYear(prm.From), decimalYear(prm.To)
	for y := y0 - 0.05; y <= y1+0.05; y += 0.02 {
		t := jdeToTime(fn(y))
		k := dayKey(t.UnixMilli())
		if seen[k] {
			continue
		}
		seen[k] = true
		if inRange(t, prm.From, prm.To) {
			out = append(out, t)
		}
	}
	return out
}

// markSupermoons promotes full-Moon events that fall near lunar perigee to "supermoon".
func markSupermoons(events []Event, prm Params) {
	perigees := perigeeTimes(prm)
	for i := range events {
		if events[i].Kind != "moon_phase" || events[i].Subtype != "full" {
			continue
		}
		for _, pt := range perigees {
			if abs64(pt-events[i].PeakUTCMs) < supermoonWindowMs {
				events[i].Kind = "supermoon"
				events[i].Title = "Supermoon"
				events[i].Notable = true
				break
			}
		}
	}
}

func perigeeTimes(prm Params) []int64 {
	var out []int64
	seen := map[int64]bool{}
	y0, y1 := decimalYear(prm.From), decimalYear(prm.To)
	for y := y0 - 0.05; y <= y1+0.05; y += 0.03 {
		t := jdeToTime(apsis.Perigee(y))
		k := dayKey(t.UnixMilli())
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, t.UnixMilli())
	}
	return out
}

// seasonEvents generates equinoxes, solstices and Earth perihelion/aphelion (calendar markers).
func seasonEvents(prm Params) []Event {
	var out []Event
	add := func(jde float64, kind, sub, title string) {
		t := jdeToTime(jde)
		if !inRange(t, prm.From, prm.To) {
			return
		}
		out = append(out, Event{Kind: kind, Subtype: sub, PeakUTCMs: t.UnixMilli(), Title: title})
	}
	for y := prm.From.Year(); y <= prm.To.Year(); y++ {
		add(solstice.March(y), "equinox", "march", "March equinox")
		add(solstice.June(y), "solstice", "june", "June solstice")
		add(solstice.September(y), "equinox", "september", "September equinox")
		add(solstice.December(y), "solstice", "december", "December solstice")
		add(perihelion.Perihelion(perihelion.Earth, float64(y)), "perihelion", "", "Earth at perihelion")
		add(perihelion.Aphelion(perihelion.Earth, float64(y)), "aphelion", "", "Earth at aphelion")
	}
	return out
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
