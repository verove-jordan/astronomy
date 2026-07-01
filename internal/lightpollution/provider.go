// Package lightpollution resolves the artificial sky brightness (light pollution) at an observing site
// and serves the location-map overlay tiles. The per-site brightness feeds the visibility scores as a
// sky-glow factor parallel to the Moon, and the UI shows it as a Bortle/SQM badge.
//
// Sourcing is hybrid and soft-failing, so a value is ALWAYS produced (the score never fails to compute):
//
//	disk/memory cache → keyed online API → offline atlas (djlorenz model) → keyless GIBS VIIRS → default
//
// The online API key is read from configuration (the environment) only — it is never logged and never
// exposed to the browser; the browser reaches the upstream tiles through this server's proxy.
package lightpollution

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

// SiteQuality is the sky brightness at one location in the forms the scorer and UI need.
type SiteQuality struct {
	SQM         float64 `json:"sqm"`          // zenith brightness, mag/arcsec² (higher = darker)
	Bortle      int     `json:"bortle"`       // 1 (pristine) … 9 (inner city)
	Source      string  `json:"source"`       // "api" | "atlas" | "viirs" | "default"
	RetrievedMs int64   `json:"retrieved_ms"` // when this value was obtained
}

// Provider resolves SiteQuality and proxies overlay tiles. It is safe for concurrent use.
type Provider struct {
	http       *http.Client
	apiURL     string
	apiKey     string
	tileURL    string
	defaultSQM float64
	ttl        time.Duration
	cacheDir   string
	atlasPath  string // <dataDir>/lightpollution/atlas.bin — reloaded/rebuilt in place

	atlasMu sync.RWMutex
	atlas   *atlas // nil when no offline atlas is installed; hot-swapped by ReloadAtlas

	builds buildTracker // single in-flight in-app rebuild + its progress

	mu   sync.Mutex
	memo map[string]SiteQuality // in-process cache keyed by rounded lat/lon
}

// currentAtlas returns the loaded atlas (or nil) under the read lock, so a concurrent ReloadAtlas swap is
// safe for every reader (per-site At, the finder scan, and the tile renderer).
func (p *Provider) currentAtlas() *atlas {
	p.atlasMu.RLock()
	defer p.atlasMu.RUnlock()
	return p.atlas
}

// New builds a Provider, placing its on-disk cache under the work dir (falling back to the user cache).
// A missing online URL disables the API step; a missing atlas file disables the offline step; the
// static default always remains.
func New(cfg *config.Config) *Provider {
	cache := filepath.Join(cfg.WorkDir, "cache", "lightpollution")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		if ucd, e2 := os.UserCacheDir(); e2 == nil {
			cache = filepath.Join(ucd, "astrostack", "lightpollution")
			_ = os.MkdirAll(cache, 0o755)
		}
	}
	atlasPath := cfg.LightPollutionAtlas
	if atlasPath == "" {
		atlasPath = filepath.Join(cfg.DataDir, "lightpollution", "atlas.bin")
	}
	ttl := time.Duration(cfg.LightPollutionCacheTTLHours) * time.Hour
	if ttl <= 0 {
		ttl = 720 * time.Hour
	}
	def := cfg.SkyDefaultSQM
	if def <= 0 {
		def = 21.0
	}
	return &Provider{
		http:       &http.Client{Timeout: 8 * time.Second},
		apiURL:     cfg.LightPollutionAPIURL,
		apiKey:     cfg.LightPollutionAPIKey,
		tileURL:    cfg.LightPollutionTileURL,
		defaultSQM: def,
		ttl:        ttl,
		cacheDir:   cache,
		atlasPath:  atlasPath,
		atlas:      loadAtlas(atlasPath),
		memo:       map[string]SiteQuality{},
	}
}

// At resolves the sky brightness at (lat, lon). It never returns a hard error: it always yields a
// SiteQuality plus an optional human-readable warning (empty when a fresh/primary value was obtained),
// so the caller's visibility score always computes — even offline.
func (p *Provider) At(ctx context.Context, lat, lon float64) (SiteQuality, string) {
	key := cacheKey(lat, lon)

	// 1. Fresh cache (in-process, then disk). Light pollution is near-static, so this hits often.
	if sq, ok := p.freshCached(key); ok {
		return sq, ""
	}

	// 2. Keyed online API — the latest VIIRS-derived value.
	if p.apiURL != "" {
		if sqm, err := p.queryAPI(ctx, lat, lon); err == nil {
			sq := newSiteQuality(sqm, "api")
			p.store(key, sq)
			return sq, ""
		}
	}

	// 3. Offline atlas (downloaded via `just update-light-pollution-data`).
	if a := p.currentAtlas(); a != nil {
		if sqm, ok := a.sampleSQM(lat, lon); ok {
			sq := newSiteQuality(sqm, "atlas")
			p.store(key, sq)
			warn := ""
			if p.apiURL != "" {
				warn = "live light-pollution service unavailable — using the offline atlas"
			}
			return sq, warn
		}
	}

	// 4. Keyless VIIRS night-lights (NASA Black Marble), sampled from the overlay tile covering the site.
	// This is the default working source (no API key, no offline atlas needed).
	if p.tileURL != "" {
		if sqm, ok := p.sampleTileSQM(ctx, lat, lon); ok {
			sq := newSiteQuality(sqm, "viirs")
			p.store(key, sq)
			return sq, ""
		}
	}

	// 5. Any stale cached value beats a flat default.
	if sq, ok := p.anyCached(key); ok {
		return sq, "light-pollution data is stale (offline) — using the last cached value"
	}

	// 6. Static default — last resort.
	return newSiteQuality(p.defaultSQM, "default"),
		"no light-pollution data available — using a default sky brightness"
}

func (p *Provider) freshCached(key string) (SiteQuality, bool) {
	p.mu.Lock()
	sq, ok := p.memo[key]
	p.mu.Unlock()
	if ok && p.fresh(sq) {
		return sq, true
	}
	if sq, ok := p.readDisk(key); ok && p.fresh(sq) {
		p.memoize(key, sq)
		return sq, true
	}
	return SiteQuality{}, false
}

func (p *Provider) anyCached(key string) (SiteQuality, bool) {
	if sq, ok := p.readDisk(key); ok {
		return sq, true
	}
	p.mu.Lock()
	sq, ok := p.memo[key]
	p.mu.Unlock()
	return sq, ok
}

func (p *Provider) fresh(sq SiteQuality) bool {
	return sq.RetrievedMs > 0 && time.Since(time.UnixMilli(sq.RetrievedMs)) < p.ttl
}

func (p *Provider) readDisk(key string) (SiteQuality, bool) {
	b, err := os.ReadFile(p.cachePath(key))
	if err != nil {
		return SiteQuality{}, false
	}
	var sq SiteQuality
	if err := json.Unmarshal(b, &sq); err != nil || sq.SQM == 0 {
		return SiteQuality{}, false
	}
	return sq, true
}

func (p *Provider) store(key string, sq SiteQuality) {
	p.memoize(key, sq)
	if b, err := json.Marshal(sq); err == nil {
		_ = os.WriteFile(p.cachePath(key), b, 0o644)
	}
}

func (p *Provider) memoize(key string, sq SiteQuality) {
	p.mu.Lock()
	p.memo[key] = sq
	p.mu.Unlock()
}

func (p *Provider) cachePath(key string) string {
	return filepath.Join(p.cacheDir, "sites", key+".json")
}

func newSiteQuality(sqm float64, source string) SiteQuality {
	sqm = clampf(sqm, 14.0, pristineSQM)
	return SiteQuality{SQM: round2(sqm), Bortle: sqmToBortle(sqm), Source: source, RetrievedMs: time.Now().UnixMilli()}
}

// cacheKey rounds to ~0.01° (~1 km). Light pollution is spatially smooth, so neighbouring sites share a
// cache entry and panning the map does not trigger a lookup per pixel.
func cacheKey(lat, lon float64) string {
	return fmt.Sprintf("%+.2f_%+.2f", lat, lon)
}
