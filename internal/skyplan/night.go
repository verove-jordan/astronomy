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

	midnight := astro.SolarMidnight(prm.At, lat, lon)
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
