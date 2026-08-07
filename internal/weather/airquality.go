package weather

import (
	"context"
	"fmt"
)

type aqHourly struct {
	Time                []string  `json:"time"`
	AerosolOpticalDepth []float64 `json:"aerosol_optical_depth"`
	Dust                []float64 `json:"dust"`
}

type aqResponse struct {
	Hourly aqHourly `json:"hourly"`
}

// fetchAirQuality pulls aerosol optical depth (haze / wildfire smoke → transparency) from Open-Meteo's
// Air-Quality API for one site. pm2_5/pm10 used to ride along unread; Open-Meteo weights a call by its
// variable count, so an unread variable is quota spent on nothing.
func (p *Provider) fetchAirQuality(ctx context.Context, lat, lon float64) (aqResponse, error) {
	url := fmt.Sprintf("%s?latitude=%s&longitude=%s&hourly=aerosol_optical_depth,dust&past_days=1&forecast_days=%d&timezone=UTC",
		p.airQualityURL, ftoa(lat), ftoa(lon), p.forecastDays)
	var resp aqResponse
	if err := p.getJSON(ctx, url, &resp); err != nil {
		return aqResponse{}, err
	}
	return resp, nil
}
