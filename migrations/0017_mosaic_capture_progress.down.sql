ALTER TABLE mosaic_plans
    DROP COLUMN IF EXISTS capture_targets,
    DROP COLUMN IF EXISTS tile_progress,
    DROP COLUMN IF EXISTS capture_root,
    DROP COLUMN IF EXISTS reconciled_at;
