-- Mosaic capture plans: the persisted contract between the planner UI, the at-scope capture
-- assistant and a later processing job (RunRequest.mosaic_plan_id). request snapshots the full
-- geometry input (resolved object coords/size/PA + optics + overlap/margin/camera-PA/overrides);
-- grid + tiles are the server-computed layout (recomputed only on an explicit edit, never on read);
-- tile_status maps tile index → "captured"|"skipped" (absent = pending); orientation_done tracks
-- the capture assistant's set-camera-angle step. Per the house DB conventions: BIGINT millisecond
-- timestamps, snake_case plural table, case-insensitive unique name.
CREATE TABLE mosaic_plans (
    id               BIGSERIAL PRIMARY KEY,
    name             TEXT    NOT NULL,
    object_name      TEXT    NOT NULL DEFAULT '',
    request          JSONB   NOT NULL DEFAULT '{}',
    grid             JSONB   NOT NULL DEFAULT '{}',
    tiles            JSONB   NOT NULL DEFAULT '[]',
    tile_status      JSONB   NOT NULL DEFAULT '{}',
    orientation_done BOOLEAN NOT NULL DEFAULT FALSE,
    created_at       BIGINT  NOT NULL,
    updated_at       BIGINT  NOT NULL
);
CREATE UNIQUE INDEX idx_mosaic_plans_name_lower ON mosaic_plans (LOWER(name));
-- Tonight/GoTo look plans up by the object they cover.
CREATE INDEX idx_mosaic_plans_object_lower ON mosaic_plans (LOWER(object_name));
