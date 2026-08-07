package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/verove-jordan/astronomy/internal/lightpollution"
	"github.com/verove-jordan/astronomy/internal/skyevents"
	"github.com/verove-jordan/astronomy/internal/skyplan"
)

// eventsResponse is the GET /api/sky/events payload: the effective inputs echoed back, the site's light
// pollution, plus the scored, time-sorted calendar of events.
type eventsResponse struct {
	Query    eventsQueryEcho            `json:"query"`
	Count    int                        `json:"count"`
	Events   []skyevents.Event          `json:"events"`
	Site     lightpollution.SiteQuality `json:"site"`
	Warnings []string                   `json:"warnings,omitempty"`
}

type eventsQueryEcho struct {
	FromUTCMs int64         `json:"from_utc_ms"`
	ToUTCMs   int64         `json:"to_utc_ms"`
	Location  locationEcho  `json:"location"`
	Equipment equipmentEcho `json:"equipment"`
	Twilight  string        `json:"twilight"`
	Limits    magLimits     `json:"limits"` // faintest visible magnitude per instrument tier
}

// magLimits is the per-instrument limiting magnitude, so the UI can show "≤ 6.0" and flag too-faint events.
type magLimits struct {
	NakedEye  float64 `json:"naked_eye"`
	Binocular float64 `json:"binocular"`
	Telescope float64 `json:"telescope"`
}

// maxEventSpan caps how wide a window the calendar will compute (keeps a request bounded).
const maxEventSpan = 400 * 24 * time.Hour

// allCategories is the full set the calendar can generate.
var allCategories = []skyevents.Category{
	skyevents.CatEclipse, skyevents.CatPlanet, skyevents.CatMeteor,
	skyevents.CatMoon, skyevents.CatSeason, skyevents.CatComet, skyevents.CatSatellite,
}

// skyEvents computes the calendar of rare, watchable events for the site/gear over a date window.
// Every parameter is optional and falls back to the configured defaults.
func (s *Server) skyEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	now := time.Now().UTC()
	from, err := dateParam(q, "from", now)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	to, err := dateParam(q, "to", from.AddDate(0, 0, 90))
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	if !to.After(from) {
		badRequest(w, "'to' must be after 'from'")
		return
	}
	if to.Sub(from) > maxEventSpan {
		to = from.Add(maxEventSpan)
	}

	loc := s.cfg.Location()
	optics := skyplan.Optics{
		FocalMM:    floatParam(q, "focal_mm", s.cfg.FocalLenMM),
		ApertureMM: floatParam(q, "aperture_mm", s.cfg.ApertureMM),
		PixelUm:    floatParam(q, "pixel_um", s.cfg.PixelSizeUm),
		SensorWpx:  intParam(q, "sensor_w", s.cfg.SensorWpx),
		SensorHpx:  intParam(q, "sensor_h", s.cfg.SensorHpx),
		BarlowX:    floatParam(q, "barlow", s.cfg.BarlowX),
		ReducerX:   floatParam(q, "reducer", s.cfg.ReducerX),
	}
	prm := skyevents.Params{
		From:       from,
		To:         to,
		Lat:        floatParam(q, "lat", s.cfg.LatDeg),
		Lon:        floatParam(q, "lon", s.cfg.LonDeg),
		ElevationM: floatParam(q, "elevation_m", s.cfg.ElevationM),
		Optics:     optics,
		Twilight:   twilightParam(q.Get("twilight")),
		Categories: parseCategories(q),
		Location:   loc,
	}
	if prm.Lat < -90 || prm.Lat > 90 || prm.Lon < -180 || prm.Lon > 180 {
		badRequest(w, "lat/lon out of range")
		return
	}

	// Resolve the site's light pollution once and fold it into the faint-event scores (soft-failing).
	site, siteWarn := s.siteAt(r.Context(), prm.Lat, prm.Lon)
	prm.SiteSQM = site.SQM

	res, err := s.events.Compute(r.Context(), prm)
	if err != nil {
		serverError(w, err)
		return
	}
	warnings := res.Warnings
	if siteWarn != "" {
		warnings = append(warnings, siteWarn)
	}
	writeJSON(w, http.StatusOK, eventsResponse{
		Query:    eventsEcho(prm, loc, s.cfg.Timezone, q),
		Count:    res.Count,
		Events:   res.Events,
		Site:     site,
		Warnings: warnings,
	})
}

// skyEventSeries returns the next N events of one kind (and optional subtype) at the site, counting
// forward from now — e.g. the next 10 annular solar eclipses. Comets and satellite transits are not
// available as a series (not predictable far ahead) and yield 400.
func (s *Server) skyEventSeries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind := q.Get("kind")
	if kind == "" {
		badRequest(w, "missing 'kind'")
		return
	}
	from, err := dateParam(q, "from", time.Now().UTC())
	if err != nil {
		badRequest(w, err.Error())
		return
	}

	loc := s.cfg.Location()
	prm := skyevents.Params{
		From:       from,
		Lat:        floatParam(q, "lat", s.cfg.LatDeg),
		Lon:        floatParam(q, "lon", s.cfg.LonDeg),
		ElevationM: floatParam(q, "elevation_m", s.cfg.ElevationM),
		Optics: skyplan.Optics{
			FocalMM:    floatParam(q, "focal_mm", s.cfg.FocalLenMM),
			ApertureMM: floatParam(q, "aperture_mm", s.cfg.ApertureMM),
			PixelUm:    floatParam(q, "pixel_um", s.cfg.PixelSizeUm),
			SensorWpx:  intParam(q, "sensor_w", s.cfg.SensorWpx),
			SensorHpx:  intParam(q, "sensor_h", s.cfg.SensorHpx),
			BarlowX:    floatParam(q, "barlow", s.cfg.BarlowX),
			ReducerX:   floatParam(q, "reducer", s.cfg.ReducerX),
		},
		Twilight: twilightParam(q.Get("twilight")),
		Location: loc,
	}
	if prm.Lat < -90 || prm.Lat > 90 || prm.Lon < -180 || prm.Lon > 180 {
		badRequest(w, "lat/lon out of range")
		return
	}

	site, siteWarn := s.siteAt(r.Context(), prm.Lat, prm.Lon)
	prm.SiteSQM = site.SQM

	res, err := skyevents.Upcoming(r.Context(), prm, kind, q.Get("subtype"), intParam(q, "count", 10))
	if errors.Is(err, skyevents.ErrSeriesKindUnsupported) {
		badRequest(w, "this event kind is not available as an upcoming-by-type series")
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}

	// Echo the actual covered span (from → last returned event).
	prm.To = prm.From
	if n := len(res.Events); n > 0 {
		prm.To = time.UnixMilli(res.Events[n-1].PeakUTCMs).UTC()
	}
	warnings := res.Warnings
	if siteWarn != "" {
		warnings = append(warnings, siteWarn)
	}
	writeJSON(w, http.StatusOK, eventsResponse{
		Query:    eventsEcho(prm, loc, s.cfg.Timezone, q),
		Count:    res.Count,
		Events:   res.Events,
		Site:     site,
		Warnings: warnings,
	})
}

// parseCategories turns the optional `cats` CSV plus the `comets`/`satellites` flags into a toggle map.
// With no `cats`, every family is enabled; `comets=0`/`satellites=0` switch off the online feeds.
func parseCategories(q url.Values) map[skyevents.Category]bool {
	cats := map[skyevents.Category]bool{}
	if csv := q.Get("cats"); csv != "" {
		for _, c := range splitCSV(csv) {
			cats[skyevents.Category(c)] = true
		}
	} else {
		for _, c := range allCategories {
			cats[c] = true
		}
	}
	if q.Get("comets") == "0" {
		cats[skyevents.CatComet] = false
	}
	if q.Get("satellites") == "0" {
		cats[skyevents.CatSatellite] = false
	}
	return cats
}

func eventsEcho(prm skyevents.Params, loc *time.Location, tz string, q url.Values) eventsQueryEcho {
	source := "config"
	if q.Get("lat") != "" || q.Get("lon") != "" {
		source = "query"
	}
	fovW, fovH := prm.Optics.FOV()
	ne, bi, te := skyevents.MagLimits(prm.Optics.ApertureMM)
	return eventsQueryEcho{
		FromUTCMs: prm.From.UnixMilli(),
		ToUTCMs:   prm.To.UnixMilli(),
		Limits:    magLimits{NakedEye: round(ne, 1), Binocular: round(bi, 1), Telescope: round(te, 1)},
		Location: locationEcho{
			Lat:        prm.Lat,
			Lon:        prm.Lon,
			ElevationM: prm.ElevationM,
			Timezone:   tz,
			Source:     source,
		},
		Equipment: equipmentEcho{
			FocalMM:            prm.Optics.FocalMM,
			ApertureMM:         prm.Optics.ApertureMM,
			PixelUm:            prm.Optics.PixelUm,
			SensorWpx:          prm.Optics.SensorWpx,
			SensorHpx:          prm.Optics.SensorHpx,
			ImageScaleArcsecPx: round(prm.Optics.ImageScale(), 2),
			FovWDeg:            round(fovW, 3),
			FovHDeg:            round(fovH, 3),
			FRatio:             round(prm.Optics.FRatio(), 2),
			BarlowX:            prm.Optics.BarlowX,
			ReducerX:           prm.Optics.ReducerX,
		},
		Twilight: prm.Twilight,
	}
}

// dateParam parses an RFC3339 instant or a YYYY-MM-DD date, falling back to def when absent.
func dateParam(q url.Values, key string, def time.Time) (time.Time, error) {
	v := q.Get(key)
	if v == "" {
		return def, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("invalid '%s' (want RFC3339 or YYYY-MM-DD)", key)
}

func splitCSV(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
