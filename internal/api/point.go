package api

// "What is it like HERE?" for one map point — the payload behind the map's hover tooltip.
//
// It answers from data we already hold: the light-pollution atlas (a local file, so a lookup costs a
// disk seek) and the weather cache. It NEVER triggers an upstream weather fetch. weather.siteKey
// rounds to 0.01° (~1.1 km), so a hover-driven Forecast would mint a fresh Open-Meteo request every
// few pixels of pointer travel and exhaust the budget in seconds. A tooltip is a glance, not a query:
// it shows weather where we happen to know it and stays quiet where we do not, and hovering is free.

import (
	"net/http"
	"time"

	"github.com/verove-jordan/astronomy/internal/weather"
)

// pointWeather is the small weather summary a tooltip can show — the hour nearest now, from cache.
type pointWeather struct {
	TMs          int64   `json:"t_ms"`
	CloudPct     float64 `json:"cloud_pct"`
	SeeingArcsec float64 `json:"seeing_arcsec,omitempty"`
	TempC        float64 `json:"temp_c,omitempty"`
	HumidityPct  float64 `json:"humidity_pct,omitempty"`
}

// skyPoint reports the light pollution at a point, plus cached weather when available.
// GET /api/sky/point?lat=&lon=
func (s *Server) skyPoint(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lat := floatParam(q, "lat", s.cfg.LatDeg)
	lon := floatParam(q, "lon", s.cfg.LonDeg)
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		badRequest(w, "lat/lon out of range")
		return
	}
	site, warn := s.siteAt(r.Context(), lat, lon)
	resp := map[string]any{"lat": lat, "lon": lon, "site": site}
	if warn != "" {
		resp["warning"] = warn
	}
	if pw, ok := s.cachedPointWeather(lat, lon); ok {
		resp["weather"] = pw
	}
	writeJSON(w, http.StatusOK, resp)
}

// cachedPointWeather picks the hour nearest now out of an ALREADY-CACHED forecast. ok is false when
// nothing is cached for this point — the caller then simply omits weather rather than fetching it.
func (s *Server) cachedPointWeather(lat, lon float64) (pointWeather, bool) {
	if s.weather == nil {
		return pointWeather{}, false
	}
	f, ok := s.weather.CachedForecast(lat, lon)
	if !ok || len(f.Hours) == 0 {
		return pointWeather{}, false
	}
	best, ok := nearestHour(f.Hours, time.Now().UnixMilli())
	if !ok {
		return pointWeather{}, false
	}
	return pointWeather{
		TMs:          best.TMs,
		CloudPct:     best.CloudPct,
		SeeingArcsec: best.SeeingArcsec,
		TempC:        best.TempC,
		HumidityPct:  best.HumidityPct,
	}, true
}

// nearestHour is the sample closest to nowMs. A tooltip should describe the sky NOW, and a cached
// forecast may start hours in the past or the future, so "the first hour" would often be wrong.
func nearestHour(hours []weather.Hour, nowMs int64) (weather.Hour, bool) {
	if len(hours) == 0 {
		return weather.Hour{}, false
	}
	best := hours[0]
	for _, h := range hours[1:] {
		if abs64(h.TMs-nowMs) < abs64(best.TMs-nowMs) {
			best = h
		}
	}
	return best, true
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
