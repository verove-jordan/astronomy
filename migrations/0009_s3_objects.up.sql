-- Classified S3 layout: the bucket mirrors captures at its root as darks/ offsets/ flats/ and
-- lum/<object>/<date>/ (see internal/s3layout). Unlike the legacy data/<rel> mirror, a classified key is
-- NOT derivable from the local path alone (it needs the object/date/type of the file), and one source
-- folder's files scatter across the four roots — so the local-rel → S3-key mapping must be PERSISTED.
-- This table is that ledger: written as files verify on upload, read by the serving fallback, the full-S3
-- pull, remove-local verification and the browse listings (all with a legacy-key fallback). Per the house
-- DB conventions: BIGINT millisecond timestamps, snake_case plural, indexed match columns.
CREATE TABLE s3_objects (
    id         BIGSERIAL PRIMARY KEY,
    bucket     TEXT   NOT NULL,
    prefix     TEXT   NOT NULL DEFAULT '',  -- the user prefix (scopes the mirror; '' = bucket root)
    local_rel  TEXT   NOT NULL,             -- DataDir-relative slash path (the stable identity)
    s3_key     TEXT   NOT NULL,             -- the classified object key, relative to nothing (full key)
    size       BIGINT NOT NULL,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL
);
-- One mapping per (bucket, prefix, local_rel). text_pattern_ops so the same index also serves the
-- `local_rel = $rel OR local_rel LIKE $rel || '/%'` prefix scans the pull / browse listings use.
CREATE UNIQUE INDEX idx_s3_objects_scope_rel ON s3_objects(bucket, prefix, local_rel text_pattern_ops);
-- Key-prefix lookups for explorer-delete row pruning.
CREATE INDEX idx_s3_objects_key ON s3_objects(bucket, s3_key text_pattern_ops);
