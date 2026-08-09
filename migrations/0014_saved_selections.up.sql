-- Named/starred folder-sets from the Import "Processing history": naming a history entry persists it
-- here, so the name/star show on every duplicate run of the same folder-set and the entry survives job
-- pruning (the history window is the last 500 jobs). signature is the normalized folder-set key
-- (lowercased paths, sorted, '|'-joined — store.SelectionSignature, the single producer). mode/format
-- snapshot the last run so an orphaned row can still pre-fill the launch form. Per the house DB
-- conventions: BIGINT millisecond timestamps, snake_case plural, case-insensitive unique name.
CREATE TABLE saved_selections (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT    NOT NULL,
    favorite   BOOLEAN NOT NULL DEFAULT FALSE,
    signature  TEXT    NOT NULL,
    paths      JSONB   NOT NULL DEFAULT '[]',  -- original-case absolute capture-folder paths
    mode       TEXT    NOT NULL DEFAULT '',
    format     TEXT    NOT NULL DEFAULT '',
    created_at BIGINT  NOT NULL,
    updated_at BIGINT  NOT NULL
);
-- Unique case-insensitive name (renames can't collide) + unique signature (one saved name per
-- folder-set — re-naming the same set upserts instead of duplicating).
CREATE UNIQUE INDEX idx_saved_selections_name_lower ON saved_selections (LOWER(name));
CREATE UNIQUE INDEX idx_saved_selections_signature ON saved_selections (signature);
