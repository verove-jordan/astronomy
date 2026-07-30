package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/verove-jordan/astronomy/internal/mosaic"
	"github.com/verove-jordan/astronomy/internal/mosaicplan"
	"github.com/verove-jordan/astronomy/internal/store"
)

// Saved mosaic plans: the planner persists a computed grid here; the capture assistant (often on
// the phone at the scope) reads it back and updates per-tile progress; a processing job references
// it by id. Tiles are ALWAYS recomputed server-side from the request — client tile lists are
// never trusted.

// mosaicPlanBody is the create/update payload. On update, absent fields are untouched; a present
// Request recomputes the layout (resetting tile progress only when the geometry actually changed).
type mosaicPlanBody struct {
	Name            string             `json:"name"`
	OrientationDone *bool              `json:"orientation_done"`
	Request         *mosaicRequestBody `json:"request"`
	// CaptureTargets is the per-filter goal for every tile ([]mosaic.CaptureTarget). Setting it is
	// what makes "this tile is finished" derivable from the frames on disk.
	CaptureTargets json.RawMessage `json:"capture_targets"`
}

// mosaicPlanJSON is the wire shape of one saved plan — the frozen contract the pipeline reads
// (tiles[].folder join key, grid.camera_pa_deg, grid.overlap_frac, tiles[].ra_deg/dec_deg).
type mosaicPlanJSON struct {
	ID              int64           `json:"id"`
	Name            string          `json:"name"`
	ObjectName      string          `json:"object_name"`
	Request         json.RawMessage `json:"request"`
	Grid            json.RawMessage `json:"grid"`
	Tiles           json.RawMessage `json:"tiles"`
	TileStatus      json.RawMessage `json:"tile_status"`
	OrientationDone bool            `json:"orientation_done"`
	CaptureTargets  json.RawMessage `json:"capture_targets"`
	TileProgress    json.RawMessage `json:"tile_progress"`
	CaptureRoot     string          `json:"capture_root,omitempty"`
	ReconciledAt    int64           `json:"reconciled_at,omitempty"`
	CreatedAt       int64           `json:"created_at"`
	UpdatedAt       int64           `json:"updated_at"`
}

func planJSONOf(row store.MosaicPlan) mosaicPlanJSON {
	return mosaicPlanJSON{
		ID: row.ID, Name: row.Name, ObjectName: row.ObjectName,
		Request: json.RawMessage(row.Request), Grid: json.RawMessage(row.Grid),
		Tiles: json.RawMessage(row.Tiles), TileStatus: json.RawMessage(row.TileStatus),
		OrientationDone: row.OrientationDone,
		CaptureTargets:  rawOrEmpty(row.CaptureTargets, "[]"),
		TileProgress:    rawOrEmpty(row.TileProgress, "{}"),
		CaptureRoot:     row.CaptureRoot, ReconciledAt: row.ReconciledAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

// rawOrEmpty keeps a JSONB column that predates its migration default from serializing as null.
func rawOrEmpty(b []byte, empty string) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage(empty)
	}
	return json.RawMessage(b)
}

// listMosaicPlans returns every saved plan (tiles included — the Tonight overlay draws them).
// GET /api/mosaic/plans
func (s *Server) listMosaicPlans(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListMosaicPlans(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	plans := make([]mosaicPlanJSON, 0, len(rows))
	for _, row := range rows {
		plans = append(plans, planJSONOf(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"plans": plans})
}

// createMosaicPlan computes and saves a new plan. A duplicate name is 409 — plans carry capture
// progress, so create never overwrites. POST /api/mosaic/plans
func (s *Server) createMosaicPlan(w http.ResponseWriter, r *http.Request) {
	var b mosaicPlanBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		badRequest(w, "invalid body")
		return
	}
	name := strings.TrimSpace(b.Name)
	if name == "" || b.Request == nil {
		badRequest(w, "name and request are required")
		return
	}
	req, echo, plan, err := s.computeMosaicPlan(*b.Request)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	reqJSON, gridJSON, tilesJSON, err := marshalMosaicPlan(req, plan)
	if err != nil {
		serverError(w, err)
		return
	}
	id, err := s.store.CreateMosaicPlan(r.Context(), name, echo.Target, reqJSON, gridJSON, tilesJSON)
	if err != nil {
		if isUniqueViolation(err) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "a plan with that name already exists"})
			return
		}
		serverError(w, err)
		return
	}
	s.respondMosaicPlan(w, r, id, false)
}

// getMosaicPlan returns one plan. GET /api/mosaic/plans/{id}
func (s *Server) getMosaicPlan(w http.ResponseWriter, r *http.Request) {
	id, ok := mosaicPlanID(w, r)
	if !ok {
		return
	}
	row, err := s.store.GetMosaicPlan(r.Context(), id)
	if err != nil {
		mosaicStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plan": planJSONOf(row)})
}

// updateMosaicPlan renames, toggles orientation_done and/or re-plans. When the request geometry
// changed, tile progress is reset and statuses_reset=true is returned so the UI can say so.
// PUT /api/mosaic/plans/{id}
func (s *Server) updateMosaicPlan(w http.ResponseWriter, r *http.Request) {
	id, ok := mosaicPlanID(w, r)
	if !ok {
		return
	}
	var b mosaicPlanBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		badRequest(w, "invalid body")
		return
	}
	existing, err := s.store.GetMosaicPlan(r.Context(), id)
	if err != nil {
		mosaicStoreError(w, err)
		return
	}
	if name := strings.TrimSpace(b.Name); name != "" && name != existing.Name {
		if err := s.store.RenameMosaicPlan(r.Context(), id, name); err != nil {
			if isUniqueViolation(err) {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "a plan with that name already exists"})
				return
			}
			serverError(w, err)
			return
		}
	}
	if b.OrientationDone != nil {
		if err := s.store.SetMosaicOrientationDone(r.Context(), id, *b.OrientationDone); err != nil {
			serverError(w, err)
			return
		}
	}
	if len(b.CaptureTargets) > 0 {
		if err := validateCaptureTargets(b.CaptureTargets); err != nil {
			badRequest(w, err.Error())
			return
		}
		if err := s.store.SetMosaicCaptureTargets(r.Context(), id, b.CaptureTargets); err != nil {
			serverError(w, err)
			return
		}
	}
	reset := false
	if b.Request != nil {
		reset, err = s.replanMosaic(r, id, existing, *b.Request)
		if err != nil {
			badRequest(w, err.Error())
			return
		}
	}
	s.respondMosaicPlan(w, r, id, reset)
}

// validateCaptureTargets rejects a malformed goal list up front: a target with no filter, or a
// negative count, would make "tile finished" undecidable later, in the middle of a night.
func validateCaptureTargets(raw json.RawMessage) error {
	var targets []mosaic.CaptureTarget
	if err := json.Unmarshal(raw, &targets); err != nil {
		return fmt.Errorf("invalid capture_targets: %w", err)
	}
	seen := map[string]bool{}
	for _, tgt := range targets {
		name := strings.TrimSpace(tgt.Filter)
		if name == "" {
			return fmt.Errorf("every capture target needs a filter")
		}
		if seen[name] {
			return fmt.Errorf("duplicate capture target for filter %q", name)
		}
		seen[name] = true
		if tgt.Frames < 0 || tgt.ExposureMs < 0 {
			return fmt.Errorf("capture target %q has a negative count or exposure", name)
		}
	}
	return nil
}

// replanMosaic recomputes a plan's layout and persists it, resetting tile progress only when the
// tile geometry actually changed (same-geometry re-saves keep the capture progress).
func (s *Server) replanMosaic(r *http.Request, id int64, existing store.MosaicPlan, body mosaicRequestBody) (reset bool, err error) {
	req, echo, plan, err := s.computeMosaicPlan(body)
	if err != nil {
		return false, err
	}
	var old mosaicplan.Plan
	if json.Unmarshal(existing.Grid, &old.Grid) == nil && json.Unmarshal(existing.Tiles, &old.Tiles) == nil {
		reset = !mosaicplan.SameGeometry(old, plan)
	} else {
		reset = true
	}
	reqJSON, gridJSON, tilesJSON, err := marshalMosaicPlan(req, plan)
	if err != nil {
		return false, err
	}
	return reset, s.store.SetMosaicPlanGeometry(r.Context(), id, echo.Target, reqJSON, gridJSON, tilesJSON, reset)
}

// deleteMosaicPlan removes a plan. DELETE /api/mosaic/plans/{id}
func (s *Server) deleteMosaicPlan(w http.ResponseWriter, r *http.Request) {
	id, ok := mosaicPlanID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteMosaicPlan(r.Context(), id); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// setMosaicTileStatus marks one tile pending/captured/skipped and returns the progress counts.
// PUT /api/mosaic/plans/{id}/tiles/{index}
func (s *Server) setMosaicTileStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := mosaicPlanID(w, r)
	if !ok {
		return
	}
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || index < 0 {
		badRequest(w, "invalid tile index")
		return
	}
	var b struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		badRequest(w, "invalid body")
		return
	}
	if b.Status != mosaicplan.StatusPending && b.Status != mosaicplan.StatusCaptured && b.Status != mosaicplan.StatusSkipped {
		badRequest(w, "status must be pending|captured|skipped")
		return
	}
	row, err := s.store.GetMosaicPlan(r.Context(), id)
	if err != nil {
		mosaicStoreError(w, err)
		return
	}
	var tiles []mosaicplan.Tile
	if err := json.Unmarshal(row.Tiles, &tiles); err != nil {
		serverError(w, err)
		return
	}
	if index >= len(tiles) {
		badRequest(w, "tile index out of range")
		return
	}
	statusJSON, err := s.store.SetMosaicTileStatus(r.Context(), id, strconv.Itoa(index), b.Status)
	if err != nil {
		mosaicStoreError(w, err)
		return
	}
	captured, skipped := mosaicStatusCounts(statusJSON)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "captured": captured, "skipped": skipped, "total": len(tiles),
		"tile_status": json.RawMessage(statusJSON),
	})
}

// computeMosaicPlan resolves a request body and computes its plan (shared by create/update).
func (s *Server) computeMosaicPlan(body mosaicRequestBody) (mosaicplan.Request, mosaicQueryEcho, mosaicplan.Plan, error) {
	req, echo, err := s.resolveMosaicRequest(body)
	if err != nil {
		return req, echo, mosaicplan.Plan{}, err
	}
	plan, err := mosaicplan.Compute(req)
	return req, echo, plan, err
}

func marshalMosaicPlan(req mosaicplan.Request, plan mosaicplan.Plan) (reqJSON, gridJSON, tilesJSON []byte, err error) {
	if reqJSON, err = json.Marshal(req); err != nil {
		return nil, nil, nil, err
	}
	if gridJSON, err = json.Marshal(plan.Grid); err != nil {
		return nil, nil, nil, err
	}
	tilesJSON, err = json.Marshal(plan.Tiles)
	return reqJSON, gridJSON, tilesJSON, err
}

// respondMosaicPlan fetches the stored row and returns it (with the statuses_reset flag on
// updates that cleared progress).
func (s *Server) respondMosaicPlan(w http.ResponseWriter, r *http.Request, id int64, statusesReset bool) {
	row, err := s.store.GetMosaicPlan(r.Context(), id)
	if err != nil {
		mosaicStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plan": planJSONOf(row), "statuses_reset": statusesReset})
}

func mosaicPlanID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid id")
		return 0, false
	}
	return id, true
}

// mosaicStoreError maps a missing row to 404, anything else to 500.
func mosaicStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "plan not found"})
		return
	}
	serverError(w, err)
}

func mosaicStatusCounts(statusJSON []byte) (captured, skipped int) {
	var m map[string]string
	if json.Unmarshal(statusJSON, &m) != nil {
		return 0, 0
	}
	for _, v := range m {
		switch v {
		case mosaicplan.StatusCaptured:
			captured++
		case mosaicplan.StatusSkipped:
			skipped++
		}
	}
	return captured, skipped
}
