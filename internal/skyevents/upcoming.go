package skyevents

import (
	"context"
	"errors"
	"sort"
)

// ErrSeriesKindUnsupported is returned when an "upcoming-by-type" series is requested for a kind that
// cannot be enumerated far into the future: comets are a finite, slowly-changing catalogue and satellite
// transits need fresh TLEs (reliable only ~days ahead). Those two are excluded from this mode.
var ErrSeriesKindUnsupported = errors.New("event kind not supported for an upcoming-by-type series")

const (
	// seriesHorizonYears caps how far Upcoming looks ahead (meeus is most accurate ~1900–2100).
	seriesHorizonYears = 80
	// seriesMaxCount bounds a single series request.
	seriesMaxCount = 100
)

// kindGenerator maps an event kind to the single cheapest generator that produces it, plus the window
// chunk (years) to advance per step. Kinds absent here are unsupported for a series (comet/satellite).
func kindGenerator(kind string) (gen func(Params) []Event, chunkYears int, ok bool) {
	switch kind {
	case "solar_eclipse", "lunar_eclipse":
		return eclipseEvents, 4, true
	case "moon_phase", "supermoon":
		return moonEvents, 2, true
	case "equinox", "solstice", "perihelion", "aphelion":
		return seasonEvents, 6, true
	case "meteor_shower":
		return meteorEvents, 4, true
	case "opposition", "elongation":
		return oppositionElongationEvents, 3, true
	case "conjunction":
		return conjunctionEvents, 3, true
	case "planet_moon":
		return planetMoonEvents, 2, true
	}
	return nil, 0, false
}

// Upcoming returns the next `count` events of `kind` (optionally narrowed by `subtype`) at the site,
// scanning forward from prm.From in expanding windows until enough are collected or the prediction
// horizon is reached. Each event is fully scored. Comets and satellite transits are unsupported.
func Upcoming(ctx context.Context, prm Params, kind, subtype string, count int) (*Result, error) {
	gen, chunkYears, ok := kindGenerator(kind)
	if !ok {
		return nil, ErrSeriesKindUnsupported
	}
	count = clampInt(count, 1, seriesMaxCount)

	horizon := prm.From.AddDate(seriesHorizonYears, 0, 0)
	res := &Result{}
	seen := map[string]bool{}

	for winStart := prm.From; winStart.Before(horizon) && len(res.Events) < count; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		winEnd := winStart.AddDate(chunkYears, 0, 0)
		if winEnd.After(horizon) {
			winEnd = horizon
		}

		chunk := prm
		chunk.From, chunk.To = winStart, winEnd
		for _, ev := range gen(chunk) {
			if ev.Kind != kind || (subtype != "" && ev.Subtype != subtype) {
				continue
			}
			finalizeEvent(&ev, prm)
			if seen[ev.ID] {
				continue
			}
			seen[ev.ID] = true
			res.Events = append(res.Events, ev)
		}
		winStart = winEnd
	}

	sort.SliceStable(res.Events, func(i, j int) bool {
		return res.Events[i].PeakUTCMs < res.Events[j].PeakUTCMs
	})
	if len(res.Events) > count {
		res.Events = res.Events[:count]
	}
	res.Count = len(res.Events)
	if res.Count < count {
		res.Warnings = append(res.Warnings,
			"fewer matching events than requested were found within the prediction horizon")
	}
	return res, nil
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
