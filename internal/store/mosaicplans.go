package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// MosaicPlan is a saved mosaic capture plan. request/grid/tiles are the server-computed
// mosaicplan snapshot (JSONB, opaque to the store); tile_status maps tile index (as a string
// key) → "captured"|"skipped", absent meaning pending.
type MosaicPlan struct {
	ID              int64  `json:"id" db:"id"`
	Name            string `json:"name" db:"name"`
	ObjectName      string `json:"object_name" db:"object_name"`
	Request         []byte `json:"request" db:"request"`
	Grid            []byte `json:"grid" db:"grid"`
	Tiles           []byte `json:"tiles" db:"tiles"`
	TileStatus      []byte `json:"tile_status" db:"tile_status"`
	OrientationDone bool   `json:"orientation_done" db:"orientation_done"`
	// Capture bookkeeping across nights: the per-filter goal per tile, and what the frames on disk
	// say has actually been shot (reconciled, see internal/mosaic/progress.go).
	CaptureTargets []byte `json:"capture_targets" db:"capture_targets"`
	TileProgress   []byte `json:"tile_progress" db:"tile_progress"`
	CaptureRoot    string `json:"capture_root" db:"capture_root"`
	ReconciledAt   int64  `json:"reconciled_at" db:"reconciled_at"`
	CreatedAt      int64  `json:"created_at" db:"created_at"`
	UpdatedAt      int64  `json:"updated_at" db:"updated_at"`
}

const mosaicPlanCols = `id,name,object_name,request,grid,tiles,tile_status,orientation_done,` +
	`capture_targets,tile_progress,capture_root,reconciled_at,created_at,updated_at`

// ListMosaicPlans returns all saved plans, newest first (a mosaic is a project; the current one
// belongs on top).
func (s *Store) ListMosaicPlans(ctx context.Context) ([]MosaicPlan, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+mosaicPlanCols+` FROM mosaic_plans ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[MosaicPlan])
}

// GetMosaicPlan returns one plan; pgx.ErrNoRows when the id is unknown.
func (s *Store) GetMosaicPlan(ctx context.Context, id int64) (MosaicPlan, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+mosaicPlanCols+` FROM mosaic_plans WHERE id=$1`, id)
	if err != nil {
		return MosaicPlan{}, err
	}
	defer rows.Close()
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[MosaicPlan])
}

// CreateMosaicPlan inserts a new plan. A duplicate name surfaces as a unique violation (the API
// maps it to 409) — plans carry capture progress, so creating never silently overwrites.
func (s *Store) CreateMosaicPlan(ctx context.Context, name, objectName string, request, grid, tiles []byte) (int64, error) {
	now := nowMs()
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO mosaic_plans(name,object_name,request,grid,tiles,created_at,updated_at)
		 VALUES($1,$2,$3,$4,$5,$6,$6) RETURNING id`,
		name, objectName, request, grid, tiles, now).Scan(&id)
	return id, err
}

// RenameMosaicPlan changes a plan's name; the LOWER(name) unique index surfaces collisions.
func (s *Store) RenameMosaicPlan(ctx context.Context, id int64, name string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE mosaic_plans SET name=$2, updated_at=$3 WHERE id=$1`, id, name, nowMs())
	return err
}

// SetMosaicPlanGeometry replaces a plan's computed layout after an edit. resetStatus clears the
// per-tile capture progress — the caller sets it only when the tile layout actually changed, so a
// same-geometry re-save (refreshed enrichment) keeps the progress.
func (s *Store) SetMosaicPlanGeometry(ctx context.Context, id int64, objectName string, request, grid, tiles []byte, resetStatus bool) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE mosaic_plans SET object_name=$2, request=$3, grid=$4, tiles=$5,
		        tile_status = CASE WHEN $6 THEN '{}'::jsonb ELSE tile_status END,
		        updated_at=$7
		 WHERE id=$1`,
		id, objectName, request, grid, tiles, resetStatus, nowMs())
	return err
}

// SetMosaicOrientationDone records the capture assistant's camera-angle step.
func (s *Store) SetMosaicOrientationDone(ctx context.Context, id int64, done bool) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE mosaic_plans SET orientation_done=$2, updated_at=$3 WHERE id=$1`, id, done, nowMs())
	return err
}

// SetMosaicTileStatus updates one tile's capture status in place and returns the updated status
// map. "pending" removes the key (absence IS pending); anything else is stored as-is — the API
// layer has validated the enum. pgx.ErrNoRows when the plan id is unknown.
func (s *Store) SetMosaicTileStatus(ctx context.Context, id int64, index string, status string) ([]byte, error) {
	var out []byte
	var err error
	if status == "pending" {
		err = s.pool.QueryRow(ctx,
			`UPDATE mosaic_plans SET tile_status = tile_status - $2::text, updated_at=$3
			 WHERE id=$1 RETURNING tile_status`,
			id, index, nowMs()).Scan(&out)
	} else {
		err = s.pool.QueryRow(ctx,
			`UPDATE mosaic_plans
			 SET tile_status = jsonb_set(tile_status, ARRAY[$2::text], to_jsonb($3::text), true), updated_at=$4
			 WHERE id=$1 RETURNING tile_status`,
			id, index, status, nowMs()).Scan(&out)
	}
	return out, err
}

// SetMosaicCaptureTargets records the per-filter capture goal for every tile. Targets are what turn
// raw frame counts into "this tile is done" — without them completion can only be ticked by hand.
func (s *Store) SetMosaicCaptureTargets(ctx context.Context, id int64, targets []byte) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE mosaic_plans SET capture_targets=$2, updated_at=$3 WHERE id=$1`, id, targets, nowMs())
	return err
}

// SetMosaicTileProgress caches a disk reconciliation: what each panel folder actually holds, which
// capture root it was read from, and when.
func (s *Store) SetMosaicTileProgress(ctx context.Context, id int64, progress []byte, root string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE mosaic_plans SET tile_progress=$2, capture_root=$3, reconciled_at=$4, updated_at=$4
		 WHERE id=$1`,
		id, progress, root, nowMs())
	return err
}

// MergeMosaicTileStatuses applies several tile statuses at once (the reconciler marking finished
// tiles). Keys are tile indices as strings; "pending" entries remove the key.
func (s *Store) MergeMosaicTileStatuses(ctx context.Context, id int64, statuses map[string]string) ([]byte, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	for index, status := range statuses {
		if status == "pending" {
			_, err = tx.Exec(ctx,
				`UPDATE mosaic_plans SET tile_status = tile_status - $2::text WHERE id=$1`, id, index)
		} else {
			_, err = tx.Exec(ctx,
				`UPDATE mosaic_plans
				 SET tile_status = jsonb_set(tile_status, ARRAY[$2::text], to_jsonb($3::text), true)
				 WHERE id=$1`, id, index, status)
		}
		if err != nil {
			return nil, err
		}
	}
	var out []byte
	if err := tx.QueryRow(ctx,
		`UPDATE mosaic_plans SET updated_at=$2 WHERE id=$1 RETURNING tile_status`,
		id, nowMs()).Scan(&out); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

// DeleteMosaicPlan removes a plan.
func (s *Store) DeleteMosaicPlan(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM mosaic_plans WHERE id=$1`, id)
	return err
}
