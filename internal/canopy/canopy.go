// Package canopy resolves tree/forest canopy height (metres) at an observing site, so the dark-sky finder
// can raise the horizon where a nearby treeline blocks the low sky. Sourcing is hybrid and soft-failing,
// exactly like internal/lightpollution — a value is ALWAYS produced (0 = open sky), so the horizon score
// never fails to compute:
//
//	disk/memory cache → offline ETH canopy-height atlas → keyless tree-cover tiles → 0 (open sky)
//
// The absence of every source yields 0 with NO warning: 0 canopy is a valid "open sky" answer, and the
// horizon then soft-falls to the terrain-only result (byte-identical to before canopy existed).
package canopy

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
	"github.com/verove-jordan/astronomy/internal/geogrid"
)

// maxCanopyM caps a sampled height, guarding against bad raster values (the tallest known trees are ~116 m).
const maxCanopyM = 120.0

// Provider resolves canopy height. Safe for concurrent use.
type Provider struct {
	http      *http.Client
	tileURL   string
	assumedM  float64 // height assumed for a "forested" tile/land-cover cell (tiles report % cover, not m)
	treeCover float64 // a tile pixel counts as forest at/above this tree-cover %
	ttl       time.Duration
	cacheDir  string
	atlasPath string
	sourceURL string  // ETH COG URL template ({tile}) for the in-app "download canopy for this area" build
	buildRes  float64 // target resolution (deg) for a downloaded atlas

	atlasMu sync.RWMutex
	atlas   *geogrid.Grid // nil when no offline atlas is installed; hot-swapped by ReloadAtlas after a build

	builds buildTracker // single in-flight in-app build + its progress (see build.go / reload.go)

	mu   sync.Mutex
	memo map[string]cachedCanopy // in-process single-point cache, keyed by rounded lat/lon
}

// New builds a Provider, placing its on-disk cache under the work dir (falling back to the user cache).
// A missing atlas file disables the offline step; a missing tile URL disables the keyless step; with
// neither, Active() is false and the horizon stays terrain-only.
func New(cfg *config.Config) *Provider {
	cache := filepath.Join(cfg.WorkDir, "cache", "canopy")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		if ucd, e2 := os.UserCacheDir(); e2 == nil {
			cache = filepath.Join(ucd, "astrostack", "canopy")
			_ = os.MkdirAll(cache, 0o755)
		}
	}
	atlasPath := cfg.CanopyAtlas
	if atlasPath == "" {
		atlasPath = filepath.Join(cfg.WorkDir, "canopy", "atlas.bin")
	}
	ttl := time.Duration(cfg.CanopyCacheTTLHours) * time.Hour
	if ttl <= 0 {
		ttl = 720 * time.Hour
	}
	assumed := cfg.CanopyAssumedHeightM
	if assumed <= 0 {
		assumed = 18
	}
	cover := cfg.CanopyTreeCoverPct
	if cover <= 0 {
		cover = 30
	}
	res := cfg.CanopyBuildResDeg
	if res <= 0 {
		res = 0.0008
	}
	return &Provider{
		http:      &http.Client{Timeout: 8 * time.Second},
		tileURL:   cfg.CanopyTileURL,
		assumedM:  assumed,
		treeCover: cover,
		ttl:       ttl,
		cacheDir:  cache,
		atlasPath: atlasPath,
		sourceURL: cfg.CanopySourceURL,
		buildRes:  res,
		atlas:     geogrid.Load(atlasPath),
		memo:      map[string]cachedCanopy{},
	}
}

// Active reports whether any canopy source is available. When false the elevation horizon stays
// terrain-only, and CanopyHeights need never be called.
func (p *Provider) Active() bool {
	return p.currentAtlas() != nil || p.tileURL != ""
}

func (p *Provider) currentAtlas() *geogrid.Grid {
	p.atlasMu.RLock()
	defer p.atlasMu.RUnlock()
	return p.atlas
}

// CanopyHeight resolves the canopy height (metres) at a single (lat, lon), cached per ~1 km cell. It never
// returns a hard error: absence of data yields (0, ""), i.e. open sky. A non-empty warning flags a degraded
// source (e.g. tiles unreachable) without failing the caller's score.
func (p *Provider) CanopyHeight(ctx context.Context, lat, lon float64) (float64, string) {
	key := cacheKey(lat, lon)
	if m, ok := p.freshCached(key); ok {
		return m, ""
	}
	m, warn := p.sample(ctx, lat, lon, nil)
	p.store(key, m)
	return m, warn
}

// CanopyHeights resolves canopy heights for many points — the horizon ring. It samples each point directly
// (NOT via the ~1 km single-point cache, whose cell would wrongly collapse the near-field ring samples that
// sit only tens of metres apart); the caller's horizon cache already avoids recomputing the ring per pan.
// Absent sources yield 0 for every point.
func (p *Provider) CanopyHeights(ctx context.Context, lats, lons []float64) []float64 {
	out := make([]float64, len(lats))
	if !p.Active() {
		return out
	}
	tc := newTileCache() // decode each tile at most once across the whole ring
	for i := range lats {
		out[i], _ = p.sample(ctx, lats[i], lons[i], tc)
	}
	return out
}

// sample is the source chain shared by CanopyHeight and CanopyHeights: offline atlas → tree-cover tiles → 0
// (open sky). The atlas is sampled worst-case (SampleMax) so the tallest nearby trees are not averaged away;
// the keyless tile tier (tiles.go) is used only when CanopyTileURL is configured.
func (p *Provider) sample(ctx context.Context, lat, lon float64, tc *tileCache) (float64, string) {
	if a := p.currentAtlas(); a != nil {
		if m, ok := a.SampleMax(lat, lon); ok {
			return clampHeight(m), ""
		}
	}
	return p.sampleTiles(ctx, lat, lon, tc)
}

// --- single-point cache (in-process memo + disk JSON, keyed by ~1 km cell) ---

type cachedCanopy struct {
	M  float64 `json:"m"`
	At int64   `json:"at"`
}

func (p *Provider) freshCached(key string) (float64, bool) {
	p.mu.Lock()
	c, ok := p.memo[key]
	p.mu.Unlock()
	if ok && p.fresh(c.At) {
		return c.M, true
	}
	if c, ok := p.readDisk(key); ok && p.fresh(c.At) {
		p.mu.Lock()
		p.memo[key] = c
		p.mu.Unlock()
		return c.M, true
	}
	return 0, false
}

func (p *Provider) store(key string, m float64) {
	c := cachedCanopy{M: m, At: time.Now().UnixMilli()}
	p.mu.Lock()
	p.memo[key] = c
	p.mu.Unlock()
	if b, err := json.Marshal(c); err == nil {
		_ = os.WriteFile(p.cachePath(key), b, 0o644)
	}
}

func (p *Provider) readDisk(key string) (cachedCanopy, bool) {
	b, err := os.ReadFile(p.cachePath(key))
	if err != nil {
		return cachedCanopy{}, false
	}
	var c cachedCanopy
	if err := json.Unmarshal(b, &c); err != nil || c.At == 0 {
		return cachedCanopy{}, false
	}
	return c, true
}

func (p *Provider) fresh(atMs int64) bool {
	return atMs > 0 && time.Since(time.UnixMilli(atMs)) < p.ttl
}

func (p *Provider) cachePath(key string) string {
	return filepath.Join(p.cacheDir, key+".json")
}

// cacheKey rounds to ~0.01° (~1 km); canopy cover is spatially smooth at that scale for a single-site badge.
func cacheKey(lat, lon float64) string {
	return fmt.Sprintf("%+.2f_%+.2f", lat, lon)
}

func clampHeight(m float64) float64 {
	if m < 0 || math.IsNaN(m) {
		return 0
	}
	if m > maxCanopyM {
		return maxCanopyM
	}
	return math.Round(m*10) / 10
}
