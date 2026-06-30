package api

import (
	"context"
	"net/http"
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/weather"
)

// weatherResponse is the GET /api/sky/weather payload: the effective inputs echoed back plus the
// per-site astronomy-weather forecast (and a warning when a feed was degraded/unavailable).
type weatherResponse struct {
	Query    weatherQueryEcho     `json:"query"`
	Forecast weather.SiteForecast `json:"forecast"`
	Warning  string               `json:"warning,omitempty"`
}

type weatherQueryEcho struct {
	AtUTCMs  int64        `json:"at_utc_ms"`
	AtLocal  string       `json:"at_local"`
	Location locationEcho `json:"location"`
}

type weatherGridResponse struct {
	Grid    weather.Grid `json:"grid"`
	Warning string       `json:"warning,omitempty"`
}

// skyWeather reports the per-site astronomy-weather timeline (clouds, seeing, transparency, humidity,
// dew risk, wind, jet stream, instability, aerosols, aurora) for the /tonight panel + badge, and marks
// the best clear window inside tonight's astronomical dark window. Soft-failing: a feed that is down is
// omitted with a warning; the page still renders.
func (s *Server) skyWeather(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	at, err := dateParam(q, "at", time.Now().UTC())
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	lat := floatParam(q, "lat", s.cfg.LatDeg)
	lon := floatParam(q, "lon", s.cfg.LonDeg)
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		badRequest(w, "lat/lon out of range")
		return
	}

	f, warn := s.weatherAt(r.Context(), lat, lon)
	night := astro.NightWindow(at, lat, lon, -18) // astronomical dark
	f.Best = weather.BestWindow(f.Hours, night.Start.UnixMilli(), night.End.UnixMilli())

	loc := s.cfg.Location()
	source := "config"
	if q.Get("lat") != "" || q.Get("lon") != "" {
		source = "query"
	}
	writeJSON(w, http.StatusOK, weatherResponse{
		Query: weatherQueryEcho{
			AtUTCMs:  at.UnixMilli(),
			AtLocal:  at.In(loc).Format("2006-01-02 15:04"),
			Location: locationEcho{Lat: lat, Lon: lon, Timezone: s.cfg.Timezone, Source: source},
		},
		Forecast: f,
		Warning:  warn,
	})
}

// skyWeatherGrid returns the regional cloud cube the map animates (one value per cell per timestep).
func (s *Server) skyWeatherGrid(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lat := floatParam(q, "lat", s.cfg.LatDeg)
	lon := floatParam(q, "lon", s.cfg.LonDeg)
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		badRequest(w, "lat/lon out of range")
		return
	}
	g, warn := s.weatherGridAt(r.Context(), lat, lon, splitCSV(q.Get("layers"))) // splitCSV: events.go
	writeJSON(w, http.StatusOK, weatherGridResponse{Grid: g, Warning: warn})
}

// weatherAt resolves the per-site forecast, tolerating a nil provider (a partially-built Server in
// tests) by returning an empty forecast — never a panic.
func (s *Server) weatherAt(ctx context.Context, lat, lon float64) (weather.SiteForecast, string) {
	if s.weather == nil {
		return weather.SiteForecast{Lat: lat, Lon: lon, Sources: []string{}}, ""
	}
	return s.weather.Forecast(ctx, lat, lon)
}

func (s *Server) weatherGridAt(ctx context.Context, lat, lon float64, layers []string) (weather.Grid, string) {
	if s.weather == nil {
		return weather.Grid{Layers: map[string][][]float32{}}, ""
	}
	return s.weather.Grid(ctx, lat, lon, layers)
}
