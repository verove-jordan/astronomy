package pipeline

import (
	"context"
	"math"
	"sort"
	"strings"

	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/store"
)

// ReuseProvider supplies prior catalog data for cross-session reuse. *store.Store implements it; an
// interface keeps the planner testable with fakes.
type ReuseProvider interface {
	PriorLightFrames(ctx context.Context, q store.LightQuery) ([]store.FrameRow, error)
	RawCalibFrames(ctx context.Context, q store.CalibQuery) ([]store.FrameRow, error)
}

// ReuseConfig configures cross-session light reuse for a run.
type ReuseConfig struct {
	Provider ReuseProvider
	ConeDeg  float64        // coordinate-match radius (degrees)
	Sessions map[int64]bool // chosen prior sessions; nil → include every discovered session
}

// lightGroup is a set of light frames sharing one calibration identity (session + filter + exposure
// + camera + temperature). Each group is calibrated with its own session's flat before all groups of
// a channel are co-registered and stacked together.
type lightGroup struct {
	SessionID int64
	Current   bool // frames from the session being processed now (use this run's flats)
	Filter    string
	Key       inspect.SetKey // for dark/bias/flat matching
	Frames    []*inspect.Frame
}

// ReusePlan is the per-channel grouping plus a human-facing summary of what prior data is folded in.
type ReusePlan struct {
	byFilter map[string][]lightGroup
	Summary  ReuseSummary
}

// ReuseSummary describes the prior data added to a run (surfaced in the API preview and run result).
type ReuseSummary struct {
	PriorSessions      int                `json:"prior_sessions"`
	PriorFrames        int                `json:"prior_frames"`
	AddedIntegrationMs int64              `json:"added_integration_ms"`
	Sessions           []ReuseSessionInfo `json:"sessions,omitempty"`
}

// ReuseSessionInfo is the per-prior-session contribution.
type ReuseSessionInfo struct {
	SessionID     int64    `json:"session_id"`
	Frames        int      `json:"frames"`
	IntegrationMs int64    `json:"integration_ms"`
	Filters       []string `json:"filters"`
}

// ReusePreview reports what a run on a directory would fold in, without processing or persisting.
type ReusePreview struct {
	Object               string       `json:"object"`
	HasCoords            bool         `json:"has_coords"`
	CurrentFrames        int          `json:"current_frames"`
	CurrentIntegrationMs int64        `json:"current_integration_ms"`
	Reuse                ReuseSummary `json:"reuse"`
}

// Scanner scans capture folders into one merged inventory. *inspect.ScanCache (which caches per
// directory, so re-inspecting after adding a folder only scans the new ones) satisfies it, as does the
// uncached package-level inspect.ScanMany.
type Scanner interface {
	ScanMany(ctx context.Context, roots []string, opts inspect.ScanOptions) (*inspect.Inventory, error)
}

// plainScanner is the uncached default scanner (the package-level scan).
type plainScanner struct{}

func (plainScanner) ScanMany(ctx context.Context, roots []string, opts inspect.ScanOptions) (*inspect.Inventory, error) {
	return inspect.ScanMany(ctx, roots, opts)
}

// PreviewReuse scans dir and reports the prior light sessions a run would integrate (target matched by
// coordinate cone or normalized name). It runs no Siril and persists nothing — it is the data behind
// the "auto-discover + confirm" UI.
func PreviewReuse(ctx context.Context, provider ReuseProvider, dir, catalogDir string, coneDeg float64) (*ReusePreview, error) {
	return PreviewReuseMany(ctx, provider, plainScanner{}, []string{dir}, catalogDir, coneDeg)
}

// PreviewReuseMany reports the prior light sessions and added integration a run over the given capture
// dirs (merged into one session) would fold in, without processing. The dirs are scanned as one
// inventory so the dominant target and current integration reflect the whole multi-folder selection.
// The scanner lets callers share a directory-scan cache so re-inspecting an overlapping folder set
// doesn't re-read every header.
func PreviewReuseMany(ctx context.Context, provider ReuseProvider, scanner Scanner, dirs []string, catalogDir string, coneDeg float64) (*ReusePreview, error) {
	inv, err := scanner.ScanMany(ctx, dirs, inspect.DefaultScanOptions())
	if err != nil {
		return nil, err
	}
	object := dominantObject(inv)
	tq := targetQueryFor(inv, object, catalogDir)
	plan, err := buildReusePlan(ctx, ReuseConfig{Provider: provider, ConeDeg: coneDeg}, inv, 0, tq)
	if err != nil {
		return nil, err
	}
	pv := &ReusePreview{Object: object, HasCoords: tq.HasCoords, Reuse: plan.Summary}
	for _, set := range inv.SetsOfType(inspect.Light) {
		pv.CurrentFrames += set.Count
		pv.CurrentIntegrationMs += set.TotalIntegrationMs
	}
	return pv, nil
}

// targetQuery describes the resolved target used to find prior lights.
type targetQuery struct {
	Object    string
	RADeg     float64
	DecDeg    float64
	HasCoords bool
}

// buildReusePlan groups the current session's light sets with matching prior light frames of the same
// target (coordinate cone or normalized name). Prior frames already present in the current scan, or
// from sessions the user deselected, are excluded. With no provider or no current session it returns
// just the current session's groups (behavior identical to a non-reuse run).
func buildReusePlan(ctx context.Context, cfg ReuseConfig, inv *inspect.Inventory,
	currentSession int64, tq targetQuery) (*ReusePlan, error) {
	plan := &ReusePlan{byFilter: map[string][]lightGroup{}}
	for _, set := range inv.SetsOfType(inspect.Light) {
		g := lightGroup{SessionID: currentSession, Current: true, Filter: set.Key.Filter, Key: set.Key, Frames: set.Frames}
		plan.byFilter[set.Key.Filter] = append(plan.byFilter[set.Key.Filter], g)
	}
	if cfg.Provider == nil {
		return plan, nil
	}

	rows, err := cfg.Provider.PriorLightFrames(ctx, store.LightQuery{
		RADeg: tq.RADeg, DecDeg: tq.DecDeg, HasCoords: tq.HasCoords, RadiusDeg: cfg.ConeDeg,
		Names: []string{strings.ToLower(tq.Object)}, ExcludeSession: currentSession,
	})
	if err != nil {
		return plan, err // plan still holds the current session's groups
	}
	addPriorGroups(plan, cfg, rows, currentLightPaths(inv))
	return plan, nil
}

// addPriorGroups folds the prior rows into the plan, de-duplicating by path (against the current scan
// and across sessions) and honoring the session selection, then computes the summary.
func addPriorGroups(plan *ReusePlan, cfg ReuseConfig, rows []store.FrameRow, currentPaths map[string]bool) {
	seen := map[string]bool{}
	groups := map[inspect.SetKey]*lightGroup{}
	perSession := map[int64]*ReuseSessionInfo{}
	filtersSeen := map[int64]map[string]bool{}

	for _, r := range rows {
		if currentPaths[r.Path] || seen[r.Path] {
			continue
		}
		if cfg.Sessions != nil && !cfg.Sessions[r.SessionID] {
			continue
		}
		seen[r.Path] = true
		fr := frameFromRow(r)
		key := lightKey(fr, r.SessionID)
		g := groups[key]
		if g == nil {
			g = &lightGroup{SessionID: r.SessionID, Filter: r.Filter, Key: key}
			groups[key] = g
		}
		g.Frames = append(g.Frames, fr)

		info := perSession[r.SessionID]
		if info == nil {
			info = &ReuseSessionInfo{SessionID: r.SessionID}
			perSession[r.SessionID] = info
			filtersSeen[r.SessionID] = map[string]bool{}
		}
		info.Frames++
		info.IntegrationMs += r.ExposureMs
		if r.Filter != "" && !filtersSeen[r.SessionID][r.Filter] {
			filtersSeen[r.SessionID][r.Filter] = true
			info.Filters = append(info.Filters, r.Filter)
		}
	}

	for key, g := range groups {
		plan.byFilter[key.Filter] = append(plan.byFilter[key.Filter], *g)
	}
	plan.Summary = summarize(perSession)
}

func summarize(perSession map[int64]*ReuseSessionInfo) ReuseSummary {
	var s ReuseSummary
	for _, info := range perSession {
		sort.Strings(info.Filters)
		s.Sessions = append(s.Sessions, *info)
		s.PriorFrames += info.Frames
		s.AddedIntegrationMs += info.IntegrationMs
	}
	s.PriorSessions = len(perSession)
	sort.Slice(s.Sessions, func(i, j int) bool { return s.Sessions[i].SessionID < s.Sessions[j].SessionID })
	return s
}

// lightKey builds the calibration identity for a reused light frame (mirrors inspect's light SetKey:
// camera + exposure + filter + temperature bucket). Object is left empty — matching is by coordinate.
func lightKey(fr *inspect.Frame, _ int64) inspect.SetKey {
	return inspect.SetKey{
		Type: inspect.Light, Filter: fr.Filter, ExposureMs: fr.ExposureMs,
		Gain: fr.Gain, Offset: fr.Offset, Bin: fr.BinX, TempBucket: tempBucketC(fr.TempMilliC),
	}
}

func frameFromRow(r store.FrameRow) *inspect.Frame {
	return &inspect.Frame{
		Path: r.Path, Type: inspect.Light, Filter: r.Filter, ExposureMs: r.ExposureMs,
		Gain: r.Gain, Offset: r.Offset, TempMilliC: r.TempMilliC, HasTemp: r.HasTemp,
		BinX: r.Bin, BinY: r.Bin,
	}
}

func currentLightPaths(inv *inspect.Inventory) map[string]bool {
	paths := map[string]bool{}
	for _, set := range inv.SetsOfType(inspect.Light) {
		for _, fr := range set.Frames {
			paths[fr.Path] = true
		}
	}
	return paths
}

// tempBucketC rounds a milli-°C temperature to the nearest 5 °C (the inspect grouping granularity).
func tempBucketC(milliC int64) int {
	return int(math.Round(float64(milliC)/1000/5) * 5)
}
