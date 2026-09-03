package skyplan

import (
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"
)

const (
	nightSeriesStep = 10 * time.Minute
	nightPad        = 30 * time.Minute
	sunHorizonDeg   = -0.833 // standard sunset/sunrise (refraction + solar semidiameter)
	moonHorizonDeg  = 0.125  // approximate moon rise/set
)

// nightCtx is the night-global chart context: the time window plotted plus the Sun/Moon curves and
// their rise/set events.
type nightCtx struct {
	start, end time.Time
	sunSeries  []AltSample
	moonSeries []AltSample

	sunSet, sunRise   time.Time
	hasSunSet         bool
	hasSunRise        bool
	moonRise, moonSet time.Time
	hasMoonRise       bool
	hasMoonSet        bool
}

// computeNight builds the chart window (padded sunset→sunrise, falling back to the dark window when the
// sun never crosses the horizon) and samples the Sun/Moon altitude curves and rise/set times.
func computeNight(prm Params, dark astro.DarkWindow) nightCtx {
	lat, lon := prm.Lat, prm.Lon
	sunAlt := func(t time.Time) float64 {
		ra, dec := astro.SunPosition(t)
		a, _ := astro.Horizontal(ra, dec, lat, lon, t)
		return a
	}
	moonAlt := func(t time.Time) float64 {
		ra, dec := astro.MoonPosition(t)
		a, _ := astro.Horizontal(ra, dec, lat, lon, t)
		return a
	}

	// Anchor on the DARK WINDOW's own midnight, not on a fresh SolarMidnight(prm.At).
	//
	// The two disagree for half of every day, and the disagreement is not subtle.
	// astro.SolarMidnight returns the anti-transit NEAREST the instant it is handed, so any time
	// between roughly dawn and mid-afternoon it returns LAST night's midnight — at noon on the 3rd it
	// answers 00:00 on the 3rd, which belongs to the night of the 2nd. astro.NightWindow already
	// handles exactly that (it rolls forward when the instant is past the window's dawn), and its
	// result is passed in here; recomputing threw that work away.
	//
	// The visible fault: at midday the chart window, the weather panel's hour filter and the
	// "night of" badge were all framed on the night that ended THIS MORNING, while the best-clear-
	// window came from astro.NightWindow and described the night ahead. The panel read "Nuit du
	// 02/09" at noon on the 3rd, over hours from a night nobody can observe any more, with a
	// degenerate best window because the two halves disagreed about which night was being described.
	midnight := astro.SolarMidnight(dark.Start.Add(dark.End.Sub(dark.Start)/2), lat, lon)
	var nc nightCtx
	for _, c := range astro.AltitudeCrossings(sunAlt, midnight.Add(-12*time.Hour), midnight.Add(12*time.Hour), 5*time.Minute, sunHorizonDeg) {
		if !c.Rising && !c.Time.After(midnight) {
			nc.sunSet, nc.hasSunSet = c.Time, true // latest set before midnight
		}
		if c.Rising && c.Time.After(midnight) && !nc.hasSunRise {
			nc.sunRise, nc.hasSunRise = c.Time, true // first rise after midnight
		}
	}

	if nc.hasSunSet && nc.hasSunRise {
		nc.start, nc.end = nc.sunSet.Add(-nightPad), nc.sunRise.Add(nightPad)
	} else { // high-latitude fallback: no sunset/sunrise — frame the dark window instead
		nc.start, nc.end = dark.Start.Add(-nightPad), dark.End.Add(nightPad)
	}

	for _, c := range astro.AltitudeCrossings(moonAlt, nc.start, nc.end, 5*time.Minute, moonHorizonDeg) {
		if c.Rising && !nc.hasMoonRise {
			nc.moonRise, nc.hasMoonRise = c.Time, true
		}
		if !c.Rising && !nc.hasMoonSet {
			nc.moonSet, nc.hasMoonSet = c.Time, true
		}
	}

	nc.sunSeries = sampleSeries(sunAlt, nc.start, nc.end)
	nc.moonSeries = sampleSeries(moonAlt, nc.start, nc.end)
	return nc
}

func sampleSeries(altFn func(time.Time) float64, start, end time.Time) []AltSample {
	var out []AltSample
	for t := start; !t.After(end); t = t.Add(nightSeriesStep) {
		out = append(out, AltSample{TMs: t.UnixMilli(), AltDeg: round1(altFn(t))})
	}
	return out
}
