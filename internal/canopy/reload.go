package canopy

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/verove-jordan/astronomy/internal/geogrid"
)

// This file lets the running server download a canopy atlas for a user-chosen area and hot-swap it in — so
// the dark-sky finder's "download canopy for this area" button takes effect without a restart.

// BuildState reports an in-progress or finished canopy build for the UI to poll.
type BuildState struct {
	Status   string   `json:"status"` // "idle" | "building" | "done" | "error"
	Done     int      `json:"done"`
	Total    int      `json:"total"`
	Error    string   `json:"error,omitempty"`
	Coverage Coverage `json:"coverage"`
}

// buildTracker holds the single in-flight build + its progress (declared on the Provider).
type buildTracker struct {
	mu    sync.Mutex
	state BuildState
}

// AtlasDir is the directory holding atlas.bin / atlas.json (the build target).
func (p *Provider) AtlasDir() string { return filepath.Dir(p.atlasPath) }

// Coverage describes the canopy atlas currently loaded (Present:false when none is installed).
func (p *Provider) Coverage() Coverage {
	a := p.currentAtlas()
	if a == nil {
		return Coverage{}
	}
	return coverageFromMeta(a.Meta, p.atlasPath)
}

// ReloadAtlas re-opens the atlas from disk (after a build) and swaps it in, clearing the per-site cache so
// the finder immediately reflects the new data.
func (p *Provider) ReloadAtlas() {
	a := geogrid.Load(p.atlasPath)
	p.atlasMu.Lock()
	p.atlas = a
	p.atlasMu.Unlock()

	p.mu.Lock()
	p.memo = map[string]cachedCanopy{}
	p.mu.Unlock()
}

// StartBuild kicks off a background canopy build covering b at the configured resolution. It errors if a
// build is already running or gdal is missing; poll BuildStateNow for progress. On success it hot-reloads.
func (p *Provider) StartBuild(b Bounds) error {
	if _, err := exec.LookPath("gdalwarp"); err != nil {
		return ErrNoGDAL // fail fast so the UI shows the install hint instead of "building" → error
	}
	tiles := sourceTiles(b, p.sourceURL)
	if len(tiles) == 0 {
		return fmt.Errorf("selected area covers no canopy tiles")
	}
	p.builds.mu.Lock()
	if p.builds.state.Status == "building" {
		p.builds.mu.Unlock()
		return fmt.Errorf("a build is already in progress")
	}
	p.builds.state = BuildState{Status: "building", Total: len(tiles) + 1}
	p.builds.mu.Unlock()

	go func() {
		cov, err := p.BuildAtlas(context.Background(), p.atlasPath, b, p.buildRes, func(done, total int) {
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

// BuildStateNow returns the current build state, stamped with the live atlas coverage.
func (p *Provider) BuildStateNow() BuildState {
	p.builds.mu.Lock()
	s := p.builds.state
	p.builds.mu.Unlock()
	if s.Status == "" {
		s.Status = "idle"
	}
	if s.Status != "building" {
		s.Coverage = p.Coverage()
	}
	return s
}
