package weather

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// openMeteoHourlyVars are the per-site forecast fields requested from Open-Meteo (the backbone feed).
var openMeteoHourlyVars = []string{
	"cloud_cover", "cloud_cover_low", "cloud_cover_mid", "cloud_cover_high",
	"relative_humidity_2m", "dew_point_2m", "temperature_2m",
	"wind_speed_10m", "wind_gusts_10m", "wind_speed_300hPa",
	"cape", "lifted_index", "visibility", "precipitation", "precipitation_probability",
}

type omHourly struct {
	Time                     []string  `json:"time"`
	CloudCover               []float64 `json:"cloud_cover"`
	CloudCoverLow            []float64 `json:"cloud_cover_low"`
	CloudCoverMid            []float64 `json:"cloud_cover_mid"`
	CloudCoverHigh           []float64 `json:"cloud_cover_high"`
	RelativeHumidity2m       []float64 `json:"relative_humidity_2m"`
	DewPoint2m               []float64 `json:"dew_point_2m"`
	Temperature2m            []float64 `json:"temperature_2m"`
	WindSpeed10m             []float64 `json:"wind_speed_10m"`
	WindGusts10m             []float64 `json:"wind_gusts_10m"`
	WindSpeed300hPa          []float64 `json:"wind_speed_300hPa"`
	CAPE                     []float64 `json:"cape"`
	LiftedIndex              []float64 `json:"lifted_index"`
	Visibility               []float64 `json:"visibility"`
	Precipitation            []float64 `json:"precipitation"`
	PrecipitationProbability []float64 `json:"precipitation_probability"`
}

type omResponse struct {
	Latitude  float64  `json:"latitude"`
	Longitude float64  `json:"longitude"`
	Hourly    omHourly `json:"hourly"`
}

// fetchOpenMeteoPoint pulls the hourly point forecast (past day + next two days) for one site.
func (p *Provider) fetchOpenMeteoPoint(ctx context.Context, lat, lon float64) (omResponse, error) {
	url := fmt.Sprintf("%s?latitude=%s&longitude=%s&hourly=%s&wind_speed_unit=kmh&past_days=1&forecast_days=2&timezone=UTC",
		p.openMeteoURL, ftoa(lat), ftoa(lon), strings.Join(openMeteoHourlyVars, ","))
	var resp omResponse
	if err := p.getJSON(ctx, url, &resp); err != nil {
		return omResponse{}, err
	}
	return resp, nil
}

// fetchOpenMeteoGrid pulls one multi-location forecast for the cloud cube. Open-Meteo returns a JSON
// array (one object per coordinate, in request order) when many coordinates are passed.
func (p *Provider) fetchOpenMeteoGrid(ctx context.Context, lats, lons []float64, vars []string) ([]omResponse, error) {
	url := fmt.Sprintf("%s?latitude=%s&longitude=%s&hourly=%s&past_days=1&forecast_days=1&timezone=UTC",
		p.openMeteoURL, joinFloats(lats), joinFloats(lons), strings.Join(vars, ","))
	var resp []omResponse
	if err := p.getJSON(ctx, url, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// omGridVars maps overlay layer names to the Open-Meteo hourly variables the grid needs (deduplicated).
func omGridVars(layers []string) []string {
	seen := map[string]bool{}
	var vars []string
	add := func(v string) {
		if !seen[v] {
			seen[v] = true
			vars = append(vars, v)
		}
	}
	for _, l := range layers {
		switch l {
		case "humidity":
			add("relative_humidity_2m")
		case "precip":
			// Probability (not mm): rain amount is ~0 almost everywhere, so it never shows; the chance of
			// precipitation varies meaningfully across the region and is what the observer cares about.
			add("precipitation_probability")
		default:
			add("cloud_cover")
		}
	}
	if len(vars) == 0 {
		add("cloud_cover")
	}
	return vars
}

// gridSeries returns the hourly series for one overlay layer from a cell's Open-Meteo response.
func gridSeries(h omHourly, layer string) []float64 {
	switch layer {
	case "humidity":
		return h.RelativeHumidity2m
	case "precip":
		return h.PrecipitationProbability // chance of precipitation (%), see omGridVars
	default:
		return h.CloudCover
	}
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', 4, 64) }

func joinFloats(fs []float64) string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = ftoa(f)
	}
	return strings.Join(parts, ",")
}

// parseOMTime parses an Open-Meteo naive UTC timestamp ("2006-01-02T15:04") to epoch milliseconds.
func parseOMTime(s string) (int64, bool) {
	t, err := time.Parse("2006-01-02T15:04", s)
	if err != nil {
		return 0, false
	}
	return t.UTC().UnixMilli(), true
}
