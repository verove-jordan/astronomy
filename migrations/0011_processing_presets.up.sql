-- User-saved processing presets: a named, reusable snapshot of the launch-form parameters (a partial
-- /api/jobs body — mode, palette, checkboxes, and the Advanced knob JSON). The built-in "best params per
-- situation" catalog lives in Go (internal/preset), not here, so it can be validated as code; this table
-- holds only the presets a user saves. Per the house DB conventions: BIGINT millisecond timestamps,
-- snake_case plural, case-insensitive unique name.
CREATE TABLE processing_presets (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT   NOT NULL,
    payload    JSONB  NOT NULL DEFAULT '{}',   -- the situation recipe (partial /api/jobs body)
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL
);
-- Names are unique case-insensitively so re-saving under the same name overwrites (upsert) rather than
-- duplicating, and a rename can't collide with an existing preset.
CREATE UNIQUE INDEX idx_processing_presets_name_lower ON processing_presets (LOWER(name));
