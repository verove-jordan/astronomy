package weather

import (
	"context"
	"fmt"
	neturl "net/url"
	"strconv"
	"strings"
	"sync"
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
	url := fmt.Sprintf("%s?latitude=%s&longitude=%s&hourly=%s&wind_speed_unit=kmh&past_days=1&forecast_days=2&timezone=UTC%s",
		p.openMeteoURL, ftoa(lat), ftoa(lon), strings.Join(openMeteoHourlyVars, ","), p.modelsParam())
	var resp omResponse
	if err := p.getJSON(ctx, url, &resp); err != nil {
		return omResponse{}, err
	}
	return resp, nil
}

// modelsParam renders the optional Open-Meteo `models=` pin ("" = best_match auto-selection, the
// default; see config.WeatherOpenMeteoModels).
func (p *Provider) modelsParam() string {
	if p.openMeteoModels == "" {
		return ""
	}
	return "&models=" + neturl.QueryEscape(p.openMeteoModels)
}

// Open-Meteo's multi-location endpoint is GET-only and request lines top out around 8 KB, so a dense
// grid cannot ride in one URL: the coordinate list is fetched in chunked GETs, a few in flight at once.
const (
	gridChunkMaxPoints = 400 // ≤400 lat+lon pairs at 3 decimals ≈ 7 KB of URL — safely under the cap
	gridChunkParallel  = 1   // sequential chunk GETs: a burst of parallel multi-hundred-point calls is
	//                          exactly what trips Open-Meteo's minutely quota (the budget keeps real
	//                          cubes to one chunk anyway; this is defense in depth)
)

// fetchOpenMeteoGrid pulls the multi-location forecast for the cloud cube. Open-Meteo returns a JSON
// array (one object per coordinate, in request order) when many coordinates are passed; the coordinates
// are fetched in concurrent chunks (see gridChunkMaxPoints) and reassembled in row-major request order.
// Any chunk failing fails the whole fetch — Grid's stale-cache soft-fail takes over from there.
func (p *Provider) fetchOpenMeteoGrid(ctx context.Context, lats, lons []float64, vars []string) ([]omResponse, error) {
	out := make([]omResponse, len(lats))
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Independent GETs → same WaitGroup + first-error fan-out as Forecast (x/sync's errgroup is not a
	// direct dependency); a semaphore keeps at most gridChunkParallel requests in flight.
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
		cancel() // the fetch is already lost — abort the other in-flight chunks
	}
	sem := make(chan struct{}, gridChunkParallel)
	for _, c := range gridChunks(len(lats), gridChunkMaxPoints) {
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if cctx.Err() != nil {
				return
			}
			chunk, err := p.fetchOpenMeteoGridChunk(cctx, lats[start:end], lons[start:end], vars)
			if err != nil {
				fail(fmt.Errorf("grid chunk %d..%d: %w", start, end, err))
				return
			}
			if len(chunk) != end-start {
				fail(fmt.Errorf("grid chunk %d..%d: got %d responses for %d coordinates", start, end, len(chunk), end-start))
				return
			}
			copy(out[start:end], chunk)
		}(c.start, c.end)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// fetchOpenMeteoGridChunk is one bulk GET for a slice of the grid's coordinate list. Unlike the per-site
// point forecast (which keeps a past day for the timeline), the animated map only shows the forecast
// window, so past_days=0 halves the frames (and the upstream payload) the cube carries.
func (p *Provider) fetchOpenMeteoGridChunk(ctx context.Context, lats, lons []float64, vars []string) ([]omResponse, error) {
	url := fmt.Sprintf("%s?latitude=%s&longitude=%s&hourly=%s&past_days=0&forecast_days=2&timezone=UTC%s",
		p.openMeteoURL, joinFloats(lats), joinFloats(lons), strings.Join(vars, ","), p.modelsParam())
	var resp []omResponse
	if err := p.getJSON(ctx, url, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// gridChunk is a half-open [start, end) run of the row-major coordinate list.
type gridChunk struct{ start, end int }

// gridChunks slices n coordinates into runs of at most size. A trailing run of exactly one point is
// folded into the previous chunk (making it size+1): Open-Meteo answers a single-coordinate request
// with a bare JSON object instead of a one-element array, which would break the chunk decode.
func gridChunks(n, size int) []gridChunk {
	if n <= 0 {
		return nil
	}
	var out []gridChunk
	for start := 0; start < n; start += size {
		out = append(out, gridChunk{start, min(start+size, n)})
	}
	last := len(out) - 1
	if last >= 1 && out[last].end-out[last].start == 1 {
		out[last-1].end = out[last].end
		out = out[:last]
	}
	return out
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
		case "dewspread":
			// Fog/dew risk = temperature − dew point (computed in gridSeries): ≈0 °C means saturated air
			// (fog forming, dew on the optics), large means dry. Two variables, still ≤10 total (weight 1×).
			add("temperature_2m")
			add("dew_point_2m")
		case "clouds_low":
			add("cloud_cover_low")
		case "clouds_mid":
			add("cloud_cover_mid")
		case "clouds_high":
			add("cloud_cover_high")
		default:
			// "clouds" (and any unknown layer) carries total cover, and also pulls the per-altitude bands:
			// the map composites low/mid/high into an intensity-true raster (expandGridLayers requests the
			// band layers explicitly, but a bare "clouds" must stay self-sufficient).
			add("cloud_cover")
			add("cloud_cover_low")
			add("cloud_cover_mid")
			add("cloud_cover_high")
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
	case "dewspread":
		return dewSpread(h.Temperature2m, h.DewPoint2m)
	case "clouds_low":
		return h.CloudCoverLow
	case "clouds_mid":
		return h.CloudCoverMid
	case "clouds_high":
		return h.CloudCoverHigh
	default:
		return h.CloudCover
	}
}

// dewSpread is the element-wise temperature−dew-point gap (°C): ≈0 → saturated air (fog forming, dew on
// the optics), large → dry. Follows the shorter input, tolerating a truncated upstream series.
func dewSpread(temp, dew []float64) []float64 {
	n := min(len(temp), len(dew))
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		out[i] = temp[i] - dew[i]
	}
	return out
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', 4, 64) }

// joinFloats renders the grid's coordinate lists for the single bulk Open-Meteo GET. It uses 3 decimals
// (~110 m) — far finer than a weather cell — rather than ftoa's 4, so a denser grid (nx·ny coordinates
// comma-joined in the URL) stays well under the ~8 KB request-line limit.
func joinFloats(fs []float64) string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = strconv.FormatFloat(f, 'f', 3, 64)
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
