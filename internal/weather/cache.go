package weather

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/verove-jordan/astronomy/internal/fsutil"
)

// Forecasts and grids are cached in-process and on disk, keyed by rounded location (+ layers for the
// grid). The disk file's mod-time is the freshness clock, so a cached forecast survives a restart and
// repeated panning never hammers the upstream feeds.

type cachedForecast struct {
	f  SiteForecast
	at time.Time
}

type cachedGrid struct {
	g  Grid
	at time.Time
}

func (p *Provider) cachedForecast(key string) (SiteForecast, bool) {
	p.mu.Lock()
	c, ok := p.memoFc[key]
	p.mu.Unlock()
	if ok && time.Since(c.at) < p.ttl {
		return c.f, true
	}
	if f, at, ok := readJSON[SiteForecast](p.forecastPath(key)); ok && time.Since(at) < p.ttl {
		p.mu.Lock()
		p.memoFc[key] = cachedForecast{f, at}
		p.mu.Unlock()
		return f, true
	}
	return SiteForecast{}, false
}

// staleForecast returns an expired-but-recent forecast for key while upstream is down, BOUNDED to
// ttl+forecastStaleGrace the way staleGrid is. Unbounded, a long-dead feed served a days-old timeline
// under a "last cached" warning — technically honest, practically a lie about tonight's sky.
func (p *Provider) staleForecast(key string) (SiteForecast, bool) {
	p.mu.Lock()
	c, ok := p.memoFc[key]
	p.mu.Unlock()
	if ok && time.Since(c.at) < p.ttl+forecastStaleGrace {
		return c.f, true
	}
	if f, at, ok := readJSON[SiteForecast](p.forecastPath(key)); ok && time.Since(at) < p.ttl+forecastStaleGrace {
		return f, true
	}
	return SiteForecast{}, false
}

func (p *Provider) storeForecast(key string, f SiteForecast) {
	p.mu.Lock()
	p.memoFc[key] = cachedForecast{f, time.Now()}
	p.mu.Unlock()
	writeJSON(p.forecastPath(key), f)
}

func (p *Provider) cachedGrid(key string) (Grid, bool) {
	p.mu.Lock()
	c, ok := p.memoGrid[key]
	p.mu.Unlock()
	if ok && time.Since(c.at) < p.ttl {
		return c.g, true
	}
	if g, at, ok := readJSON[Grid](p.gridPath(key)); ok && time.Since(at) < p.ttl {
		p.mu.Lock()
		p.memoGrid[key] = cachedGrid{g, at}
		p.mu.Unlock()
		return g, true
	}
	return Grid{}, false
}

// staleGrid returns an expired-but-recent cube for key: fresh misses fall back to it while upstream is
// down, BOUNDED to ttl+staleGrace so a long-dead feed eventually reads as honestly empty instead of
// serving day-old frames as if they were live (the old anyGrid had no age bound at all).
func (p *Provider) staleGrid(key string) (Grid, bool) {
	p.mu.Lock()
	c, ok := p.memoGrid[key]
	p.mu.Unlock()
	if ok && time.Since(c.at) < p.ttl+staleGrace {
		return c.g, true
	}
	if g, at, ok := readJSON[Grid](p.gridPath(key)); ok && time.Since(at) < p.ttl+staleGrace {
		return g, true
	}
	return Grid{}, false
}

func (p *Provider) storeGrid(key string, g Grid) {
	p.mu.Lock()
	p.memoGrid[key] = cachedGrid{g, time.Now()}
	p.mu.Unlock()
	writeJSON(p.gridPath(key), g)
}

// Night scans cache the assembled HOURS, not the finished outlooks, so re-scoring the same area with a
// different moonlight weighting or deck-top estimate costs no upstream call.

type cachedNight struct {
	hours []pointHours
	at    time.Time
}

func (p *Provider) cachedNight(key string) ([]pointHours, bool) {
	p.mu.Lock()
	c, ok := p.memoNight[key]
	p.mu.Unlock()
	if ok && time.Since(c.at) < p.ttl {
		return c.hours, true
	}
	if h, at, ok := readJSON[[]pointHours](p.nightPath(key)); ok && time.Since(at) < p.ttl {
		p.mu.Lock()
		p.memoNight[key] = cachedNight{h, at}
		p.mu.Unlock()
		return h, true
	}
	return nil, false
}

// staleNight is the night-scan counterpart of staleGrid: an expired-but-recent scan keeps the ranking
// working through an upstream outage, bounded so it can never quietly present yesterday's sky.
func (p *Provider) staleNight(key string) ([]pointHours, bool) {
	p.mu.Lock()
	c, ok := p.memoNight[key]
	p.mu.Unlock()
	if ok && time.Since(c.at) < p.ttl+forecastStaleGrace {
		return c.hours, true
	}
	if h, at, ok := readJSON[[]pointHours](p.nightPath(key)); ok && time.Since(at) < p.ttl+forecastStaleGrace {
		return h, true
	}
	return nil, false
}

func (p *Provider) storeNight(key string, hours []pointHours) {
	p.mu.Lock()
	p.memoNight[key] = cachedNight{hours, time.Now()}
	p.mu.Unlock()
	writeJSON(p.nightPath(key), hours)
}

func (p *Provider) forecastPath(key string) string {
	return filepath.Join(p.cacheDir, "forecast", key+".json")
}

func (p *Provider) gridPath(key string) string {
	return filepath.Join(p.cacheDir, "grid", key+".json")
}

func (p *Provider) nightPath(key string) string {
	return filepath.Join(p.cacheDir, "night", key+".json")
}

func (p *Provider) confidencePath(key string) string {
	return filepath.Join(p.cacheDir, "confidence", key+".json")
}

func readJSON[T any](path string) (T, time.Time, bool) {
	var v T
	info, err := os.Stat(path)
	if err != nil {
		return v, time.Time{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return v, time.Time{}, false
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return v, time.Time{}, false
	}
	return v, info.ModTime(), true
}

func writeJSON(path string, v any) {
	if err := fsutil.EnsureDir(filepath.Dir(path)); err != nil {
		return
	}
	if b, err := json.Marshal(v); err == nil {
		_ = os.WriteFile(path, b, 0o644)
	}
}
