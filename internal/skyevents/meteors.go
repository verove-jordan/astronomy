package skyevents

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"
)

//go:embed data/meteor_showers.json
var meteorData []byte

// shower is one annually-recurring meteor shower (IMO data). Radiant is J2000 equatorial degrees.
type shower struct {
	Code   string  `json:"code"`
	Name   string  `json:"name"`
	Month  int     `json:"month"`
	Day    int     `json:"day"`
	ZHR    float64 `json:"zhr"`
	RADeg  float64 `json:"ra_deg"`
	DecDeg float64 `json:"dec_deg"`
	Parent string  `json:"parent"`
}

var (
	showersOnce sync.Once
	showers     []shower
)

func loadShowers() []shower {
	showersOnce.Do(func() {
		if err := json.Unmarshal(meteorData, &showers); err != nil {
			showers = nil
		}
	})
	return showers
}

// meteorEvents projects each shower's peak into every year the window touches, scoring it by the
// radiant's altitude during darkness and the Moon's interference at the site.
func meteorEvents(prm Params) []Event {
	var out []Event
	for y := prm.From.Year(); y <= prm.To.Year(); y++ {
		for _, s := range loadShowers() {
			peakDate := time.Date(y, time.Month(s.Month), s.Day, 0, 0, 0, 0, time.UTC)
			mid := astro.SolarMidnight(peakDate, prm.Lat, prm.Lon) // darkest moment near the peak
			if !inRange(mid, prm.From, prm.To) {
				continue
			}
			ev := Event{
				Kind: "meteor_shower", Subtype: s.Code,
				PeakUTCMs: mid.UnixMilli(),
				ZHR:       s.ZHR,
				RADeg:     s.RADeg, DecDeg: s.DecDeg, HasPosition: true,
				Title:     s.Name + " meteor shower",
				ExtraText: fmt.Sprintf("ZHR ~%.0f · %s", s.ZHR, s.Parent),
				Notable:   s.ZHR >= 20,
			}
			ev.applyObs(observeNight(s.RADeg, s.DecDeg, mid, prm))
			out = append(out, ev)
		}
	}
	return out
}
