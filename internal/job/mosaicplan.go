package job

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/verove-jordan/astronomy/internal/mosaic"
	"github.com/verove-jordan/astronomy/internal/mosaicplan"
)

// mosaicPlanFor resolves a saved mosaic plan into the pipeline's read model — the pipeline stays
// DB-free (same pattern as Catalog). The stored JSONB is the server-computed mosaicplan snapshot.
func (m *Manager) mosaicPlanFor(ctx context.Context, id int64) (*mosaic.Plan, error) {
	row, err := m.store.GetMosaicPlan(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load plan %d: %w", id, err)
	}
	var grid mosaicplan.Grid
	if err := json.Unmarshal(row.Grid, &grid); err != nil {
		return nil, fmt.Errorf("plan %d grid: %w", id, err)
	}
	var tiles []mosaicplan.Tile
	if err := json.Unmarshal(row.Tiles, &tiles); err != nil {
		return nil, fmt.Errorf("plan %d tiles: %w", id, err)
	}
	var req mosaicplan.Request
	if err := json.Unmarshal(row.Request, &req); err != nil {
		return nil, fmt.Errorf("plan %d request: %w", id, err)
	}

	plan := &mosaic.Plan{
		ID: row.ID, Name: row.Name, Target: row.ObjectName,
		CenterRA: req.RADeg, CenterDec: req.DecDeg,
		CameraPADeg: grid.CameraPADeg, OverlapFrac: grid.OverlapFrac,
		Cols: grid.Cols, Rows: grid.Rows,
	}
	for _, t := range tiles {
		plan.Tiles = append(plan.Tiles, mosaic.Tile{
			Row: t.Row, Col: t.Col, Order: t.Order, Folder: t.Folder,
			RA: t.RADeg, Dec: t.DecDeg,
		})
	}
	return plan, nil
}
