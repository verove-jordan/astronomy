package lightpollution

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

// ReloadAtlas re-opens the atlas from disk (after a rebuild) and swaps it in, then clears the per-site memo
// + cache and the colored-tile cache so the badge, finder, and map all immediately reflect the new data.
func (p *Provider) ReloadAtlas() {
	a := loadAtlas(p.atlasPath)
	p.atlasMu.Lock()
	p.atlas = a
	p.atlasMu.Unlock()

	p.mu.Lock()
	p.memo = map[string]SiteQuality{} // may hold GIBS values for points the atlas now covers
	p.mu.Unlock()

	_ = os.RemoveAll(filepath.Join(p.cacheDir, "sites"))
	for _, dir := range coloredCacheDirs(p.cacheDir) {
		_ = os.RemoveAll(dir)
	}
}

func coloredCacheDirs(cacheDir string) []string {
	m, _ := filepath.Glob(filepath.Join(cacheDir, "tiles_bortle_v*"))
	return m
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
			p.ReloadAtlas()
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
