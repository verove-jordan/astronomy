// Package darksky finds the best observing sites in a map area: it grids the region for low light
// pollution (reusing internal/lightpollution), ranks the darkest cells, and — for the top few — scores
// how open their ~360° horizon is from terrain (internal/elevation). It composes those two providers
// behind small interfaces so the finder is testable with fakes.
package darksky

import (
	"context"
	"math"
	"sort"
	"sync"

	"github.com/verove-jordan/astronomy/internal/elevation"
	"github.com/verove-jordan/astronomy/internal/lightpollution"
	"github.com/verove-jordan/astronomy/internal/routing"
	"github.com/verove-jordan/astronomy/internal/weather"
)

// Scanner samples sky brightness over an area (implemented by *lightpollution.Provider).
type Scanner interface {
	ScanArea(ctx context.Context, bbox lightpollution.Bbox, nx, ny int) []lightpollution.Cell
}

// HorizonScorer scores a site's horizon openness (implemented by *elevation.Provider).
type HorizonScorer interface {
	Horizon(ctx context.Context, lat, lon float64) (elevation.Horizon, string)
}

// Router resolves road distance + time from the observer to each candidate (implemented by
// *routing.Provider). It is display-only — it never changes the ranking — and soft-fails to the
// straight-line distance when routing is unavailable.
type Router interface {
	DriveMatrix(ctx context.Context, srcLat, srcLon float64, dstLats, dstLons []float64) ([]routing.Drive, string)
}

// ScoreConfig tunes the blended place score. DarkWeight is the darkness share (openness gets the rest).
// SouthWeight blends a south-weighted openness into the openness term (the low southern sky matters most
// for N-hemisphere deep-sky). MaxSouthBlockDeg (0 = disabled) heavily penalises a site whose southern
// horizon is blocked above that angle. WeatherWeight is the share taken by the selected night's
// forecast, with the terrain terms sharing the remainder in their usual proportion; 0 (or no forecast)
// reproduces the historical 0.6·darkness + 0.4·openness score exactly.
type ScoreConfig struct {
	DarkWeight       float64
	SouthWeight      float64
	MaxSouthBlockDeg float64
	WeatherWeight    float64
}

// defaultScoreConfig is the historical 0.6 darkness / 0.4 openness split, plus the default share the
// forecast takes once a search asks for weather. The weather term only ever engages when a Query opts
// in AND a forecast provider is attached, so this default cannot change a terrain-only finder.
func defaultScoreConfig() ScoreConfig { return ScoreConfig{DarkWeight: 0.6, WeatherWeight: 0.3} }

// maxWeatherWeight keeps the terrain terms from being drowned out: past this the finder stops being a
// dark-sky finder and becomes a cloud map with a location list.
const maxWeatherWeight = 0.8

// Option configures a Finder at construction, keeping New's positional signature stable for existing callers.
type Option func(*Finder)

// WithScore sets the blended-score weights (clamped to sane ranges).
func WithScore(sc ScoreConfig) Option {
	return func(f *Finder) {
		sc.DarkWeight = clamp01(sc.DarkWeight)
		sc.SouthWeight = clamp01(sc.SouthWeight)
		if sc.MaxSouthBlockDeg < 0 {
			sc.MaxSouthBlockDeg = 0
		}
		sc.WeatherWeight = clampf(sc.WeatherWeight, 0, maxWeatherWeight)
		f.score = sc
	}
}

// WithRouter attaches a driving-distance provider (display-only; nil = straight-line distance only).
func WithRouter(r Router) Option { return func(f *Finder) { f.router = r } }

// WithWeather attaches the night-forecast provider and the sampling budget for one area pass (nil =
// the finder ranks on terrain alone, exactly as it did before weather existed).
func WithWeather(w NightScanner, probes int) Option {
	return func(f *Finder) {
		f.wx = w
		f.probes = clampInt(probes, 4, 400)
	}
}

// Finder finds the darkest, most open observing sites in a region.
type Finder struct {
	lp       Scanner
	elev     HorizonScorer
	maxCells int
	horizonN int // how many top candidates get horizon scoring
	score    ScoreConfig
	router   Router
	wx       NightScanner
	probes   int // forecast sample points per area pass
}

// New builds a Finder. elev may be nil (horizon scoring then degrades to a dark-only ranking). Score weights
// default to the historical 0.6/0.4 blend; pass WithScore/WithRouter to override.
func New(lp Scanner, elev HorizonScorer, maxCells, horizonN int, opts ...Option) *Finder {
	if maxCells < 4 {
		maxCells = 4000
	}
	if horizonN < 0 {
		horizonN = 0
	}
	f := &Finder{lp: lp, elev: elev, maxCells: maxCells, horizonN: horizonN, score: defaultScoreConfig()}
	for _, o := range opts {
		o(f)
	}
	return f
}

// Query parameterizes one area search.
type Query struct {
	Bbox      lightpollution.Bbox
	MaxBortle int
	Limit     int
	Horizon   bool    // evaluate horizon openness for the top candidates
	ObsLat    float64 // observer location, for the distance column
	ObsLon    float64
	ObsSet    bool // the observer location was explicitly provided — driving distance is computed only then
	// NightIndex selects which night to rank for: 0 = tonight, 1 = tomorrow night, and so on.
	NightIndex int
	// Weather turns the forecast term on. WeatherWeight overrides the configured share for this one
	// search (0 = use the configured default) so the UI can offer a darkest-to-clearest slider.
	Weather       bool
	WeatherWeight float64
}

// Candidate is one ranked dark site.
type Candidate struct {
	Lat        float64               `json:"lat"`
	Lon        float64               `json:"lon"`
	SQM        float64               `json:"sqm"`
	Bortle     int                   `json:"bortle"`
	BortleF    float64               `json:"bortle_f"`            // the class, continuous — 3.7 vs 3.2 inside one class
	DistanceKm float64               `json:"distance_km"`         // straight-line (great-circle) distance
	DriveKm    float64               `json:"drive_km,omitempty"`  // road distance from the observer (0 = not computed)
	DriveMin   float64               `json:"drive_min,omitempty"` // estimated driving time, minutes (0 = not computed)
	Score      float64               `json:"score"`
	Sub        SubScores             `json:"sub"`
	ElevationM *float64              `json:"elevation_m,omitempty"`
	Horizon    *elevation.Horizon    `json:"horizon,omitempty"`
	Weather    *weather.NightOutlook `json:"weather,omitempty"`
}

// SubScores are the normalised 0..1 terms behind Score. They are returned so the UI can re-blend the
// ranking as the user moves the darkest-to-clearest slider without issuing another search — and
// therefore without spending another forecast call on an answer it already has.
type SubScores struct {
	Darkness float64 `json:"darkness"`
	Openness float64 `json:"openness"`
	Weather  float64 `json:"weather"`
	// WeatherKnown distinguishes "the forecast says this night is hopeless" from "there is no
	// forecast". Only the first may lower a score.
	WeatherKnown bool `json:"weather_known"`
}

// Result is a completed area search.
type Result struct {
	Count         int         `json:"count"`
	CellsScanned  int         `json:"cells_scanned"`
	Night         *Night      `json:"night,omitempty"`
	WeatherWeight float64     `json:"weather_weight"`
	Candidates    []Candidate `json:"candidates"`
	Warnings      []string    `json:"warnings"`
}

// Find scans the area, keeps the cells at/below MaxBortle, forecasts the chosen night across the whole
// box, shortlists on darkness + weather, evaluates horizon openness and a precise forecast for the
// shortlist, and orders by the blended score.
func (f *Finder) Find(ctx context.Context, q Query) *Result {
	if q.MaxBortle <= 0 {
		q.MaxBortle = 4
	}
	if q.Limit <= 0 {
		q.Limit = 12
	}
	sc := f.scoreFor(q)
	res := &Result{Candidates: []Candidate{}, Warnings: []string{}, WeatherWeight: sc.WeatherWeight}

	nx, ny := f.gridDims(q.Bbox)
	cells := f.lp.ScanArea(ctx, q.Bbox, nx, ny)
	res.CellsScanned = len(cells)

	cand := make([]Candidate, 0, len(cells))
	for _, c := range cells {
		if c.Bortle > q.MaxBortle {
			continue
		}
		cand = append(cand, Candidate{
			Lat: c.Lat, Lon: c.Lon, SQM: c.SQM, Bortle: c.Bortle, BortleF: c.BortleF,
			DistanceKm: round1(haversineKm(q.ObsLat, q.ObsLon, c.Lat, c.Lon)),
		})
	}
	if len(cand) == 0 {
		res.Warnings = append(res.Warnings, "no locations at or below the chosen Bortle in this area")
		return res
	}

	// The coarse pass runs over EVERY surviving cell, before the shortlist is cut, so a spot that is a
	// little brighter but genuinely clear can still make it into the results.
	var probes probeSet
	weatherOn := sc.WeatherWeight > 0 && f.wx != nil
	if weatherOn {
		night := planNight(q.NightIndex, q.Bbox)
		res.Night = &night
		probes = f.scanArea(ctx, q, night, cand, res)
	}

	rank(cand, sc)
	if len(cand) > q.Limit {
		cand = cand[:q.Limit]
	}

	if q.Horizon && f.elev != nil {
		f.evalHorizon(ctx, cand, res)
	}
	if weatherOn && len(probes.outlooks) > 0 {
		f.refineFinalists(ctx, cand, *res.Night, probes, res)
	}
	rank(cand, sc)
	if weatherOn {
		res.Night.Confidence = f.nightConfidence(ctx, q, cand)
	}

	// Driving distance is meaningful only from the user's real location, so require it to be explicitly set
	// (never the server's default site — that would show a drive from the wrong origin).
	if f.router != nil && q.ObsSet {
		f.evalRoutes(ctx, cand, q, res)
	}

	res.Candidates = cand
	res.Count = len(cand)
	return res
}

// scoreFor resolves the weights for one search: the configured defaults, with the request's weather
// weight overriding when the UI sent one and the term switched off entirely when weather is not wanted.
func (f *Finder) scoreFor(q Query) ScoreConfig {
	sc := f.score
	if !q.Weather {
		sc.WeatherWeight = 0
		return sc
	}
	if q.WeatherWeight > 0 {
		sc.WeatherWeight = clampf(q.WeatherWeight, 0, maxWeatherWeight)
	}
	return sc
}

// nightConfidence asks the ensemble how much it agrees about this night AT THE SPOT BEING RECOMMENDED,
// which is why it runs after the ranking rather than before it. Cloud can go from nothing to overcast
// inside one drawn box (measured: 0% at one corner, 100% at the centre, 45 km apart), so a confidence
// figure taken at the box centre would describe somewhere the user is not being sent. One call per
// search, and it never blocks or changes the ranking.
func (f *Finder) nightConfidence(ctx context.Context, q Query, cand []Candidate) *weather.Confidence {
	lat, lon := boxCenter(q.Bbox)
	if len(cand) > 0 {
		lat, lon = cand[0].Lat, cand[0].Lon
	}
	night := planNight(q.NightIndex, q.Bbox)
	return f.wx.NightConfidence(ctx, lat, lon, night.StartMs, night.EndMs)
}

// rank scores every candidate and sorts best-first. Ties keep their previous order, so an unscored
// dimension never reshuffles the list.
func rank(cand []Candidate, sc ScoreConfig) {
	for i := range cand {
		cand[i].Sub = subScores(cand[i], sc)
		cand[i].Score = round2(blend(cand[i].Sub, sc))
	}
	sort.SliceStable(cand, func(i, j int) bool { return cand[i].Score > cand[j].Score })
}

// gridDims picks the scan grid from the bbox: ~1 km steps, capped at maxCells total cells.
func (f *Finder) gridDims(b lightpollution.Bbox) (int, int) {
	const stepDeg = 0.01 // ≈1 km, near the atlas/tile resolution
	nx := int((b.MaxLon-b.MinLon)/stepDeg) + 1
	ny := int((b.MaxLat-b.MinLat)/stepDeg) + 1
	if nx < 2 {
		nx = 2
	}
	if ny < 2 {
		ny = 2
	}
	if nx*ny > f.maxCells {
		scale := math.Sqrt(float64(f.maxCells) / float64(nx*ny))
		nx = max(2, int(float64(nx)*scale))
		ny = max(2, int(float64(ny)*scale))
	}
	return nx, ny
}

// evalHorizon scores the top candidates' horizon concurrently (each goroutine writes only its own
// index, so the shared slice is race-free).
func (f *Finder) evalHorizon(ctx context.Context, cand []Candidate, res *Result) {
	n := len(cand)
	if f.horizonN > 0 && n > f.horizonN {
		n = f.horizonN
	}
	warns := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h, warn := f.elev.Horizon(ctx, cand[i].Lat, cand[i].Lon)
			if warn != "" {
				warns[i] = warn
				return
			}
			elev := h.ElevationM
			horizon := h
			cand[i].ElevationM = &elev
			cand[i].Horizon = &horizon
		}(i)
	}
	wg.Wait()
	for _, w := range warns {
		if w != "" {
			res.Warnings = append(res.Warnings, w)
			break
		}
	}
}

// evalRoutes fills DriveKm/DriveMin for the candidates from the routing provider (one call). It is
// display-only and soft-fails: on any routing error the candidates keep their straight-line DistanceKm.
func (f *Finder) evalRoutes(ctx context.Context, cand []Candidate, q Query, res *Result) {
	lats := make([]float64, len(cand))
	lons := make([]float64, len(cand))
	for i := range cand {
		lats[i], lons[i] = cand[i].Lat, cand[i].Lon
	}
	drives, warn := f.router.DriveMatrix(ctx, q.ObsLat, q.ObsLon, lats, lons)
	if warn != "" {
		res.Warnings = append(res.Warnings, warn)
	}
	for i := range cand {
		if i < len(drives) && drives[i].OK {
			cand[i].DriveKm = round1(drives[i].DistanceKm)
			cand[i].DriveMin = math.Round(drives[i].DurationMin)
		}
	}
}

// scoreCandidate blends darkness, horizon openness and the night's forecast per ScoreConfig. When the
// horizon was not evaluated, openness is treated as neutral (1) so the ordering falls back to darkness
// alone. With the default config (DarkWeight 0.6, SouthWeight 0, MaxSouthBlockDeg 0, WeatherWeight 0)
// this is the historical 0.6·dark + 0.4·open score.
func scoreCandidate(c Candidate, sc ScoreConfig) float64 {
	return blend(subScores(c, sc), sc)
}

// subScores computes the normalised 0..1 terms behind a candidate's score.
func subScores(c Candidate, sc ScoreConfig) SubScores {
	s := SubScores{
		Darkness: clamp01((c.SQM - 18.0) / (22.0 - 18.0)),
		Openness: 1.0,
	}
	if c.Horizon != nil {
		open := c.Horizon.OpennessPct
		if sc.SouthWeight > 0 {
			open = (1-sc.SouthWeight)*open + sc.SouthWeight*c.Horizon.SouthOpennessPct
		}
		s.Openness = clamp01(open / 100.0)
		if sc.MaxSouthBlockDeg > 0 && c.Horizon.SouthObstructionDeg > sc.MaxSouthBlockDeg {
			s.Openness = 0 // southern sky blocked past the gate — unusable for deep-sky
		}
	}
	if c.Weather != nil && c.Weather.Known() {
		s.Weather = clamp01(c.Weather.Score / 100)
		s.WeatherKnown = true
	}
	return s
}

// blend combines the normalised terms per the configured weights. Weather takes its share off the top
// and the terrain terms keep their relative proportion in what remains — so moving the slider changes
// how much the sky matters without also changing how darkness trades against horizon.
func blend(s SubScores, sc ScoreConfig) float64 {
	terrain := sc.DarkWeight*s.Darkness + (1-sc.DarkWeight)*s.Openness
	if sc.WeatherWeight <= 0 || !s.WeatherKnown {
		return terrain
	}
	return (1-sc.WeatherWeight)*terrain + sc.WeatherWeight*s.Weather
}

// haversineKm is the great-circle distance between two lat/lon points, in kilometres.
func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0
	rad := math.Pi / 180
	dlat := (lat2 - lat1) * rad
	dlon := (lon2 - lon1) * rad
	a := math.Sin(dlat/2)*math.Sin(dlat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dlon/2)*math.Sin(dlon/2)
	return r * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func clamp01(x float64) float64 { return clampf(x, 0, 1) }

func clampf(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }
func round2(x float64) float64 { return math.Round(x*100) / 100 }
