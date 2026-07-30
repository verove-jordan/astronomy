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

// lightGroup is a set of light frames sharing one calibration identity (session + capture night +
// filter + exposure + camera + temperature). Each group is calibrated with its own night's flat
// before all groups of a channel are co-registered and stacked together.
type lightGroup struct {
	SessionID int64
	Current   bool // frames from the session being processed now (use this run's flats)
	// Session is the group's capture-night key ("YYYY-MM-DD"; "" = undated) — the display label and
	// the night the group's flats are selected from.
	Session string
	Filter  string
	Key     inspect.SetKey // for dark/bias/flat matching
	Frames  []*inspect.Frame
}

// ReusePlan is the per-channel grouping plus a human-facing summary of what prior data is folded in.
type ReusePlan struct {
	byFilter map[string][]lightGroup
	Summary  ReuseSummary
	// MissingPrior counts catalogued prior frames skipped because their file is gone from disk (e.g.
	// freed after an S3 mirror) — one ghost path would otherwise sink its whole group's Siril link.
	MissingPrior int
	// Anchored is true when some channel merges several groups (multi-night capture and/or prior
	// sessions): every channel is then routed through the grouped path and registered onto the anchor
	// night's canvas, so all channel masters share one geometry (the combine's dims invariant).
	Anchored bool
	// AnchorNight is the capture night whose reference frame pins that shared canvas ("" when the
	// grouped data is undated — anchoring then falls back to each channel's biggest current group).
	AnchorNight string
	// Nights are the distinct dated capture nights across all groups (sorted).
	Nights []string
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
	// Nights are the distinct capture nights this session contributes ("YYYY-MM-DD", sorted).
	Nights []string `json:"nights,omitempty"`
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
// target (coordinate cone or normalized name), then resolves the run's anchor night. Prior frames
// already present in the current scan, or from sessions the user deselected, are excluded. With no
// provider or no current session it returns just the current session's groups (behavior identical to
// a non-reuse run).
func buildReusePlan(ctx context.Context, cfg ReuseConfig, inv *inspect.Inventory,
	currentSession int64, tq targetQuery) (*ReusePlan, error) {
	plan, err := assembleReusePlan(ctx, cfg, inv, currentSession, tq)
	plan.computeAnchor()
	return plan, err
}

// assembleReusePlan builds the per-channel grouping (current sets + prior rows); buildReusePlan
// finishes it with the anchor computation so every exit path gets one.
func assembleReusePlan(ctx context.Context, cfg ReuseConfig, inv *inspect.Inventory,
	currentSession int64, tq targetQuery) (*ReusePlan, error) {
	plan := &ReusePlan{byFilter: map[string][]lightGroup{}}
	for _, set := range inv.SetsOfType(inspect.Light) {
		g := lightGroup{SessionID: currentSession, Current: true, Session: set.Frames[0].Session,
			Filter: set.Key.Filter, Key: set.Key, Frames: set.Frames}
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
	addPriorGroups(plan, cfg, rows, currentScanPaths(inv))
	return plan, nil
}

// priorKey identifies one prior calibration group: the catalog session AND the config key. The
// session id is part of the key on purpose — two different prior sessions with an identical config
// (and night) must NOT merge into one group, or the second session's frames would silently get the
// FIRST session's flat (a real bug this fixed).
type priorKey struct {
	sessionID int64
	key       inspect.SetKey
}

// addPriorGroups folds the prior rows into the plan, de-duplicating by path (against the current scan
// and across sessions) and honoring the session selection, then computes the summary.
func addPriorGroups(plan *ReusePlan, cfg ReuseConfig, rows []store.FrameRow, currentPaths map[string]bool) {
	seen := map[string]bool{}
	groups := map[priorKey]*lightGroup{}
	perSession := map[int64]*ReuseSessionInfo{}
	filtersSeen := map[int64]map[string]bool{}
	nightsSeen := map[int64]map[string]bool{}

	for _, r := range rows {
		if currentPaths[r.Path] || seen[r.Path] {
			continue
		}
		if cfg.Sessions != nil && !cfg.Sessions[r.SessionID] {
			continue
		}
		if !fileExists(r.Path) { // freed to S3 (or moved) — linking the ghost would fail the whole group
			plan.MissingPrior++
			continue
		}
		seen[r.Path] = true
		fr := frameFromRow(r)
		pk := priorKey{sessionID: r.SessionID, key: lightKey(fr)}
		g := groups[pk]
		if g == nil {
			g = &lightGroup{SessionID: r.SessionID, Session: fr.Session, Filter: r.Filter, Key: pk.key}
			groups[pk] = g
		}
		g.Frames = append(g.Frames, fr)

		info := perSession[r.SessionID]
		if info == nil {
			info = &ReuseSessionInfo{SessionID: r.SessionID}
			perSession[r.SessionID] = info
			filtersSeen[r.SessionID] = map[string]bool{}
			nightsSeen[r.SessionID] = map[string]bool{}
		}
		info.Frames++
		info.IntegrationMs += r.ExposureMs
		if r.Filter != "" && !filtersSeen[r.SessionID][r.Filter] {
			filtersSeen[r.SessionID][r.Filter] = true
			info.Filters = append(info.Filters, r.Filter)
		}
		if fr.Session != "" && !nightsSeen[r.SessionID][fr.Session] {
			nightsSeen[r.SessionID][fr.Session] = true
			info.Nights = append(info.Nights, fr.Session)
		}
	}

	for pk, g := range groups {
		plan.byFilter[pk.key.Filter] = append(plan.byFilter[pk.key.Filter], *g)
	}
	plan.Summary = summarize(perSession)
}

// nightStat aggregates one capture night's contribution across every channel of the plan.
type nightStat struct {
	night   string
	current bool            // some current-capture group belongs to this night
	filters map[string]bool // distinct channels the night feeds
	frames  int
}

// computeAnchor resolves the run's anchor night — the night whose reference frame every channel's
// registration canvas is pinned to, so all channel masters share one geometry regardless of which
// nights each channel covers. Only grouped runs anchor (some channel with ≥2 groups); a plain
// single-group-per-channel run keeps Anchored=false and the untouched fast path. Ranking: nights
// with current-capture data first (the user's own capture defines the field), then channels
// covered, then light frames, then the latest night.
func (p *ReusePlan) computeAnchor() {
	stats := map[string]*nightStat{}
	for _, groups := range p.byFilter {
		p.Anchored = p.Anchored || len(groups) > 1
		for _, g := range groups {
			s := stats[g.Session]
			if s == nil {
				s = &nightStat{night: g.Session, filters: map[string]bool{}}
				stats[g.Session] = s
			}
			s.current = s.current || g.Current
			s.filters[g.Filter] = true
			s.frames += len(g.Frames)
		}
	}
	for night := range stats {
		if night != "" {
			p.Nights = append(p.Nights, night)
		}
	}
	sort.Strings(p.Nights)
	if !p.Anchored {
		return
	}
	var best *nightStat
	for _, s := range stats {
		if best == nil || anchorOutranks(s, best) {
			best = s
		}
	}
	if best != nil {
		p.AnchorNight = best.night
	}
}

// anchorOutranks reports whether challenger beats incumbent as the run's anchor night.
func anchorOutranks(challenger, incumbent *nightStat) bool {
	if challenger.current != incumbent.current {
		return challenger.current
	}
	if len(challenger.filters) != len(incumbent.filters) {
		return len(challenger.filters) > len(incumbent.filters)
	}
	if challenger.frames != incumbent.frames {
		return challenger.frames > incumbent.frames
	}
	// Latest dated night wins; an undated ("") night never beats a dated one.
	return challenger.night > incumbent.night
}

// anchorGroupIndex picks a channel's anchor group — the group whose frame the merged registration
// is referenced to, making its night's canvas the channel master's geometry. The plan's anchor
// night wins when the channel has data for it; a channel absent from the anchor night anchors to
// its biggest current group (the channel-level master alignment reconciles that rarer canvas).
func anchorGroupIndex(groups []lightGroup, anchorNight string) int {
	best := 0
	for i := 1; i < len(groups); i++ {
		if anchorGroupOutranks(groups[i], groups[best], anchorNight) {
			best = i
		}
	}
	return best
}

// anchorGroupOutranks reports whether group b beats group a as a channel's anchor: anchor-night
// membership, then current-capture data, then frame count, then the later night.
func anchorGroupOutranks(b, a lightGroup, anchorNight string) bool {
	if bOn, aOn := b.Session == anchorNight, a.Session == anchorNight; bOn != aOn {
		return bOn
	}
	if b.Current != a.Current {
		return b.Current
	}
	if len(b.Frames) != len(a.Frames) {
		return len(b.Frames) > len(a.Frames)
	}
	return b.Session > a.Session
}

func summarize(perSession map[int64]*ReuseSessionInfo) ReuseSummary {
	var s ReuseSummary
	for _, info := range perSession {
		sort.Strings(info.Filters)
		sort.Strings(info.Nights)
		s.Sessions = append(s.Sessions, *info)
		s.PriorFrames += info.Frames
		s.AddedIntegrationMs += info.IntegrationMs
	}
	s.PriorSessions = len(perSession)
	sort.Slice(s.Sessions, func(i, j int) bool { return s.Sessions[i].SessionID < s.Sessions[j].SessionID })
	return s
}

// lightKey builds the calibration identity for a reused light frame (mirrors inspect's light SetKey:
// camera + exposure + filter + temperature bucket + capture night). Object is left empty — matching
// is by coordinate. The night term splits a prior session's frames per night, exactly like a
// multi-night current scan (per-night flats).
func lightKey(fr *inspect.Frame) inspect.SetKey {
	return inspect.SetKey{
		Type: inspect.Light, Filter: fr.Filter, ExposureMs: fr.ExposureMs,
		Gain: fr.Gain, Offset: fr.Offset, Bin: fr.BinX, TempBucket: tempBucketC(fr.TempMilliC),
		Session: fr.Session,
	}
}

func frameFromRow(r store.FrameRow) *inspect.Frame {
	return &inspect.Frame{
		Path: r.Path, Type: inspect.Light, Filter: r.Filter, ExposureMs: r.ExposureMs,
		Gain: r.Gain, Offset: r.Offset, TempMilliC: r.TempMilliC, HasTemp: r.HasTemp,
		// The frames table has no has_gain column: gain > 0 is the honest proxy (a true-gain-0
		// catalog row degrades to photom's background-matched rung, which is safe).
		HasGain: r.Gain > 0, Instrument: r.Instrument,
		BinX: r.Bin, BinY: r.Bin,
		// The capture instant was previously DROPPED here — the night key needs it.
		DateObsMs: r.DateObsMs, Session: inspect.NightKey(r.DateObsMs),
	}
}

// currentScanPaths returns EVERY path the current scan saw, whatever its type. A catalogued prior
// row for such a path must never re-enter through the plan: the fresh classification is
// authoritative — e.g. a processed leftover once catalogued as a LIGHT (before the leftover veto)
// is now Unknown, and folding the stale catalog row back in would resurrect the junk.
func currentScanPaths(inv *inspect.Inventory) map[string]bool {
	paths := map[string]bool{}
	for _, fr := range inv.Frames {
		paths[fr.Path] = true
	}
	return paths
}

// tempBucketC rounds a milli-°C temperature to the nearest 5 °C (the inspect grouping granularity).
func tempBucketC(milliC int64) int {
	return int(math.Round(float64(milliC)/1000/5) * 5)
}
