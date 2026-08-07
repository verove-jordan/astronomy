package weather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/verove-jordan/astronomy/internal/config"
)

const userAgent = "AstroStack/1.0 (+https://github.com/verove-jordan/astronomy)"

// Provider fetches and caches astronomy weather. It is safe for concurrent use, and every public method
// soft-fails: a value (possibly partial or stale) is always returned together with an optional warning.
type Provider struct {
	http            *http.Client
	openMeteoURL    string
	openMeteoModels string // optional models= pin (empty = best_match auto-selection)
	airQualityURL   string
	sevenTimerURL   string
	swpcURL         string
	ensembleURL     string
	ensembleModel   string
	forecastDays    int
	gridRadius      float64
	gridSize        int
	ttl             time.Duration
	cacheDir        string

	mu        sync.Mutex
	memoFc    map[string]cachedForecast
	memoGrid  map[string]cachedGrid
	memoNight map[string]cachedNight
	// rlUntil is the upstream-429 circuit breaker: until this instant no Open-Meteo fetch is attempted
	// (each would fail and burn more of the minutely/daily quota). gridFail is a short per-cube negative
	// memo so a just-failed cube isn't retried by every tile request in a Leaflet burst.
	rlUntil  time.Time
	gridFail map[string]time.Time
	// flight collapses concurrent Grid calls for the same cube key into ONE upstream fetch — a viewport
	// fires dozens of tile requests at once, and without this each cache miss stampeded its own fetch.
	flight singleflight.Group
}

// New builds a Provider, placing its on-disk cache under the work dir (falling back to the user cache).
func New(cfg *config.Config) *Provider {
	cache := filepath.Join(cfg.WorkDir, "cache", "weather")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		if ucd, e2 := os.UserCacheDir(); e2 == nil {
			cache = filepath.Join(ucd, "astrostack", "weather")
			_ = os.MkdirAll(cache, 0o755)
		}
	}
	ttl := time.Duration(cfg.WeatherCacheTTLMin) * time.Minute
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	size := cfg.WeatherGridSize
	if size < 4 {
		size = 16
	}
	radius := cfg.WeatherGridRadiusDeg
	if radius <= 0 {
		radius = 4
	}
	days := clampInt(cfg.WeatherForecastDays, 1, maxForecastDays)
	return &Provider{
		http:            &http.Client{Timeout: 12 * time.Second},
		openMeteoURL:    cfg.WeatherOpenMeteoURL,
		openMeteoModels: cfg.WeatherOpenMeteoModels,
		airQualityURL:   cfg.WeatherAirQualityURL,
		sevenTimerURL:   cfg.WeatherSevenTimerURL,
		swpcURL:         cfg.WeatherSWPCURL,
		ensembleURL:     cfg.WeatherEnsembleURL,
		ensembleModel:   cfg.WeatherEnsembleModel,
		forecastDays:    days,
		gridRadius:      radius,
		gridSize:        size,
		ttl:             ttl,
		cacheDir:        cache,
		memoFc:          map[string]cachedForecast{},
		memoGrid:        map[string]cachedGrid{},
		memoNight:       map[string]cachedNight{},
		gridFail:        map[string]time.Time{},
	}
}

// ForecastDays is how many days ahead the per-site timeline reaches — the number of nights the planner
// may offer.
func (p *Provider) ForecastDays() int { return p.forecastDays }

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Forecast assembles the per-site hourly astronomy-weather timeline. It never hard-errors: the feeds
// are fetched concurrently and any that fail are simply omitted (their data left at zero / "unknown").
// The returned warning is non-empty only when the result is degraded (stale cache or backbone down).
func (p *Provider) Forecast(ctx context.Context, lat, lon float64) (SiteForecast, string) {
	key := siteKey(lat, lon)
	if f, ok := p.cachedForecast(key); ok {
		return f, ""
	}

	var (
		om               omResponse
		aq               aqResponse
		st               stResponse
		omOK, aqOK, stOK bool
		kpNow, kpMax     float64
		kpMs             int64
		kpOK             bool
	)
	// Independent feeds → fetch concurrently. Each goroutine writes only its own variables, and a
	// failure leaves that source's "ok" flag false (soft-fail) rather than aborting the others.
	var wg sync.WaitGroup
	wg.Add(4)
	go func() {
		defer wg.Done()
		if p.rateLimited() {
			return // breaker open: a fetch is guaranteed to 429 — fall through to the stale path below
		}
		if r, err := p.fetchOpenMeteoPoint(ctx, lat, lon); err == nil {
			om, omOK = r, true
		} else if errors.Is(err, ErrRateLimited) {
			p.tripRateLimit()
		}
	}()
	go func() {
		defer wg.Done()
		if r, err := p.fetchAirQuality(ctx, lat, lon); err == nil {
			aq, aqOK = r, true
		}
	}()
	go func() {
		defer wg.Done()
		if r, err := p.fetchSevenTimer(ctx, lat, lon); err == nil {
			st, stOK = r, true
		}
	}()
	go func() {
		defer wg.Done()
		if n, m, ms, err := p.fetchKp(ctx); err == nil {
			kpNow, kpMax, kpMs, kpOK = n, m, ms, true
		}
	}()
	wg.Wait()

	if !omOK {
		// Open-Meteo is the backbone (the hourly timeline). Without it, serve a recent-enough cache, else warn.
		if f, ok := p.staleForecast(key); ok {
			return f, "live weather unavailable — showing the last cached forecast"
		}
		return SiteForecast{Lat: lat, Lon: lon, IssuedMs: nowMs(), Sources: []string{}}, "weather data is currently unavailable"
	}

	f := assembleForecast(lat, lon, om, aq, aqOK, st, stOK)
	if kpOK {
		f.Kp = &KpInfo{Now: kpNow, Max: kpMax, Aurora: auroraChance(kpMax, lat), IssuedMs: kpMs}
		f.Sources = append(f.Sources, "NOAA SWPC")
	}
	p.storeForecast(key, f)
	return f, ""
}

// gridSupersetLayers is the FIXED layer set every cube carries. One cube (one Open-Meteo fetch) serves
// every map metric — total clouds + the altitude bands, humidity, precipitation chance and the fog/dew
// spread — so the frames endpoint and every tile metric share ONE cache entry. The v6 design keyed the
// cube on the requested layers, so each metric fetched its own multi-hundred-point cube and the free-tier
// minutely quota starved (429 → silently blank layers). 8 hourly variables keeps the per-location call
// weight at 1× on Open-Meteo's free tier (weight rises past 10 variables).
var gridSupersetLayers = []string{"clouds", "clouds_low", "clouds_mid", "clouds_high", "humidity", "precip", "dewspread"}

// Grid returns the regional weather cube for the animated map overlay (chunked Open-Meteo multi-point
// GETs, see fetchOpenMeteoGrid). The layers parameter is advisory: the cube always carries
// gridSupersetLayers (see there), so any requested metric is served from the same shared cube.
func (p *Provider) Grid(ctx context.Context, lat, lon, radiusDeg float64, _ []string) (Grid, string) {
	layers := gridSupersetLayers
	geom := p.snapGrid(lat, lon, p.gridRadiusFor(radiusDeg))
	key := gridKey(geom, layers)
	if g, ok := p.cachedGrid(key); ok {
		return g, ""
	}
	if p.rateLimited() || p.recentlyFailed(key) {
		return p.staleOrEmpty(geom, key)
	}
	v, err, _ := p.flight.Do(key, func() (any, error) {
		// Detached from the caller: a browser aborting one tile request must not cancel the shared
		// fetch every other tile of the burst is waiting on.
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gridFetchTimeout)
		defer cancel()
		lats, lons := geom.points()
		log.Printf("weather: grid fetch %s (%d points)", key, len(lats))
		resp, err := p.fetchOpenMeteoGrid(fctx, lats, lons, omGridVars(layers))
		if err != nil {
			return nil, err
		}
		if len(resp) == 0 {
			return nil, errors.New("empty upstream response")
		}
		g := assembleGrid(resp, geom.nx, geom.ny, geom.bbox(), layers)
		p.storeGrid(key, g)
		return g, nil
	})
	if err != nil {
		log.Printf("weather: grid fetch %s failed (%d points): %v", key, geom.nx*geom.ny, err)
		if errors.Is(err, ErrRateLimited) {
			p.tripRateLimit()
		}
		p.noteFailure(key)
		return p.staleOrEmpty(geom, key)
	}
	return v.(Grid), ""
}

// staleOrEmpty serves the last good cube for key (age-bounded, see staleGrid) with a degraded warning,
// else an empty cube — the map keeps showing the previous frames instead of going silently blank.
func (p *Provider) staleOrEmpty(geom gridGeom, key string) (Grid, string) {
	if g, ok := p.staleGrid(key); ok {
		return g, "live cloud map unavailable — showing the last cached frames"
	}
	return Grid{BBox: geom.bbox(), Nx: geom.nx, Ny: geom.ny, Layers: map[string][][]float32{}, IssuedMs: nowMs()}, "cloud map currently unavailable"
}

// rateLimited reports whether the upstream-429 cooldown is still open (no Open-Meteo calls allowed).
func (p *Provider) rateLimited() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return time.Now().Before(p.rlUntil)
}

// tripRateLimit opens the breaker: Open-Meteo's minutely quota resets every minute, so fetches inside
// the cooldown are guaranteed 429s that only burn more of the daily budget. Logged once per trip.
func (p *Provider) tripRateLimit() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if time.Now().Before(p.rlUntil) {
		return
	}
	p.rlUntil = time.Now().Add(rateLimitCooldown)
	log.Printf("weather: upstream rate-limited — pausing Open-Meteo fetches for %s", rateLimitCooldown)
}

// recentlyFailed reports whether this cube failed within the negative-memo window, so a Leaflet tile
// burst doesn't re-attempt a just-failed fetch dozens of times. noteFailure records such a failure.
func (p *Provider) recentlyFailed(key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return time.Since(p.gridFail[key]) < gridFailMemo
}

func (p *Provider) noteFailure(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.gridFail[key] = time.Now()
}

// gridGeom is the regional sample lattice: a box snapped OUTWARD to integer multiples of a fixed cell
// size (step) measured from the (0,0) global origin. Because the lattice is globally fixed — not centred
// on the raw viewport — two overlapping viewports sample the SAME geographic points, so a location's
// value stays put as the map pans/zooms instead of drifting with a floating box (the old bug).
type gridGeom struct {
	west, south, east, north float64
	step                     float64
	nx, ny                   int
}

func (g gridGeom) bbox() [4]float64 { return [4]float64{g.west, g.south, g.east, g.north} }

// points lists the lattice coordinates row-major (north→south, west→east) — the order assembleGrid and
// the map's row-major cube expect.
func (g gridGeom) points() (lats, lons []float64) {
	lats = make([]float64, 0, g.nx*g.ny)
	lons = make([]float64, 0, g.nx*g.ny)
	for j := 0; j < g.ny; j++ {
		la := clampf(g.north-g.step*float64(j), -90, 90)
		for i := 0; i < g.nx; i++ {
			lats = append(lats, la)
			lons = append(lons, normLon(g.west+g.step*float64(i)))
		}
	}
	return lats, lons
}

// snapGrid picks a fixed cell size for the viewport and snaps a margin-padded box around (lat,lon) onto
// the global step lattice. The step is the finest "nice" (1/2/5×10ⁿ) value that still spans the viewport
// in ~gridSize cells and is no finer than Open-Meteo's native resolution — a finer grid would only
// interpolate the same data. The margin fetches a little beyond the viewport so a small pan doesn't force
// a refetch. A point-budget guard coarsens the step if a padded box would ever exceed the fetch budget.
func (p *Provider) snapGrid(lat, lon, radius float64) gridGeom {
	step := clampf(niceStep(2*radius/float64(p.gridSize)), gridStepMinDeg, gridStepMaxDeg)
	geom := snapBox(lat, lon, radius*(1+gridMarginFrac), step)
	for geom.nx*geom.ny > maxGridPoints && step < gridStepMaxDeg {
		step = niceStep(step * 1.5)
		geom = snapBox(lat, lon, radius*(1+gridMarginFrac), step)
	}
	return geom
}

// snapBox builds the geom for a half-span and step: each edge floored/ceiled to a multiple of step from
// the global origin (0,0), so the same edges recur for any viewport covering the same ground.
func snapBox(lat, lon, halfSpan, step float64) gridGeom {
	west := math.Floor((lon-halfSpan)/step) * step
	east := math.Ceil((lon+halfSpan)/step) * step
	south := clampf(math.Floor((lat-halfSpan)/step)*step, -90, 90)
	north := clampf(math.Ceil((lat+halfSpan)/step)*step, -90, 90)
	nx := int(math.Round((east-west)/step)) + 1
	ny := int(math.Round((north-south)/step)) + 1
	if nx < 1 {
		nx = 1
	}
	if ny < 1 {
		ny = 1
	}
	return gridGeom{west: west, south: south, east: east, north: north, step: step, nx: nx, ny: ny}
}

// niceStep rounds a raw degree step UP to the next 1/2/5×10ⁿ value, so the grid uses a small, shared set
// of fixed cell sizes across zoom levels (0.1°, 0.2°, 0.5°, 1°, …) rather than an arbitrary viewport step.
func niceStep(raw float64) float64 {
	if raw <= 0 {
		return gridStepMinDeg
	}
	pow := math.Pow(10, math.Floor(math.Log10(raw)))
	for _, m := range []float64{1, 2, 5} {
		if raw <= m*pow {
			return m * pow
		}
	}
	return 10 * pow
}

// gridRadiusFor picks the half-span (deg) of the sample box: the caller's requested radius (from the
// map viewport), clamped to a sane range, or the configured default when none (≤0) is given.
func (p *Provider) gridRadiusFor(radiusDeg float64) float64 {
	if radiusDeg <= 0 {
		return p.gridRadius
	}
	return clampf(radiusDeg, gridRadiusMinDeg, gridRadiusMaxDeg)
}

const (
	gridRadiusMinDeg = 0.5 // a very zoomed-in view still fetches a usable box
	gridRadiusMaxDeg = 24  // cap a zoomed-out request (the step coarsens, keeping the point count bounded)
	gridStepMinDeg   = 0.1 // finest cell ≈ Open-Meteo's native resolution; finer would only interpolate
	gridStepMaxDeg   = 8   // coarsest cell, for a very zoomed-out view
	gridMarginFrac   = 0.5 // fetch 50% beyond the region half-span: a small pan needs no refetch, and adjacent
	//                         tile-block cubes overlap by ≥2 cells so the bicubic stays seamless across a block edge
	//                         (both cubes snap to the same global lattice, so overlap cells are identical)
	maxGridPoints = 400 // fetch-budget guard = ONE chunked GET ≈ ≤400 location-calls on Open-Meteo's
	//                        free tier (~600/min) — a single cube fetch must fit the minutely quota with
	//                        headroom. The step coarsens if a box exceeds it; the bicubic upsample keeps
	//                        the render smooth at the coarser lattice.
)

// Upstream-failure handling knobs (see Grid): the breaker window after a 429, the per-cube negative
// memo, how far past TTL a stale cube may still be served, and the detached shared-fetch budget.
const (
	rateLimitCooldown  = 70 * time.Second // Open-Meteo's minutely quota resets each minute; +10 s slack
	gridFailMemo       = 30 * time.Second // don't re-attempt a just-failed cube on every tile request
	staleGrace         = 6 * time.Hour    // stale cubes older than TTL+grace read as empty, not as live data
	forecastStaleGrace = 12 * time.Hour   // same bound for the per-site timeline (it spans several days, so it ages slower)
	gridFetchTimeout   = 60 * time.Second // sequential chunks × 12 s HTTP timeout fits comfortably
	nightFetchTimeout  = 60 * time.Second // night scans are chunked the same way as cubes
)

// ErrRateLimited marks an upstream HTTP 429 — callers open the fetch breaker (tripRateLimit) so the
// remaining minutely/daily quota isn't burned on calls that are guaranteed to fail.
var ErrRateLimited = errors.New("weather upstream rate-limited")

// omError is Open-Meteo's error envelope ({"error":true,"reason":"..."}); other feeds return plain
// non-200s and leave the reason empty.
type omError struct {
	Error  bool   `json:"error"`
	Reason string `json:"reason"`
}

func (p *Provider) getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// Failures used to be swallowed silently all the way up to a blank map — always name the
		// status (and Open-Meteo's reason, e.g. "Minutely API request limit exceeded") in the log.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		reason := ""
		var oe omError
		if json.Unmarshal(body, &oe) == nil && oe.Error {
			reason = oe.Reason
		}
		log.Printf("weather: upstream %s → %d %q", req.URL.Host, resp.StatusCode, reason)
		if resp.StatusCode == http.StatusTooManyRequests {
			return fmt.Errorf("status 429 (%s): %w", reason, ErrRateLimited)
		}
		return fmt.Errorf("weather upstream status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// forecastCacheVersion namespaces the per-site forecast cache, exactly as gridCacheVersion does for
// cubes. The point forecast had NO version until v1, so a change to its variable list or horizon
// silently reinterpreted every cached file. v1 = derived seeing inputs + boundary-layer height + a
// multi-night horizon.
const forecastCacheVersion = 1

// maxForecastDays is Open-Meteo's ceiling for the forecast endpoint. Skill collapses long before it;
// the knob exists so the horizon can be widened without touching code, not because 16 days is useful.
const maxForecastDays = 16

func siteKey(lat, lon float64) string {
	return fmt.Sprintf("v%d_%+.2f_%+.2f", forecastCacheVersion, lat, lon)
}

// gridCacheVersion namespaces the grid cache; bump it when a layer's semantics change (e.g. precip went
// from rain amount in mm to chance of precipitation in %) or the grid geometry changes, so stale cached
// cubes are ignored. v3 = default grid size 16→22; v4 = per-viewport radius in the key; v5 = cloud
// bands + denser chunked grid; v6 = fixed global-lattice snap (stable under pan/zoom) + viewport margin;
// v7 = one shared superset cube (frames + every metric, incl. dewspread) + 400-point budget.
const gridCacheVersion = 7

// gridKey keys the cache on the SNAPPED geometry (not the raw viewport centre), so every viewport that
// resolves to the same lattice box shares one cached cube — the mechanism that keeps values stable.
func gridKey(g gridGeom, layers []string) string {
	s := fmt.Sprintf("v%d_w%.3f_s%.3f_e%.3f_n%.3f_st%.3f",
		gridCacheVersion, g.west, g.south, g.east, g.north, g.step)
	for _, l := range layers {
		s += "_" + l
	}
	return s
}

func nowMs() int64 { return time.Now().UnixMilli() }

func normLon(lon float64) float64 {
	for lon > 180 {
		lon -= 360
	}
	for lon < -180 {
		lon += 360
	}
	return lon
}
