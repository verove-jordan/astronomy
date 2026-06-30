// Package elevation resolves ground elevation and, from it, how open a site's horizon is — the second
// half of the dark-sky finder ("the very best spots have a clear ~360° horizon"). It uses the keyless
// Open-Meteo Elevation API (batches up to 100 coordinates per request) and, like the weather/light-
// pollution providers, soft-fails: callers get a value plus an optional warning, never a hard abort.
package elevation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/config"
)

const (
	userAgent = "AstroStack/1.0 (+https://github.com/verove-jordan/astronomy)"
	maxBatch  = 100 // Open-Meteo elevation accepts at most 100 coordinates per request
)

// Provider fetches elevations and scores horizon openness. Safe for concurrent use.
type Provider struct {
	http     *http.Client
	apiURL   string
	ttl      time.Duration
	cacheDir string

	naz     int       // azimuth samples around the horizon
	radii   []float64 // sample distances (metres) along each azimuth
	openDeg float64   // an azimuth counts as "open" when its obstruction angle is below this

	mu   sync.Mutex
	memo map[string]cachedHorizon // in-process horizon cache, keyed by rounded lat/lon
}

// New builds a Provider with its on-disk cache under the work dir (falling back to the user cache dir).
func New(cfg *config.Config) *Provider {
	cache := filepath.Join(cfg.WorkDir, "cache", "elevation")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		if ucd, e2 := os.UserCacheDir(); e2 == nil {
			cache = filepath.Join(ucd, "astrostack", "elevation")
			_ = os.MkdirAll(cache, 0o755)
		}
	}
	ttl := time.Duration(cfg.ElevationCacheTTLHours) * time.Hour
	if ttl <= 0 {
		ttl = 720 * time.Hour
	}
	naz := cfg.HorizonAzimuths
	if naz < 4 {
		naz = 12
	}
	radii := cfg.HorizonRadiiM
	if len(radii) == 0 {
		radii = []float64{1000, 2500}
	}
	openDeg := cfg.HorizonOpenThresholdDeg
	if openDeg <= 0 {
		openDeg = 3
	}
	return &Provider{
		http:     &http.Client{Timeout: 12 * time.Second},
		apiURL:   cfg.ElevationAPIURL,
		ttl:      ttl,
		cacheDir: cache,
		naz:      naz,
		radii:    radii,
		openDeg:  openDeg,
		memo:     map[string]cachedHorizon{},
	}
}

// Elevations returns the ground elevation (metres) of each (lats[i], lons[i]), batching ≤100 per call.
func (p *Provider) Elevations(ctx context.Context, lats, lons []float64) ([]float64, error) {
	if len(lats) != len(lons) {
		return nil, fmt.Errorf("elevation: %d lats vs %d lons", len(lats), len(lons))
	}
	if p.apiURL == "" {
		return nil, fmt.Errorf("elevation: no API configured")
	}
	out := make([]float64, 0, len(lats))
	for start := 0; start < len(lats); start += maxBatch {
		end := min(start+maxBatch, len(lats))
		batch, err := p.fetchBatch(ctx, lats[start:end], lons[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
	}
	return out, nil
}

type elevResp struct {
	Elevation []float64 `json:"elevation"`
}

func (p *Provider) fetchBatch(ctx context.Context, lats, lons []float64) ([]float64, error) {
	url := fmt.Sprintf("%s?latitude=%s&longitude=%s", p.apiURL, joinFloats(lats), joinFloats(lons))
	var r elevResp
	if err := p.getJSON(ctx, url, &r); err != nil {
		return nil, err
	}
	if len(r.Elevation) != len(lats) {
		return nil, fmt.Errorf("elevation: got %d values for %d points", len(r.Elevation), len(lats))
	}
	return r.Elevation, nil
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
		return fmt.Errorf("elevation upstream status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func joinFloats(fs []float64) string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = strconv.FormatFloat(f, 'f', 5, 64)
	}
	return strings.Join(parts, ",")
}
