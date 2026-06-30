package skyevents

import (
	"fmt"

	"github.com/soniakeys/meeus/v3/eclipse"
	"github.com/verove-jordan/astronomy/internal/astro"
)

// eclipseEvents enumerates solar and lunar eclipses in the window. meeus returns the eclipse nearest a
// decimal year, so we step finely and dedupe by day. Local visibility is approximated by the Sun/Moon
// altitude at greatest eclipse from the site (precise solar local circumstances are a future refinement).
func eclipseEvents(prm Params) []Event {
	var out []Event
	seen := map[int64]bool{}
	y0, y1 := decimalYear(prm.From), decimalYear(prm.To)
	for y := y0 - 0.05; y <= y1+0.05; y += 0.03 {
		if ev, ok := solarEclipseAt(y, prm, seen); ok {
			out = append(out, ev)
		}
		if ev, ok := lunarEclipseAt(y, prm, seen); ok {
			out = append(out, ev)
		}
	}
	return out
}

func solarEclipseAt(y float64, prm Params, seen map[int64]bool) (Event, bool) {
	etype, _, jmax, _, _, _, _ := eclipse.Solar(y)
	if etype == eclipse.None {
		return Event{}, false
	}
	t := jdeToTime(jmax)
	key := dayKey(t.UnixMilli())*10 + 1
	if seen[key] {
		return Event{}, false
	}
	seen[key] = true
	if !inRange(t, prm.From, prm.To) {
		return Event{}, false
	}
	sub := solarTypeName(etype)
	contacts, start, end, peak, visible := solarLocalContacts(t, prm)
	ra, dec := astro.SunPosition(peak)
	alt, az := astro.Horizontal(ra, dec, prm.Lat, prm.Lon, peak)
	ev := Event{
		Kind: "solar_eclipse", Subtype: sub,
		PeakUTCMs: peak.UnixMilli(), BestUTCMs: peak.UnixMilli(),
		Bodies: []string{"sun", "moon"},
		RADeg:  ra, DecDeg: dec, HasPosition: true,
		AltAtBestDeg: alt, AzAtBestDeg: az,
		MoonIllum: astro.MoonIllumination(peak),
		Title:     titleCase(sub) + " solar eclipse",
		Notable:   true,
	}
	if visible { // local circumstances at the site
		ev.Contacts = contacts
		ev.StartUTCMs, ev.EndUTCMs = start.UnixMilli(), end.UnixMilli()
	} else {
		ev.Contacts = []Contact{{Label: "maximum", UTCMs: peak.UnixMilli(), AltDeg: round1(alt)}}
		ev.ExtraText = "not visible from your site"
	}
	return ev, true
}

func lunarEclipseAt(y float64, prm Params, seen map[int64]bool) (Event, bool) {
	etype, jmax, _, _, _, mag, sdTotal, sdPartial, sdPenumbral := eclipse.Lunar(y)
	if etype == eclipse.None {
		return Event{}, false
	}
	t := jdeToTime(jmax)
	key := dayKey(t.UnixMilli())*10 + 2
	if seen[key] {
		return Event{}, false
	}
	seen[key] = true
	if !inRange(t, prm.From, prm.To) {
		return Event{}, false
	}
	sub := lunarTypeName(etype)
	ra, dec := astro.MoonPosition(t)
	alt, az := astro.Horizontal(ra, dec, prm.Lat, prm.Lon, t)
	contacts, start, end := lunarContacts(t, sdTotal, sdPartial, sdPenumbral, prm)
	return Event{
		Kind: "lunar_eclipse", Subtype: sub,
		PeakUTCMs: t.UnixMilli(), BestUTCMs: t.UnixMilli(),
		StartUTCMs: start.UnixMilli(), EndUTCMs: end.UnixMilli(),
		Bodies: []string{"moon"},
		RADeg:  ra, DecDeg: dec, HasPosition: true,
		AltAtBestDeg: alt, AzAtBestDeg: az,
		MoonIllum: 1.0, // full Moon by definition
		Title:     titleCase(sub) + " lunar eclipse",
		ExtraText: fmt.Sprintf("umbral magnitude %.2f", mag),
		Contacts:  contacts,
		Notable:   sub != "penumbral",
	}, true
}

func solarTypeName(t int) string {
	switch t {
	case eclipse.Total:
		return "total"
	case eclipse.Annular:
		return "annular"
	case eclipse.AnnularTotal:
		return "annular_total"
	default:
		return "partial"
	}
}

func lunarTypeName(t int) string {
	switch t {
	case eclipse.Total:
		return "total"
	case eclipse.Umbral:
		return "partial"
	default:
		return "penumbral"
	}
}
