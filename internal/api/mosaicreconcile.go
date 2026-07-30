package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/mosaic"
	"github.com/verove-jordan/astronomy/internal/mosaicplan"
	"github.com/verove-jordan/astronomy/internal/store"
)

// Reconciling a mosaic plan against the disk is what makes it resumable across nights: rather than
// trusting a checkbox the user has to remember to tick at 3 a.m., it counts the light frames sitting
// in each pNN/ folder, per filter, and marks the tiles whose capture targets are met. It also
// recovers progress for panels shot before the plan existed, or with other capture software.

// reconcileMosaicPlan re-reads a capture folder and updates the plan's per-tile progress.
// POST /api/mosaic/plans/{id}/reconcile  {"path": "input/M31/2026-07-20"}
func (s *Server) reconcileMosaicPlan(w http.ResponseWriter, r *http.Request) {
	id, ok := mosaicPlanID(w, r)
	if !ok {
		return
	}
	var body struct {
		Path  string   `json:"path"`
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, "invalid body")
		return
	}
	roots, ok := s.resolveRoots(body.Path, body.Paths)
	if !ok {
		badRequest(w, "path must be inside the data directory")
		return
	}
	row, err := s.store.GetMosaicPlan(r.Context(), id)
	if err != nil {
		mosaicStoreError(w, err)
		return
	}
	inv, err := s.scanCache.ScanMany(r.Context(), roots, inspect.DefaultScanOptions())
	if err != nil {
		serverError(w, err)
		return
	}

	frames := make([]inspect.Frame, 0, len(inv.Frames))
	for _, fr := range inv.Frames {
		frames = append(frames, *fr)
	}
	progress := mosaic.CountCaptured(frames, roots[0])
	progressJSON, err := json.Marshal(progress)
	if err != nil {
		serverError(w, err)
		return
	}
	if err := s.store.SetMosaicTileProgress(r.Context(), id, progressJSON, roots[0]); err != nil {
		serverError(w, err)
		return
	}

	autoMarked, err := s.autoMarkFinishedTiles(r, id, row, progress)
	if err != nil {
		serverError(w, err)
		return
	}
	fresh, err := s.store.GetMosaicPlan(r.Context(), id)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"plan":        planJSONOf(fresh),
		"root":        roots[0],
		"auto_marked": autoMarked,
		"panels":      len(progress),
	})
}

// autoMarkFinishedTiles flips tiles to "captured" once their per-filter targets are met. It only
// ever ADDS completion: a tile the user deliberately skipped, or one whose frames were deleted,
// keeps whatever status it has — the reconciler must never silently undo a human decision.
func (s *Server) autoMarkFinishedTiles(r *http.Request, id int64, row store.MosaicPlan, progress mosaic.TileProgress) (int, error) {
	targets, tiles, statuses := planTargets(row), planTiles(row), planStatuses(row)
	if len(targets) == 0 || len(tiles) == 0 {
		return 0, nil
	}
	updates := map[string]string{}
	for _, tile := range tiles {
		key := strconv.Itoa(tile.Index)
		if statuses[key] != "" {
			continue // already captured or skipped by hand
		}
		if mosaic.TileDone(progress[tile.Folder], targets) {
			updates[key] = mosaicplan.StatusCaptured
		}
	}
	if len(updates) == 0 {
		return 0, nil
	}
	if _, err := s.store.MergeMosaicTileStatuses(r.Context(), id, updates); err != nil {
		return 0, err
	}
	return len(updates), nil
}

// planTargets/planTiles/planStatuses decode the plan's JSONB columns. A column that fails to parse
// yields the zero value: reconciliation is a best-effort convenience and must never 500 a plan the
// user can still capture by hand.
func planTargets(row store.MosaicPlan) []mosaic.CaptureTarget {
	var out []mosaic.CaptureTarget
	if len(row.CaptureTargets) > 0 {
		_ = json.Unmarshal(row.CaptureTargets, &out)
	}
	return out
}

func planTiles(row store.MosaicPlan) []mosaicplan.Tile {
	var out []mosaicplan.Tile
	if len(row.Tiles) > 0 {
		_ = json.Unmarshal(row.Tiles, &out)
	}
	return out
}

func planStatuses(row store.MosaicPlan) map[string]string {
	out := map[string]string{}
	if len(row.TileStatus) > 0 {
		_ = json.Unmarshal(row.TileStatus, &out)
	}
	return out
}
