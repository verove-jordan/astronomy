package lightpollution

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// This file lets the running server rebuild the offline atlas for a user-chosen region and hot-swap it in
// — so the "download offline data for this area" button takes effect without a restart.

// BuildState reports an in-progress or finished atlas rebuild for the UI to poll.
type BuildState struct {
	Status   string   `json:"status"` // "idle" | "building" | "done" | "error"
	Done     int      `json:"done"`   // tiles downloaded so far
	Total    int      `json:"total"`  // tiles to download
	Error    string   `json:"error,omitempty"`
	Coverage Coverage `json:"coverage"` // the currently-installed atlas (updated on completion)
}

// buildMu / build track the single in-flight rebuild. Declared on the Provider via the fields below.
type buildTracker struct {
	mu    sync.Mutex
	state BuildState
}

// AtlasDir is the directory where atlas.bin / atlas.json live (the rebuild target).
func (p *Provider) AtlasDir() string { return filepath.Dir(p.atlasPath) }

// Coverage describes the atlas currently loaded (Present:false when none is installed).
func (p *Provider) Coverage() Coverage {
	a := p.currentAtlas()
	if a == nil {
		return Coverage{}
	}
	return coverageFromMeta(a.Meta, p.AtlasDir())
}

// ReloadAtlas re-opens the atlas from disk (after a rebuild covering b) and swaps it in, then invalidates
// exactly the caches the new data can change: the badge/finder per-site cache (tiny JSON + an in-RAM memo,
// cheap to rebuild lazily, so cleared wholesale) and — scoped to b — the recolored map tiles. Scoping the
// tiles is the important part: a rebuild used to RemoveAll every tiles_bortle_v* dir, forcing the whole
// world's overlay to cold-re-render (and re-fetch GIBS) even for areas the download never touched. Now only
// tiles overlapping the rebuilt region are dropped; everything else keeps serving from cache.
func (p *Provider) ReloadAtlas(b Bounds) {
	a := loadAtlas(p.atlasPath)
	p.atlasMu.Lock()
	p.atlas = a
	p.atlasMu.Unlock()

	p.mu.Lock()
	p.memo = map[string]SiteQuality{} // may hold GIBS values for points the atlas now covers
	p.mu.Unlock()

	_ = os.RemoveAll(filepath.Join(p.cacheDir, "sites")) // per-site JSON: tiny, no re-render, safe to drop wholesale
	invalidateColoredTiles(p.cacheDir, b)                // rendered PNGs: expensive — drop only the rebuilt region's
}

func coloredCacheDirs(cacheDir string) []string {
	m, _ := filepath.Glob(filepath.Join(cacheDir, "tiles_bortle_v*"))
	return m
}

// invalidateColoredTiles removes only the recolored overlay tiles that overlap b (the just-rebuilt region),
// across every cache version. A tile whose path can't be parsed is left alone — the goal is to avoid a full
// re-render, so an unclassifiable tile is never nuked "just in case".
func invalidateColoredTiles(cacheDir string, b Bounds) {
	for _, dir := range coloredCacheDirs(cacheDir) {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".png") {
				return nil
			}
			if z, x, y, ok := parseTilePath(dir, path); ok && tileIntersectsBounds(z, x, y, b) {
				_ = os.Remove(path)
			}
			return nil
		})
	}
}

// parseTilePath extracts z/x/y from a cached tile path "<dir>/{z}/{x}/{y}.png".
func parseTilePath(dir, path string) (z, x, y int, ok bool) {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return 0, 0, 0, false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	z, e1 := strconv.Atoi(parts[0])
	x, e2 := strconv.Atoi(parts[1])
	y, e3 := strconv.Atoi(strings.TrimSuffix(parts[2], ".png"))
	if e1 != nil || e2 != nil || e3 != nil {
		return 0, 0, 0, false
	}
	return z, x, y, true
}

// tileIntersectsBounds reports whether tile z/x/y overlaps the geographic box b (mirrors tileIntersectsAtlas).
func tileIntersectsBounds(z, x, y int, b Bounds) bool {
	n, s, w, e := tileLatLonBounds(z, x, y)
	return s <= b.MaxLat && n >= b.MinLat && w <= b.MaxLon && e >= b.MinLon
}

// StartBuild kicks off a background rebuild of the atlas covering b for the given year (0 → default). It
// returns an error if a build is already running; poll BuildState for progress. On success it hot-reloads.
func (p *Provider) StartBuild(b Bounds, year int) error {
	total := TileCount(b)
	if total == 0 {
		return fmt.Errorf("selected area covers no tiles")
	}
	p.builds.mu.Lock()
	if p.builds.state.Status == "building" {
		p.builds.mu.Unlock()
		return fmt.Errorf("a build is already in progress")
	}
	p.builds.state = BuildState{Status: "building", Total: total}
	p.builds.mu.Unlock()

	go func() {
		cov, err := BuildAtlas(context.Background(), p.AtlasDir(), b, year, nil, func(done, total int) {
			p.builds.mu.Lock()
			p.builds.state.Done, p.builds.state.Total = done, total
			p.builds.mu.Unlock()
		})
		if err == nil {
			p.ReloadAtlas(b)
			cov = p.Coverage()
		}
		p.builds.mu.Lock()
		if err != nil {
			p.builds.state.Status, p.builds.state.Error = "error", err.Error()
		} else {
			p.builds.state.Status, p.builds.state.Coverage = "done", cov
		}
		p.builds.mu.Unlock()
	}()
	return nil
}

// BuildStateNow returns the current build state, always stamped with the live atlas coverage.
func (p *Provider) BuildStateNow() BuildState {
	p.builds.mu.Lock()
	s := p.builds.state
	p.builds.mu.Unlock()
	if s.Status == "" {
		s.Status = "idle"
	}
	if s.Status != "building" {
		s.Coverage = p.Coverage() // reflect what is actually installed right now
	}
	return s
}
