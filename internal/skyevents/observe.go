package skyevents

import (
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/skyplan"
)

// obs is the best observing circumstance for a fixed sky position on a given night from the site.
type obs struct {
	BestUTCMs    int64
	AltDeg       float64
	AzDeg        float64
	MoonSepDeg   float64
	MoonIllum    float64
	UpInDarkness bool
	WinStartMs   int64 // first/last moment the object is above the horizon during the night's darkness
	WinEndMs     int64
	Night        *skyplan.DarknessInfo
}

// observeNight finds, for an object at fixed ra/dec (degrees), the highest moment during the darkness of
// the night bracketing `around` at the site, plus the Moon context and the night chart-context (reused
// from skyplan so the detail chart matches the Tonight page). For slowly-moving bodies (planets, comets)
// treating the position as fixed across one night is accurate enough for visibility scoring.
func observeNight(ra, dec float64, around time.Time, prm Params) obs {
	return observeWith(func(time.Time) (float64, float64) { return ra, dec }, around, prm)
}

// observeWith is observeNight for a moving body: posFn returns the body's RA/Dec at a given time (used
// for the fast-moving Moon). It returns the position at the best moment via the obs' Alt/Az.
func observeWith(posFn func(time.Time) (raDeg, decDeg float64), around time.Time, prm Params) obs {
	night := skyplan.NightContext(around, prm.Lat, prm.Lon, prm.ElevationM, prm.Twilight, prm.Location)
	o := obs{Night: &night, AltDeg: -90}

	start := time.UnixMilli(night.DuskUTCMs).UTC()
	end := time.UnixMilli(night.DawnUTCMs).UTC()
	if !end.After(start) { // no real darkness (polar summer): scan the whole chart window
		start = time.UnixMilli(night.NightStartMs).UTC()
		end = time.UnixMilli(night.NightEndMs).UTC()
	}
	bestRA, bestDec := posFn(around)
	var firstUp, lastUp time.Time
	for t := start; !t.After(end); t = t.Add(5 * time.Minute) {
		ra, dec := posFn(t)
		alt, az := astro.Horizontal(ra, dec, prm.Lat, prm.Lon, t)
		if alt > o.AltDeg {
			o.AltDeg, o.AzDeg, o.BestUTCMs = alt, az, t.UnixMilli()
			bestRA, bestDec = ra, dec
		}
		if alt > 0 {
			if firstUp.IsZero() {
				firstUp = t
			}
			lastUp = t
		}
	}
	o.UpInDarkness = o.AltDeg > 0
	if !firstUp.IsZero() && lastUp.After(firstUp) {
		o.WinStartMs, o.WinEndMs = firstUp.UnixMilli(), lastUp.UnixMilli()
	}
	best := time.UnixMilli(o.BestUTCMs).UTC()
	moon := astro.MoonNow(best, prm.Lat, prm.Lon)
	o.MoonIllum = moon.IllumFraction
	o.MoonSepDeg = astro.AngularSeparation(bestRA, bestDec, moon.RADeg, moon.DecDeg)
	return o
}

// applyObs copies an observing circumstance onto an event.
func (e *Event) applyObs(o obs) {
	e.AltAtBestDeg = o.AltDeg
	e.AzAtBestDeg = o.AzDeg
	e.BestUTCMs = o.BestUTCMs
	e.MoonSepDeg = o.MoonSepDeg
	e.MoonIllum = o.MoonIllum
	e.Night = o.Night
	if e.StartUTCMs == 0 && o.WinStartMs != 0 && o.WinEndMs > o.WinStartMs {
		e.StartUTCMs, e.EndUTCMs = o.WinStartMs, o.WinEndMs // when it's actually up in the dark
	}
}
