package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/config"
)

const userAgent = "AstroStack/1.0 (+https://github.com/verove-jordan/astronomy)"

// Provider fetches and caches astronomy weather. It is safe for concurrent use, and every public method
// soft-fails: a value (possibly partial or stale) is always returned together with an optional warning.
type Provider struct {
	http          *http.Client
	openMeteoURL  string
	airQualityURL string
	sevenTimerURL string
	swpcURL       string
	gridRadius    float64
	gridSize      int
	ttl           time.Duration
	cacheDir      string

	mu       sync.Mutex
	memoFc   map[string]cachedForecast
	memoGrid map[string]cachedGrid
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
	return &Provider{
		http:          &http.Client{Timeout: 12 * time.Second},
		openMeteoURL:  cfg.WeatherOpenMeteoURL,
		airQualityURL: cfg.WeatherAirQualityURL,
		sevenTimerURL: cfg.WeatherSevenTimerURL,
		swpcURL:       cfg.WeatherSWPCURL,
		gridRadius:    radius,
		gridSize:      size,
		ttl:           ttl,
		cacheDir:      cache,
		memoFc:        map[string]cachedForecast{},
		memoGrid:      map[string]cachedGrid{},
	}
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
		if r, err := p.fetchOpenMeteoPoint(ctx, lat, lon); err == nil {
			om, omOK = r, true
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
		// Open-Meteo is the backbone (the hourly timeline). Without it, serve any stale cache, else warn.
		if f, ok := p.anyForecast(key); ok {
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

// Grid returns the regional cloud cube for the animated map overlay (chunked Open-Meteo multi-point
// GETs, see fetchOpenMeteoGrid).
func (p *Provider) Grid(ctx context.Context, lat, lon, radiusDeg float64, layers []string) (Grid, string) {
	if len(layers) == 0 {
		layers = []string{"clouds"}
	}
	layers = expandGridLayers(layers)
	geom := p.snapGrid(lat, lon, p.gridRadiusFor(radiusDeg))
	key := gridKey(geom, layers)
	if g, ok := p.cachedGrid(key); ok {
		return g, ""
	}
	lats, lons := geom.points()
	resp, err := p.fetchOpenMeteoGrid(ctx, lats, lons, omGridVars(layers))
	if err != nil || len(resp) == 0 {
		if g, ok := p.anyGrid(key); ok {
			return g, "live cloud map unavailable — showing the last cached frames"
		}
		return Grid{BBox: geom.bbox(), Nx: geom.nx, Ny: geom.ny, Layers: map[string][][]float32{}, IssuedMs: nowMs()}, "cloud map currently unavailable"
	}
	g := assembleGrid(resp, geom.nx, geom.ny, geom.bbox(), layers)
	p.storeGrid(key, g)
	return g, ""
}

// expandGridLayers expands composite layer names into every concrete layer the cube must carry:
// "clouds" also yields its per-altitude bands ("clouds_low"/"clouds_mid"/"clouds_high"), which the map
// composites into an intensity-true cloud raster. Deduplicated and order-stable, so the expanded list
// doubles as a deterministic cache key.
func expandGridLayers(layers []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(layers)+3)
	add := func(l string) {
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	for _, l := range layers {
		add(l)
		if l == "clouds" {
			add("clouds_low")
			add("clouds_mid")
			add("clouds_high")
		}
	}
	return out
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
	gridRadiusMinDeg = 0.5  // a very zoomed-in view still fetches a usable box
	gridRadiusMaxDeg = 24   // cap a zoomed-out request (the step coarsens, keeping the point count bounded)
	gridStepMinDeg   = 0.1  // finest cell ≈ Open-Meteo's native resolution; finer would only interpolate
	gridStepMaxDeg   = 8    // coarsest cell, for a very zoomed-out view
	gridMarginFrac   = 0.5  // fetch 50% beyond the region half-span: a small pan needs no refetch, and adjacent
	//                         tile-block cubes overlap by ≥2 cells so the bicubic stays seamless across a block edge
	//                         (both cubes snap to the same global lattice, so overlap cells are identical)
	maxGridPoints    = 2500 // fetch-budget guard (≈7 chunked GETs); the step coarsens if a box exceeds it
)

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
		return fmt.Errorf("weather upstream status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func siteKey(lat, lon float64) string { return fmt.Sprintf("%+.2f_%+.2f", lat, lon) }

// gridCacheVersion namespaces the grid cache; bump it when a layer's semantics change (e.g. precip went
// from rain amount in mm to chance of precipitation in %) or the grid geometry changes, so stale cached
// cubes are ignored. v3 = default grid size 16→22; v4 = per-viewport radius in the key; v5 = cloud
// bands + denser chunked grid; v6 = fixed global-lattice snap (stable under pan/zoom) + viewport margin.
const gridCacheVersion = 6

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
