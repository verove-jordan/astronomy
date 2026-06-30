package weather

import (
	"context"
	"fmt"
	"time"
)

// stPoint is one 7Timer! ASTRO timestep. seeing and transparency are bucket indices (1..8); we only
// take those two astronomy-specific fields — Open-Meteo supplies the rest at hourly resolution.
type stPoint struct {
	Timepoint    int `json:"timepoint"` // hours from Init
	Seeing       int `json:"seeing"`
	Transparency int `json:"transparency"`
	Cloudcover   int `json:"cloudcover"`
}

type stResponse struct {
	Init       string    `json:"init"` // "YYYYMMDDHH" (UTC model run)
	Dataseries []stPoint `json:"dataseries"`
}

// fetchSevenTimer pulls the 7Timer! ASTRO product (seeing + transparency), the gold-standard free
// astronomy forecast, for one site.
func (p *Provider) fetchSevenTimer(ctx context.Context, lat, lon float64) (stResponse, error) {
	url := fmt.Sprintf("%s?lon=%s&lat=%s&product=astro&output=json", p.sevenTimerURL, ftoa(lon), ftoa(lat))
	var resp stResponse
	if err := p.getJSON(ctx, url, &resp); err != nil {
		return stResponse{}, err
	}
	return resp, nil
}

// stTimeMs converts a 7Timer Init ("YYYYMMDDHH") + a timepoint offset (hours) to epoch milliseconds.
func stTimeMs(init string, timepoint int) (int64, bool) {
	t, err := time.Parse("2006010215", init)
	if err != nil {
		return 0, false
	}
	return t.UTC().Add(time.Duration(timepoint) * time.Hour).UnixMilli(), true
}
