// The joined pre-run plan: which calibration masters would apply to WHICH (session, night, config)
// group — the union the Import view needs that neither /api/calib/preview (current sets × masters
// only) nor /api/reuse/preview (which sessions fold in, no calibration) could answer alone.
//
// Honest by construction: the grouping IS the run's own buildReusePlan, current-group matching IS the
// run's MatchForLightExcluding over PreviewCandidates, and prior-group flats go through the SAME
// sessionFlatPaths helper over the same RawCalibFrames rows the run's flatCache will read — so the
// plan can never drift from what the run actually does.
package pipeline

import (
	"context"
	"fmt"
	"sort"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/store"
)

// PlanMaster sources (PlanMaster.Source).
const (
	planSourceLibrary = "library"         // an existing library master
	planSourceCapture = "capture"         // built from the capture's own calibration frames
	planSourceSession = "session-rebuild" // rebuilt from a prior session's raw flats (per night)
)

// RunPlanPreview is the joined plan: per-session summary + per-channel groups with their masters.
type RunPlanPreview struct {
	Object    string        `json:"object"`
	HasCoords bool          `json:"has_coords"`
	Sessions  []PlanSession `json:"sessions,omitempty"`
	Channels  []PlanChannel `json:"channels"`
	Reuse     ReuseSummary  `json:"reuse"`
	// AnchorNight is the night whose canvas every channel master will be registered onto (grouped
	// runs only — see ReusePlan.AnchorNight).
	AnchorNight string   `json:"anchor_night,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
}

// PlanSession is one (session, capture-night) contribution to the run.
type PlanSession struct {
	SessionID     int64    `json:"session_id"` // 0 = the current capture
	Current       bool     `json:"current,omitempty"`
	Session       string   `json:"session,omitempty"` // capture-night key ("" = undated)
	Frames        int      `json:"frames"`
	IntegrationMs int64    `json:"integration_ms"`
	Filters       []string `json:"filters,omitempty"`
}

// PlanChannel is one filter's calibration groups.
type PlanChannel struct {
	Filter string      `json:"filter"`
	Groups []PlanGroup `json:"groups"`
}

// PlanGroup is one calibration group and the masters the run would apply to it. A nil role means
// that correction would be skipped (the Notes say why).
type PlanGroup struct {
	SessionID   int64       `json:"session_id"`
	Current     bool        `json:"current,omitempty"`
	Session     string      `json:"session,omitempty"`
	ExposureMs  int64       `json:"exposure_ms"`
	Gain        int64       `json:"gain"`
	Offset      int64       `json:"offset"`
	TempBucketC int         `json:"temp_bucket_c"`
	Bin         int         `json:"bin"`
	Frames      int         `json:"frames"`
	Dark        *PlanMaster `json:"dark,omitempty"`
	Flat        *PlanMaster `json:"flat,omitempty"`
	Bias        *PlanMaster `json:"bias,omitempty"`
	Notes       []string    `json:"notes,omitempty"`
}

// PlanMaster is one master a group would use, with its provenance.
type PlanMaster struct {
	Source string `json:"source"` // library | capture | session-rebuild
	// Master describes a library/capture candidate (absent for a session-rebuild, which stacks raw
	// flats at run time — RawFlats counts them).
	Master   *calib.Master `json:"master,omitempty"`
	RawFlats int           `json:"raw_flats,omitempty"`
	// SuggestID is the exclusion key the run honors (calib_exclude) — same identity as the
	// calibration preview, deliberately night-blind (an exclusion covers all nights of a config).
	SuggestID string `json:"suggest_id,omitempty"`
}

// PreviewRunPlan builds the joined plan for the given capture dirs. provider nil (reuse disabled) →
// a current-capture-only plan. It runs no Siril and persists nothing.
func PreviewRunPlan(ctx context.Context, provider ReuseProvider, scanner Scanner, masterLib calib.MasterStore,
	dirs []string, catalogDir string, coneDeg float64, force bool, sessions map[int64]bool) (*RunPlanPreview, error) {
	inv, err := scanner.ScanMany(ctx, dirs, inspect.DefaultScanOptions())
	if err != nil {
		return nil, err
	}
	object := dominantObject(inv)
	tq := targetQueryFor(inv, object, catalogDir)
	out := &RunPlanPreview{Object: object, HasCoords: tq.HasCoords}

	plan, err := buildReusePlan(ctx, ReuseConfig{Provider: provider, ConeDeg: coneDeg, Sessions: sessions}, inv, 0, tq)
	if err != nil {
		out.Warnings = append(out.Warnings, "prior-session lookup failed: "+err.Error()) // plan still holds the current groups
	}
	if plan.MissingPrior > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d prior frame(s) missing on disk (freed to S3?) — excluded from the plan", plan.MissingPrior))
	}
	out.Reuse = plan.Summary
	if plan.Anchored {
		out.AnchorNight = plan.AnchorNight
	}

	lib, err := masterLib.ListMasters(ctx)
	if err != nil {
		return nil, err
	}
	candidates := calib.PreviewCandidates(inv, lib)

	for _, filter := range orderedPlanFilters(plan) {
		pc := PlanChannel{Filter: filter}
		for _, g := range plan.byFilter[filter] {
			pc.Groups = append(pc.Groups, planGroupFor(ctx, provider, g, candidates, force))
		}
		out.Channels = append(out.Channels, pc)
	}
	out.Sessions = planSessions(plan)
	return out, nil
}

// planGroupFor resolves one group's masters exactly as the run will: MatchForLightExcluding for
// darks/bias (and the current capture's flat), sessionFlatPaths for a prior group's per-night flat.
func planGroupFor(ctx context.Context, provider ReuseProvider, g lightGroup, candidates []calib.Master, force bool) PlanGroup {
	pg := PlanGroup{
		SessionID: g.SessionID, Current: g.Current, Session: g.Session,
		ExposureMs: g.Key.ExposureMs, Gain: g.Key.Gain, Offset: g.Key.Offset,
		TempBucketC: g.Key.TempBucket, Bin: g.Key.Bin, Frames: len(g.Frames),
	}
	sel := calib.MatchForLightExcluding(g.Key, candidates, nil, force)
	pg.Notes = sel.Notes
	pg.Dark = planMasterFor(g.Key, calib.RoleDark, sel.Dark)
	pg.Bias = planMasterFor(g.Key, calib.RoleBias, sel.Bias)
	if g.Current {
		pg.Flat = planMasterFor(g.Key, calib.RoleFlat, sel.Flat)
		return pg
	}
	// Prior group: the run rebuilds the flat from that session's own raw flats (per night) —
	// mirror flatCache.sessionFlat through the shared pure helper over the same provider rows.
	if provider == nil {
		return pg
	}
	rows, err := provider.RawCalibFrames(ctx, store.CalibQuery{
		Types: []string{string(inspect.Flat)}, Gain: g.Key.Gain, Offset: g.Key.Offset, Bin: g.Key.Bin, SessionID: g.SessionID,
	})
	if err != nil {
		pg.Notes = append(pg.Notes, fmt.Sprintf("session %d: flat lookup failed: %v", g.SessionID, err))
		return pg
	}
	paths, missing, note := sessionFlatPaths(rows, g.Filter, g.Session)
	if note != "" {
		pg.Notes = append(pg.Notes, fmt.Sprintf("session %d: %s", g.SessionID, note))
	}
	switch {
	case len(paths) > 0:
		pg.Flat = &PlanMaster{Source: planSourceSession, RawFlats: len(paths)}
	case missing > 0:
		pg.Notes = append(pg.Notes, fmt.Sprintf("session %d: %d raw flat(s) missing on disk (freed to S3?) — flat correction skipped for its frames", g.SessionID, missing))
	default:
		pg.Notes = append(pg.Notes, fmt.Sprintf("session %d: no flats for filter %q — flat correction skipped for its frames", g.SessionID, g.Filter))
	}
	return pg
}

// planMasterFor wraps one matched master with its provenance; nil in → nil out (role skipped).
func planMasterFor(light inspect.SetKey, role string, m *calib.Master) *PlanMaster {
	if m == nil {
		return nil
	}
	src := planSourceLibrary
	if m.Path == "" { // the from-capture discriminator (synthetic, to-be-built masters have no path)
		src = planSourceCapture
	}
	return &PlanMaster{Source: src, Master: m, SuggestID: calib.SuggestID(light, role)}
}

// planSessions aggregates the plan's groups into per-(session, night) summaries, current first.
func planSessions(plan *ReusePlan) []PlanSession {
	type skey struct {
		id      int64
		night   string
		current bool
	}
	agg := map[skey]*PlanSession{}
	filters := map[skey]map[string]bool{}
	for _, groups := range plan.byFilter {
		for _, g := range groups {
			k := skey{g.SessionID, g.Session, g.Current}
			ps := agg[k]
			if ps == nil {
				ps = &PlanSession{SessionID: g.SessionID, Current: g.Current, Session: g.Session}
				agg[k] = ps
				filters[k] = map[string]bool{}
			}
			ps.Frames += len(g.Frames)
			for _, fr := range g.Frames {
				ps.IntegrationMs += fr.ExposureMs
			}
			if g.Filter != "" && !filters[k][g.Filter] {
				filters[k][g.Filter] = true
				ps.Filters = append(ps.Filters, g.Filter)
			}
		}
	}
	out := make([]PlanSession, 0, len(agg))
	for _, ps := range agg {
		sort.Strings(ps.Filters)
		out = append(out, *ps)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Current != b.Current {
			return a.Current // the current capture's nights lead
		}
		if a.SessionID != b.SessionID {
			return a.SessionID < b.SessionID
		}
		return a.Session < b.Session
	})
	return out
}
