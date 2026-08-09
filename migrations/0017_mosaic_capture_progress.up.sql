-- A mosaic is captured over many nights, so "where did I stop?" cannot live in a checkbox the user
-- has to remember to tick. capture_targets states the per-filter goal for every tile (how many
-- frames of what exposure make a tile done); tile_progress caches what has ACTUALLY been shot,
-- reconciled from the frames on disk (POST /api/mosaic/plans/{id}/reconcile), so progress is
-- recovered even for panels captured before the plan existed or with other software.
--
-- Shapes (JSONB, opaque to SQL — see internal/mosaic/progress.go):
--   capture_targets: [{"filter":"L","frames":40,"exposure_ms":120000,"gain":139,"dither":5}, …]
--   tile_progress:   {"p01": {"L": {"frames":12,"seconds":1440,"last_ms":…,"nights":2}}, …}
ALTER TABLE mosaic_plans
    ADD COLUMN capture_targets JSONB  NOT NULL DEFAULT '[]',
    ADD COLUMN tile_progress   JSONB  NOT NULL DEFAULT '{}',
    ADD COLUMN capture_root    TEXT   NOT NULL DEFAULT '',  -- last reconciled capture folder
    ADD COLUMN reconciled_at   BIGINT NOT NULL DEFAULT 0;
