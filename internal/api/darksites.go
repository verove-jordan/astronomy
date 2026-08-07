package api

import (
	"net/http"

	"github.com/verove-jordan/astronomy/internal/darksky"
	"github.com/verove-jordan/astronomy/internal/lightpollution"
)

// darkSites finds the darkest, most open observing sites in a map area, ranked for one night:
// GET /api/sky/darksites?min_lat=&min_lon=&max_lat=&max_lon=&max_bortle=&limit=&horizon=&lat=&lon=
//
//	&night=0..N          which night to rank for (0 = tonight)
//	&weather=1|0         fold the night's forecast into the ranking (default on)
//	&weather_weight=0..0.8   how much of the score the forecast takes (0 = the configured default)
func (s *Server) darkSites(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	bbox := lightpollution.Bbox{
		MinLat: floatParam(q, "min_lat", 0),
		MinLon: floatParam(q, "min_lon", 0),
		MaxLat: floatParam(q, "max_lat", 0),
		MaxLon: floatParam(q, "max_lon", 0),
	}
	if bbox.MaxLat <= bbox.MinLat || bbox.MaxLon <= bbox.MinLon {
		badRequest(w, "invalid area: max_lat/max_lon must exceed min_lat/min_lon")
		return
	}
	if bbox.MaxLat-bbox.MinLat > 12 || bbox.MaxLon-bbox.MinLon > 12 {
		badRequest(w, "area too large: keep each side under ~12°")
		return
	}

	result := s.darksky.Find(r.Context(), darksky.Query{
		Bbox:      bbox,
		MaxBortle: clampInt(intParam(q, "max_bortle", 4), 1, 9),
		Limit:     clampInt(intParam(q, "limit", 12), 1, 50),
		Horizon:   q.Get("horizon") == "1",
		ObsLat:    floatParam(q, "lat", s.cfg.LatDeg),
		ObsLon:    floatParam(q, "lon", s.cfg.LonDeg),
		ObsSet:    q.Has("lat") && q.Has("lon"), // drives only from the user's real location, not the default
		// Weather is on unless the client says otherwise: "where should I go tonight" is a question
		// about the sky, and a ranking that ignores it is confidently wrong more often than not.
		NightIndex:    clampInt(intParam(q, "night", 0), 0, s.nightCount()-1),
		Weather:       q.Get("weather") != "0",
		WeatherWeight: floatParam(q, "weather_weight", 0),
	})
	writeJSON(w, http.StatusOK, result)
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
