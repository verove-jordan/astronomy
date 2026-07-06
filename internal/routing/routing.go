// Package routing resolves the driving distance and time by road from the observer to candidate observing
// sites, so the dark-sky finder can show "how far to drive tonight" rather than a straight-line distance.
// It calls an OSRM routing server's table service (keyless public demo by default) and, like the other
// providers, soft-fails: callers get a per-destination result with OK=false plus an optional warning, never
// a hard error, so the finder simply falls back to the great-circle distance.
package routing

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

// Drive is the road distance + time from the observer to one destination. OK is false when routing was
// unavailable or the destination is unroutable (the caller then shows the straight-line distance).
type Drive struct {
	DistanceKm  float64
	DurationMin float64
	OK          bool
}

// Provider resolves driving distances. Safe for concurrent use.
type Provider struct {
	http     *http.Client
	baseURL  string
	ttl      time.Duration
	cacheDir string

	mu   sync.Mutex
	memo map[string]cachedDrive // in-process cache keyed by rounded src+dest
}

// New builds a Provider, placing its on-disk cache under the work dir (falling back to the user cache).
// An empty RoutingURL disables the provider (DriveMatrix then returns all-unresolved, no warning).
func New(cfg *config.Config) *Provider {
	cache := filepath.Join(cfg.WorkDir, "cache", "routing")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		if ucd, e2 := os.UserCacheDir(); e2 == nil {
			cache = filepath.Join(ucd, "astrostack", "routing")
			_ = os.MkdirAll(cache, 0o755)
		}
	}
	ttl := time.Duration(cfg.RoutingCacheTTLHours) * time.Hour
	if ttl <= 0 {
		ttl = 720 * time.Hour
	}
	return &Provider{
		http:     &http.Client{Timeout: 10 * time.Second},
		baseURL:  cfg.RoutingURL,
		ttl:      ttl,
		cacheDir: cache,
		memo:     map[string]cachedDrive{},
	}
}

// DriveMatrix returns the road distance + time from (srcLat, srcLon) to each destination, in one routing
// call for the uncached destinations. It never hard-errors: on failure the affected entries stay OK=false
// and a single warning is returned, so the finder falls back to the straight-line distance.
func (p *Provider) DriveMatrix(ctx context.Context, srcLat, srcLon float64, dstLats, dstLons []float64) ([]Drive, string) {
	out := make([]Drive, len(dstLats))
	if p.baseURL == "" || len(dstLats) == 0 || len(dstLats) != len(dstLons) {
		return out, ""
	}

	var missIdx []int
	for i := range dstLats {
		if d, ok := p.cached(srcLat, srcLon, dstLats[i], dstLons[i]); ok {
			out[i] = d
		} else {
			missIdx = append(missIdx, i)
		}
	}
	if len(missIdx) == 0 {
		return out, ""
	}

	mlats := make([]float64, len(missIdx))
	mlons := make([]float64, len(missIdx))
	for k, i := range missIdx {
		mlats[k], mlons[k] = dstLats[i], dstLons[i]
	}
	drives, err := p.fetchTable(ctx, srcLat, srcLon, mlats, mlons)
	if err != nil || len(drives) != len(missIdx) {
		return out, "driving distances unavailable — showing straight-line distance"
	}
	for k, i := range missIdx {
		out[i] = drives[k]
		if drives[k].OK {
			p.store(srcLat, srcLon, dstLats[i], dstLons[i], drives[k])
		}
	}
	return out, ""
}

// --- cache (in-process memo + disk JSON, keyed by ~100 m src+dest cells) ---

type cachedDrive struct {
	D  Drive `json:"d"`
	At int64 `json:"at"`
}

func (p *Provider) cached(srcLat, srcLon, dstLat, dstLon float64) (Drive, bool) {
	key := cacheKey(srcLat, srcLon, dstLat, dstLon)
	p.mu.Lock()
	c, ok := p.memo[key]
	p.mu.Unlock()
	if ok && p.fresh(c.At) {
		return c.D, true
	}
	if c, ok := p.readDisk(key); ok && p.fresh(c.At) {
		p.mu.Lock()
		p.memo[key] = c
		p.mu.Unlock()
		return c.D, true
	}
	return Drive{}, false
}

func (p *Provider) store(srcLat, srcLon, dstLat, dstLon float64, d Drive) {
	key := cacheKey(srcLat, srcLon, dstLat, dstLon)
	c := cachedDrive{D: d, At: time.Now().UnixMilli()}
	p.mu.Lock()
	p.memo[key] = c
	p.mu.Unlock()
	if b, err := json.Marshal(c); err == nil {
		_ = os.WriteFile(filepath.Join(p.cacheDir, key+".json"), b, 0o644)
	}
}

func (p *Provider) readDisk(key string) (cachedDrive, bool) {
	b, err := os.ReadFile(filepath.Join(p.cacheDir, key+".json"))
	if err != nil {
		return cachedDrive{}, false
	}
	var c cachedDrive
	if err := json.Unmarshal(b, &c); err != nil || c.At == 0 {
		return cachedDrive{}, false
	}
	return c, true
}

func (p *Provider) fresh(atMs int64) bool {
	return atMs > 0 && time.Since(time.UnixMilli(atMs)) < p.ttl
}

// cacheKey rounds to ~0.001° (~100 m): road distance between two fixed points is static, and the observer/
// candidate cells are stable enough that panning the map does not re-query the same route.
func cacheKey(srcLat, srcLon, dstLat, dstLon float64) string {
	return fmt.Sprintf("%+.3f_%+.3f__%+.3f_%+.3f", srcLat, srcLon, dstLat, dstLon)
}
