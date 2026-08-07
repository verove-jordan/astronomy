package api

import (
	"net/http"
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/skyplan"
)

// nightInfo describes one upcoming night: when it is dark, how much Moon is in the way, and whether a
// forecast for it is worth trusting. The dark-sky finder's night picker is built from this, so the
// twilight and Moon arithmetic lives here once instead of being re-implemented in the browser.
type nightInfo struct {
	Index         int     `json:"index"` // 0 = tonight
	StartMs       int64   `json:"start_ms"`
	EndMs         int64   `json:"end_ms"`
	StartLocal    string  `json:"start_local"`
	EndLocal      string  `json:"end_local"`
	DateLocal     string  `json:"date_local"` // the evening's date, for labelling the picker
	Kind          string  `json:"kind"`       // astronomical | nautical | civil | best_effort
	DarkHours     float64 `json:"dark_hours"`
	MoonIllum     float64 `json:"moon_illum"`    // 0..1
	MoonUpHours   float64 `json:"moon_up_hours"` // hours of the dark window the Moon spoils
	MoonPhase     string  `json:"moon_phase"`    // locale-neutral key; the UI translates it
	LowConfidence bool    `json:"low_confidence"`
}

type nightsResponse struct {
	Nights   []nightInfo `json:"nights"`
	Timezone string      `json:"timezone"`
}

// skyNightsHorizonDays is how far ahead a forecast still carries enough skill to rank places by. Past
// it a cloud forecast tends towards climatology, so nights beyond are flagged rather than hidden — the
// user can still look, they just should not plan a two-hour drive on it.
const skyNightsConfidentDays = 4

// nightCount is how many nights the finder may be asked about: never more than the forecast reaches.
func (s *Server) nightCount() int {
	n := s.cfg.DarkSkyNights
	if n < 1 {
		n = 1
	}
	if s.weather != nil && n > s.weather.ForecastDays() {
		n = s.weather.ForecastDays()
	}
	return n
}

// skyNights lists the upcoming observing nights: GET /api/sky/nights?lat=&lon=&days=
func (s *Server) skyNights(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lat := floatParam(q, "lat", s.cfg.LatDeg)
	lon := floatParam(q, "lon", s.cfg.LonDeg)
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		badRequest(w, "invalid location: lat must be -90..90 and lon -180..180")
		return
	}
	days := clampInt(intParam(q, "days", s.nightCount()), 1, s.nightCount())
	loc := s.cfg.Location()

	out := nightsResponse{Nights: make([]nightInfo, 0, days), Timezone: loc.String()}
	base := astro.NightWindow(time.Now().UTC(), lat, lon, -18)
	for i := 0; i < days; i++ {
		night := base
		if i > 0 {
			// Step from the first night rather than from the clock, so the list is exactly one entry
			// per night at any longitude and at any hour of the night the search is made.
			night = astro.NightWindow(base.Start.AddDate(0, 0, i).Add(time.Hour), lat, lon, -18)
		}
		mid := night.Start.Add(night.End.Sub(night.Start) / 2)
		out.Nights = append(out.Nights, nightInfo{
			Index:         i,
			StartMs:       night.Start.UnixMilli(),
			EndMs:         night.End.UnixMilli(),
			StartLocal:    night.Start.In(loc).Format("15:04"),
			EndLocal:      night.End.In(loc).Format("15:04"),
			DateLocal:     night.Start.In(loc).Format("2006-01-02"),
			Kind:          night.Kind,
			DarkHours:     round(night.Hours(), 1),
			MoonIllum:     round(astro.MoonIllumination(mid), 2),
			MoonUpHours:   round(astro.MoonUpHours(night, lat, lon), 1),
			MoonPhase:     skyplan.MoonPhaseName(astro.MoonPhaseAngle(mid)),
			LowConfidence: i >= skyNightsConfidentDays,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
