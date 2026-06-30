package api

import (
	"net/http"
	"time"

	"github.com/verove-jordan/astronomy/internal/polaralign"
)

// polarResponse is the GET /api/sky/polar payload: the effective inputs echoed back plus the
// polar-alignment readout (where the pole star sits on the reticle right now).
type polarResponse struct {
	Query  polarQueryEcho    `json:"query"`
	Result polaralign.Result `json:"result"`
}

type polarQueryEcho struct {
	AtUTCMs  int64        `json:"at_utc_ms"`
	AtLocal  string       `json:"at_local"`
	Location locationEcho `json:"location"`
}

// skyPolar reports the pole star's reticle clock position for the observer's site and time, used to
// polar-align an equatorial mount. Every parameter is optional and falls back to the configured site.
func (s *Server) skyPolar(w http.ResponseWriter, r *http.Request) {
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

	loc := s.cfg.Location()
	source := "config"
	if q.Get("lat") != "" || q.Get("lon") != "" {
		source = "query"
	}
	writeJSON(w, http.StatusOK, polarResponse{
		Query: polarQueryEcho{
			AtUTCMs: at.UnixMilli(),
			AtLocal: at.In(loc).Format("2006-01-02 15:04"),
			Location: locationEcho{
				Lat:      lat,
				Lon:      lon,
				Timezone: s.cfg.Timezone,
				Source:   source,
			},
		},
		Result: polaralign.Compute(at, lat, lon),
	})
}
