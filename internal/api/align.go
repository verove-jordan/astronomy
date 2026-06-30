package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/align"
)

// alignResponse is the GET /api/sky/align payload: the effective inputs echoed back plus the ordered
// GoTo alignment-star plan.
type alignResponse struct {
	Query  alignQueryEcho `json:"query"`
	Result align.Result   `json:"result"`
}

type alignQueryEcho struct {
	AtUTCMs  int64        `json:"at_utc_ms"`
	AtLocal  string       `json:"at_local"`
	Location locationEcho `json:"location"`
	Profile  string       `json:"profile"`
	Count    int          `json:"count"`
}

// skyAlign recommends an ordered, well-spread set of bright stars to center when calibrating a GoTo
// mount, for the observer's site and time. Stars the user skips (rejected) are excluded and replaced;
// stars already centered (accepted) are locked and constrain the rest. Every parameter is optional and
// falls back to the configured site / profile default.
func (s *Server) skyAlign(w http.ResponseWriter, r *http.Request) {
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

	profile := align.Lookup(q.Get("profile"))
	count := profile.ClampCount(intParam(q, "count", profile.DefaultStars))
	accepted := splitStarNames(q.Get("accepted"))
	rejected := splitStarNames(q.Get("rejected"))

	loc := s.cfg.Location()
	source := "config"
	if q.Get("lat") != "" || q.Get("lon") != "" {
		source = "query"
	}

	writeJSON(w, http.StatusOK, alignResponse{
		Query: alignQueryEcho{
			AtUTCMs: at.UnixMilli(),
			AtLocal: at.In(loc).Format("2006-01-02 15:04"),
			Location: locationEcho{
				Lat:      lat,
				Lon:      lon,
				Timezone: s.cfg.Timezone,
				Source:   source,
			},
			Profile: profile.Key,
			Count:   count,
		},
		Result: align.Plan(align.Params{At: at, Lat: lat, Lon: lon}, profile, count, accepted, rejected),
	})
}

// splitStarNames parses a comma-separated star-name list (the accepted/rejected query params),
// trimming blanks.
func splitStarNames(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
