package api

import (
	"context"
	"fmt"
	"image/png"
	"net/http"
	"strconv"
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/weather"
	"github.com/verove-jordan/astronomy/internal/weathertile"
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
	radius := floatParam(q, "radius", 0)                                                 // 0 → provider default; the provider clamps to a sane range
	g, warn := s.weatherGridAt(r.Context(), lat, lon, radius, splitCSV(q.Get("layers"))) // splitCSV: events.go
	writeJSON(w, http.StatusOK, weatherGridResponse{Grid: g, Warning: warn})
}

// weatherFramesResponse is the animated cube's time axis + coverage WITHOUT the float data — the small
// payload the map fetches to drive the scrubber and build tile URLs (the tiles carry the heavy data).
type weatherFramesResponse struct {
	BBox      [4]float64 `json:"bbox"`
	Timesteps []int64    `json:"timesteps"` // epoch ms per frame
	IssuedMs  int64      `json:"issued_ms"`
	Warning   string     `json:"warning,omitempty"`
}

// skyWeatherGridFrames returns just the cube's frames + coverage (no floats). It anchors its region to
// the SAME weathertile.TileRegion block quantizer the tile handler uses (via the map zoom `z`), so this
// fetch warms exactly the cube every subsequent tile request reads — one upstream fetch serves the
// scrubber axis and all metrics. (The old map-center+radius snap produced a different cache key from the
// tiles, so nothing was actually shared.) The time axis is trimmed to the scrub window: the observer
// plans tonight, not a 48 h film. ETag'd so a re-fetch of an unchanged forecast is a 304.
// GET /api/sky/weather/grid/frames?lat&lon&z
func (s *Server) skyWeatherGridFrames(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lat := floatParam(q, "lat", s.cfg.LatDeg)
	lon := floatParam(q, "lon", s.cfg.LonDeg)
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		badRequest(w, "lat/lon out of range")
		return
	}
	z := intParam(q, "z", 8)
	if z < 0 {
		z = 0
	} else if z > 19 {
		z = 19
	}
	tx, ty := weathertile.LatLonToTile(lat, lon, z)
	cLat, cLon, radius := weathertile.TileRegion(z, tx, ty)
	g, warn := s.weatherGridAt(r.Context(), cLat, cLon, radius, nil)
	resp := weatherFramesResponse{BBox: g.BBox, Timesteps: scrubWindow(g.Timesteps, time.Now()), IssuedMs: g.IssuedMs, Warning: warn}
	etag := fmt.Sprintf(`W/"wxf-%d-%d"`, g.IssuedMs, len(resp.Timesteps))
	writeJSONCached(w, r, http.StatusOK, etag, "private, max-age=300", resp)
}

// scrubWindow trims the cube's hourly axis to [now−1h, now+24h] — tonight through tomorrow's dawn. The
// cube itself keeps its full window (the tile handler renders any requested frame), only the scrubber
// range is bounded.
func scrubWindow(timesteps []int64, now time.Time) []int64 {
	lo := now.Add(-time.Hour).UnixMilli()
	hi := now.Add(24 * time.Hour).UnixMilli()
	out := make([]int64, 0, len(timesteps))
	for _, t := range timesteps {
		if t >= lo && t <= hi {
			out = append(out, t)
		}
	}
	return out
}

// skyWeatherTile server-renders one animated weather-overlay tile (metric at a frame time) as a PNG,
// mirroring lightPollutionTile: parse the tile, fetch the covering cube (shared per region → cached),
// render + serve. Transparent where the cube has no coverage (cacheable no-overlay); a 502 when upstream is
// fully down and no cube is cached (so Leaflet retries instead of caching a hole).
// GET /api/sky/weather/tiles/{metric}/{time}/{z}/{x}/{y}
func (s *Server) skyWeatherTile(w http.ResponseWriter, r *http.Request) {
	metric := r.PathValue("metric")
	switch metric {
	case "clouds", "clouds_low", "clouds_mid", "clouds_high", "humidity", "precip", "dewspread":
	default:
		badRequest(w, "unknown metric")
		return
	}
	timeMs, e0 := strconv.ParseInt(r.PathValue("time"), 10, 64)
	z, e1 := strconv.Atoi(r.PathValue("z"))
	x, e2 := strconv.Atoi(r.PathValue("x"))
	y, e3 := strconv.Atoi(r.PathValue("y"))
	if e0 != nil || e1 != nil || e2 != nil || e3 != nil {
		badRequest(w, "invalid tile coordinates")
		return
	}
	cLat, cLon, radius := weathertile.TileRegion(z, x, y)
	g, warn := s.weatherGridAt(r.Context(), cLat, cLon, radius, []string{metric})
	if warn != "" {
		// A tile is an <img> load — the browser can't read a body, but the header makes a degraded
		// upstream visible in the Network tab instead of an indistinguishable transparent PNG.
		w.Header().Set("X-Weather-Warning", warn)
	}
	if len(g.Timesteps) == 0 {
		// Upstream degraded (e.g. Open-Meteo's daily-request limit → 429) and no cube is cached. Serve a
		// transparent tile with a SHORT cache instead of a 502: a 200 means Leaflet simply shows no overlay
		// (the panel already surfaces the "unavailable" warning) rather than erroring — a tile error used to
		// trigger the client's cache-busting retry storm, which only burned MORE of the upstream daily quota
		// and kept the outage alive. The brief TTL lets the overlay self-heal within a minute once upstream
		// recovers, without caching a long-lived hole.
		writeTransparentTile(w, "public, max-age=60")
		return
	}
	img, painted := weathertile.RenderTile(g, metric, weathertile.FrameIndex(g.Timesteps, timeMs), z, x, y)
	if !painted { // this tile is outside the cube hull → a real "no overlay here", safe to cache
		writeTransparentTile(w, "public, max-age=1800")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=1800") // ~ the cube's 30-min cache window
	_ = png.Encode(w, img)
}

// weatherAt resolves the per-site forecast, tolerating a nil provider (a partially-built Server in
// tests) by returning an empty forecast — never a panic.
func (s *Server) weatherAt(ctx context.Context, lat, lon float64) (weather.SiteForecast, string) {
	if s.weather == nil {
		return weather.SiteForecast{Lat: lat, Lon: lon, Sources: []string{}}, ""
	}
	return s.weather.Forecast(ctx, lat, lon)
}

func (s *Server) weatherGridAt(ctx context.Context, lat, lon, radiusDeg float64, layers []string) (weather.Grid, string) {
	if s.weather == nil {
		return weather.Grid{Layers: map[string][][]float32{}}, ""
	}
	return s.weather.Grid(ctx, lat, lon, radiusDeg, layers)
}
