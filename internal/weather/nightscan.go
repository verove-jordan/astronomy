package weather

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// A night scan forecasts many locations for ONE night. It is the cheap way to weigh a whole search
// area against the free-tier quota, which weights a call by locations × days × variables: asking for
// the night's ten hours instead of a whole forecast day cuts a 160-point scan from ~100 calls to ~4,
// which is what makes "rank these spots for next Saturday" affordable at all.
//
// It never hard-errors. A failed or rate-limited scan returns unknown outlooks and a warning, and the
// caller falls back to ranking on terrain alone.

// Point is one location to forecast. ElevationM pins the elevation Open-Meteo downscales temperature
// to — worth setting for a mountain spot, whose real height the model's terrain smooths away — and 0
// leaves the model's own DEM in charge.
type Point struct {
	Lat, Lon   float64
	ElevationM float64
}

// NightOpts tunes one scan.
type NightOpts struct {
	// Moon weights each hour by how much moonlight spoils it (see astro.MoonGlowFactor).
	Moon func(tMs int64) float64
	// DeckTopM is the estimated top of any low-cloud deck over the area, shared by every point (see
	// DeckTop). A point standing above it is not charged for the cloud underneath.
	DeckTopM float64
	// AutoDeck derives DeckTopM from the scan itself when it is not supplied. It needs a scan that
	// spans real terrain — a handful of hand-picked summits has no valley floor to measure.
	AutoDeck bool
	// Detailed adds the pressure-level winds that drive the derived seeing index. It costs three more
	// variables per location, so it is for the handful of finalists, not the whole area.
	Detailed bool
}

// nightBaseVars is the smallest set that answers "is this night usable here": the cloud layers, the
// dew-point pair behind fog and dew risk, surface wind, and the boundary-layer depth the inversion
// test needs.
var nightBaseVars = []string{
	"cloud_cover", "cloud_cover_low", "cloud_cover_mid", "cloud_cover_high",
	"relative_humidity_2m", "dew_point_2m", "temperature_2m",
	"wind_speed_10m", "boundary_layer_height",
}

// nightDetailVars add the wind profile the seeing index is derived from.
var nightDetailVars = []string{"wind_speed_300hPa", "wind_speed_500hPa", "wind_speed_850hPa"}

const (
	// nightScanMaxPoints bounds one scan. It matches the cube's budget: both are ultimately limited by
	// the same minutely quota.
	nightScanMaxPoints = maxGridPoints
	nightCacheVersion  = 1
)

// NightScan returns one outlook per point for the night spanning [startMs,endMs], in request order.
func (p *Provider) NightScan(ctx context.Context, pts []Point, startMs, endMs int64, o NightOpts) ([]NightOutlook, string) {
	if len(pts) == 0 || endMs <= startMs {
		return nil, ""
	}
	if len(pts) > nightScanMaxPoints {
		pts = pts[:nightScanMaxPoints]
	}
	out := make([]NightOutlook, len(pts))
	for i := range out {
		out[i] = NightOutlook{StartMs: startMs, EndMs: endMs, Flags: []string{FlagBeyondHorizon}}
	}

	if p.beyondHorizon(endMs) {
		return out, "that night is past the forecast horizon"
	}

	key := nightKey(pts, startMs, endMs, o)
	if hours, ok := p.cachedNight(key); ok {
		return p.scoreScan(pts, hours, startMs, endMs, o), ""
	}
	if p.rateLimited() || p.recentlyFailed(key) {
		return p.staleNightOrUnknown(pts, out, key, startMs, endMs, o)
	}

	v, err, _ := p.flight.Do(key, func() (any, error) {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nightFetchTimeout)
		defer cancel()
		log.Printf("weather: night scan %s (%d points, %s)", key, len(pts), time.UnixMilli(startMs).UTC().Format("2006-01-02"))
		resp, err := p.fetchNight(fctx, pts, startMs, endMs, o.Detailed)
		if err != nil {
			return nil, err
		}
		hours := nightHoursFrom(resp)
		p.storeNight(key, hours)
		return hours, nil
	})
	if err != nil {
		log.Printf("weather: night scan %s failed (%d points): %v", key, len(pts), err)
		if errors.Is(err, ErrRateLimited) {
			p.tripRateLimit()
		}
		p.noteFailure(key)
		return p.staleNightOrUnknown(pts, out, key, startMs, endMs, o)
	}
	return p.scoreScan(pts, v.([]pointHours), startMs, endMs, o), ""
}

// pointHours is one location's assembled hours plus the elevation the forecast was computed for. It is
// what the cache stores: raw enough that re-scoring with a different Moon weighting costs no fetch.
type pointHours struct {
	ElevationM float64 `json:"elevation_m"`
	Hours      []Hour  `json:"hours"`
}

func (p *Provider) staleNightOrUnknown(pts []Point, unknown []NightOutlook, key string, startMs, endMs int64, o NightOpts) ([]NightOutlook, string) {
	if hours, ok := p.staleNight(key); ok {
		return p.scoreScan(pts, hours, startMs, endMs, o), "live weather unavailable — showing the last cached forecast for this night"
	}
	return unknown, "weather forecast unavailable — spots are ranked on darkness and horizon only"
}

// scoreScan aggregates each point's hours into its outlook.
func (p *Provider) scoreScan(pts []Point, hours []pointHours, startMs, endMs int64, o NightOpts) []NightOutlook {
	deck := o.DeckTopM
	if deck <= 0 && o.AutoDeck {
		deck = autoDeckTop(hours)
	}
	out := make([]NightOutlook, len(pts))
	for i := range pts {
		if i >= len(hours) {
			out[i] = NightOutlook{StartMs: startMs, EndMs: endMs, Flags: []string{FlagBeyondHorizon}}
			continue
		}
		elevation := pts[i].ElevationM
		if elevation <= 0 {
			elevation = hours[i].ElevationM
		}
		out[i] = ScoreNight(hours[i].Hours, NightInputs{
			StartMs:        startMs,
			EndMs:          endMs,
			Moon:           o.Moon,
			SiteElevationM: elevation,
			DeckTopM:       deck,
		})
		out[i].ElevationM = hours[i].ElevationM
		out[i].DeckTopM = deck
	}
	return out
}

// autoDeckTop derives the deck top from the scan itself. The 20th-percentile sampled terrain height
// stands in for the valley floor the deck forms over — a mean would be dragged up by the very high
// ground whose clearance we are trying to judge.
func autoDeckTop(hours []pointHours) float64 {
	if len(hours) < minDeckSamples {
		return 0
	}
	elevations := make([]float64, 0, len(hours))
	var all []Hour
	for _, ph := range hours {
		if ph.ElevationM > 0 {
			elevations = append(elevations, ph.ElevationM)
		}
		all = append(all, ph.Hours...)
	}
	if len(elevations) < minDeckSamples {
		return 0
	}
	sort.Float64s(elevations)
	floor := elevations[len(elevations)*lowlandPercentile/100]
	return DeckTop(floor, all)
}

const (
	// minDeckSamples is the fewest scanned points that can describe an area's terrain. Below it there
	// is no valley floor to speak of, only a few chosen spots.
	minDeckSamples = 12
	// lowlandPercentile picks the "floor" out of the sampled elevations.
	lowlandPercentile = 20
)

// beyondHorizon reports whether a night ends past what the configured forecast span can cover. Asking
// anyway would make Open-Meteo reject the whole request, so this saves both the call and the quota.
func (p *Provider) beyondHorizon(endMs int64) bool {
	limit := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, p.forecastDays)
	return time.UnixMilli(endMs).UTC().After(limit)
}

// fetchNight pulls the multi-location forecast restricted to the night's hours.
func (p *Provider) fetchNight(ctx context.Context, pts []Point, startMs, endMs int64, detailed bool) ([]omResponse, error) {
	vars := nightBaseVars
	if detailed {
		vars = append(append([]string{}, nightBaseVars...), nightDetailVars...)
	}
	return p.fetchChunked(ctx, len(pts), func(cctx context.Context, start, end int) ([]omResponse, error) {
		return p.fetchNightChunk(cctx, pts[start:end], startMs, endMs, vars)
	})
}

func (p *Provider) fetchNightChunk(ctx context.Context, pts []Point, startMs, endMs int64, vars []string) ([]omResponse, error) {
	lats := make([]float64, len(pts))
	lons := make([]float64, len(pts))
	for i, pt := range pts {
		lats[i], lons[i] = pt.Lat, pt.Lon
	}
	url := fmt.Sprintf("%s?latitude=%s&longitude=%s%s&hourly=%s&wind_speed_unit=kmh&start_hour=%s&end_hour=%s&timezone=UTC%s",
		p.openMeteoURL, joinFloats(lats), joinFloats(lons), elevationParam(pts),
		strings.Join(vars, ","), omHourParam(startMs), omHourParam(endMs), p.modelsParam())
	var resp []omResponse
	if err := p.getJSON(ctx, url, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// elevationParam renders the per-location elevation override, but only when EVERY point has one:
// Open-Meteo takes one comma list for all coordinates, so a partial list would silently shift the
// elevations onto the wrong points.
func elevationParam(pts []Point) string {
	elevations := make([]float64, len(pts))
	for i, pt := range pts {
		if pt.ElevationM <= 0 {
			return ""
		}
		elevations[i] = pt.ElevationM
	}
	return "&elevation=" + joinFloats(elevations)
}

// omHourParam renders an epoch-ms instant as Open-Meteo's start_hour/end_hour format, floored to the
// hour it belongs to so a dusk at 21:37 still includes the 21:00 sample.
func omHourParam(ms int64) string {
	return time.UnixMilli(ms).UTC().Truncate(time.Hour).Format("2006-01-02T15:04")
}

// nightHoursFrom assembles each location's response into hours. Unlike the per-site forecast there is
// no 7Timer or air-quality overlay: those are single-point feeds, and one call per candidate would
// cost far more than the sky-quality detail is worth at this stage.
func nightHoursFrom(resp []omResponse) []pointHours {
	out := make([]pointHours, len(resp))
	for i, r := range resp {
		ph := pointHours{ElevationM: r.Elevation}
		for j := range r.Hourly.Time {
			h, ok := omHour(r.Hourly, j)
			if !ok {
				continue
			}
			h.Verdict = hourVerdict(h)
			ph.Hours = append(ph.Hours, h)
		}
		out[i] = ph
	}
	return out
}

// nightKey identifies a scan by its exact point set, night and variable depth. The point list is
// hashed rather than listed: a 160-point scan would otherwise make a filename no filesystem accepts.
func nightKey(pts []Point, startMs, endMs int64, o NightOpts) string {
	h := sha256.New()
	for _, pt := range pts {
		fmt.Fprintf(h, "%.3f,%.3f,%.0f;", pt.Lat, pt.Lon, pt.ElevationM)
	}
	detail := "b"
	if o.Detailed {
		detail = "d"
	}
	return fmt.Sprintf("night_v%d_%s_%d_%d_%s", nightCacheVersion,
		hex.EncodeToString(h.Sum(nil))[:16], startMs/1000, endMs/1000, detail)
}
