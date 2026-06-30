package skyevents

import (
	"math"
	"time"

	"github.com/soniakeys/unit"
	"github.com/verove-jordan/astronomy/internal/astro"
)

// solarLocalContacts computes a solar eclipse's circumstances AT THE SITE, to the second, by sampling
// the topocentric Sun–Moon separation around greatest eclipse: outer contacts C1/C4 (partial begin/end),
// inner contacts C2/C3 (total or annular begin/end) and the local maximum. Returns visible=false when
// the disks never overlap from this location (the eclipse is not seen there).
func solarLocalContacts(jmax time.Time, prm Params) (contacts []Contact, start, end, peak time.Time, visible bool) {
	rhoSin, rhoCos := observerParallax(prm.Lat, prm.ElevationM)
	moonSD := func(t time.Time) float64 {
		_, _, sd := moonTopoRADec(t, prm.Lon, rhoSin, rhoCos)
		return sd
	}
	sep := func(t time.Time) float64 {
		sra, sdec := astro.SunPosition(t)
		mra, mdec, _ := moonTopoRADec(t, prm.Lon, rhoSin, rhoCos)
		return astro.AngularSeparation(sra, sdec, mra, mdec)
	}
	sunAlt := func(t time.Time) float64 {
		ra, dec := astro.SunPosition(t)
		a, _ := astro.Horizontal(ra, dec, prm.Lat, prm.Lon, t)
		return a
	}

	lo, hi := jmax.Add(-3*time.Hour), jmax.Add(3*time.Hour)
	minSep, tmin := math.MaxFloat64, jmax
	for t := lo; !t.After(hi); t = t.Add(10 * time.Second) {
		if s := sep(t); s < minSep {
			minSep, tmin = s, t
		}
	}
	peak = tmin
	if minSep > sunSemiDiamDeg+moonSD(tmin) { // disks never touch from here
		return nil, time.Time{}, time.Time{}, peak, false
	}

	gOuter := func(t time.Time) float64 { return sep(t) - (sunSemiDiamDeg + moonSD(t)) }
	c1 := bisectCrossing(gOuter, lo, tmin)
	c4 := bisectCrossing(gOuter, tmin, hi)
	contacts = append(contacts, contactAt("partial_begin", c1, sunAlt))

	if minSep < math.Abs(moonSD(tmin)-sunSemiDiamDeg) { // central phase reached locally (total/annular)
		gInner := func(t time.Time) float64 { return sep(t) - math.Abs(moonSD(t)-sunSemiDiamDeg) }
		c2 := bisectCrossing(gInner, c1, tmin)
		c3 := bisectCrossing(gInner, tmin, c4)
		contacts = append(contacts,
			contactAt("central_begin", c2, sunAlt),
			contactAt("maximum", tmin, sunAlt),
			contactAt("central_end", c3, sunAlt),
		)
	} else {
		contacts = append(contacts, contactAt("maximum", tmin, sunAlt))
	}
	contacts = append(contacts, contactAt("partial_end", c4, sunAlt))
	return contacts, c1, c4, peak, true
}

// lunarContacts builds a lunar eclipse's contact times from meeus' half-durations (these are global —
// the eclipse looks the same across the whole night side). Altitudes are the Moon's from the site.
func lunarContacts(jmax time.Time, sdTotal, sdPartial, sdPenumbral unit.Time, prm Params) (contacts []Contact, start, end time.Time) {
	moonAlt := func(t time.Time) float64 {
		ra, dec := astro.MoonPosition(t)
		a, _ := astro.Horizontal(ra, dec, prm.Lat, prm.Lon, t)
		return a
	}
	dur := func(u unit.Time) time.Duration { return time.Duration(u.Sec() * float64(time.Second)) }

	start, end = jmax.Add(-dur(sdPenumbral)), jmax.Add(dur(sdPenumbral))
	contacts = append(contacts, contactAt("penumbral_begin", start, moonAlt))
	if sdPartial.Sec() > 0 {
		contacts = append(contacts, contactAt("partial_begin", jmax.Add(-dur(sdPartial)), moonAlt))
	}
	if sdTotal.Sec() > 0 {
		contacts = append(contacts, contactAt("total_begin", jmax.Add(-dur(sdTotal)), moonAlt))
	}
	contacts = append(contacts, contactAt("maximum", jmax, moonAlt))
	if sdTotal.Sec() > 0 {
		contacts = append(contacts, contactAt("total_end", jmax.Add(dur(sdTotal)), moonAlt))
	}
	if sdPartial.Sec() > 0 {
		contacts = append(contacts, contactAt("partial_end", jmax.Add(dur(sdPartial)), moonAlt))
	}
	contacts = append(contacts, contactAt("penumbral_end", end, moonAlt))
	return contacts, start, end
}

func contactAt(label string, t time.Time, altFn func(time.Time) float64) Contact {
	return Contact{Label: label, UTCMs: t.UnixMilli(), AltDeg: round1(altFn(t))}
}

// bisectCrossing finds, to ~1 s, the time in [a,b] where g changes sign (g(a) and g(b) must differ).
func bisectCrossing(g func(time.Time) float64, a, b time.Time) time.Time {
	ga := g(a)
	for b.Sub(a) > time.Second {
		m := a.Add(b.Sub(a) / 2)
		if (ga < 0) == (g(m) < 0) {
			a, ga = m, g(m)
		} else {
			b = m
		}
	}
	return a.Add(b.Sub(a) / 2)
}
