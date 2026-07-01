package weather

import (
	"context"
	"encoding/json"
	"fmt"
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

// Grid returns the regional cloud cube for the animated map overlay (one Open-Meteo multi-point call).
func (p *Provider) Grid(ctx context.Context, lat, lon float64, layers []string) (Grid, string) {
	if len(layers) == 0 {
		layers = []string{"clouds"}
	}
	key := gridKey(lat, lon, layers)
	if g, ok := p.cachedGrid(key); ok {
		return g, ""
	}
	lats, lons, nx, ny, bbox := p.gridPoints(lat, lon)
	resp, err := p.fetchOpenMeteoGrid(ctx, lats, lons, omGridVars(layers))
	if err != nil || len(resp) == 0 {
		if g, ok := p.anyGrid(key); ok {
			return g, "live cloud map unavailable — showing the last cached frames"
		}
		return Grid{BBox: bbox, Nx: nx, Ny: ny, Layers: map[string][][]float32{}, IssuedMs: nowMs()}, "cloud map currently unavailable"
	}
	g := assembleGrid(resp, nx, ny, bbox, layers)
	p.storeGrid(key, g)
	return g, ""
}

// gridPoints builds the lat/lon sample grid (row-major, north→south, west→east) and its bounding box.
func (p *Provider) gridPoints(lat, lon float64) (lats, lons []float64, nx, ny int, bbox [4]float64) {
	nx, ny = p.gridSize, p.gridSize
	r := p.gridRadius
	west, east, south, north := lon-r, lon+r, lat-r, lat+r
	bbox = [4]float64{west, south, east, north}
	lats = make([]float64, 0, nx*ny)
	lons = make([]float64, 0, nx*ny)
	for j := 0; j < ny; j++ {
		la := north - (north-south)*float64(j)/float64(ny-1)
		for i := 0; i < nx; i++ {
			lo := west + (east-west)*float64(i)/float64(nx-1)
			lats = append(lats, clampf(la, -90, 90))
			lons = append(lons, normLon(lo))
		}
	}
	return lats, lons, nx, ny, bbox
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
		return fmt.Errorf("weather upstream status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func siteKey(lat, lon float64) string { return fmt.Sprintf("%+.2f_%+.2f", lat, lon) }

// gridCacheVersion namespaces the grid cache; bump it when a layer's semantics change (e.g. precip went
// from rain amount in mm to chance of precipitation in %) or the grid resolution changes, so stale
// cached cubes are ignored. v3 = default grid size 16→22.
const gridCacheVersion = 3

func gridKey(lat, lon float64, layers []string) string {
	s := fmt.Sprintf("v%d_%+.1f_%+.1f", gridCacheVersion, lat, lon)
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
