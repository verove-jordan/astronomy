-- Catalog + cross-session reuse. Persist sky-object identity (coordinates + a canonical target
-- catalog) so a new run can fold in prior light frames of the same target, and scope flats to the
-- session that produced them. Per house DB conventions: int64 ms timestamps, snake_case, indexed
-- lookups. ("offset" is reserved → cam_offset, mirrored from 0001.)

CREATE TABLE targets (
    id              BIGSERIAL PRIMARY KEY,
    canonical_name  TEXT   NOT NULL,
    normalized_name TEXT   NOT NULL,           -- upper-cased, alphanumerics only (e.g. "M101")
    ra_deg          DOUBLE PRECISION NOT NULL DEFAULT 0,
    dec_deg         DOUBLE PRECISION NOT NULL DEFAULT 0,
    has_coords      BOOLEAN NOT NULL DEFAULT FALSE,
    aliases         TEXT[]  NOT NULL DEFAULT '{}',
    created_at      BIGINT NOT NULL,
    updated_at      BIGINT NOT NULL
);
CREATE UNIQUE INDEX idx_targets_normalized ON targets(normalized_name);

-- Per-frame sky coordinates (J2000, decimal degrees) parsed from OBJCTRA/OBJCTDEC, plus a catalog
-- link. Coordinate cone-search prefilters on dec; name fallback uses LOWER(object).
ALTER TABLE frames ADD COLUMN ra_deg     DOUBLE PRECISION NOT NULL DEFAULT 0;
ALTER TABLE frames ADD COLUMN dec_deg    DOUBLE PRECISION NOT NULL DEFAULT 0;
ALTER TABLE frames ADD COLUMN has_coords BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE frames ADD COLUMN target_id  BIGINT NULL REFERENCES targets(id) ON DELETE SET NULL;
CREATE INDEX idx_frames_object_lower ON frames(LOWER(object));
CREATE INDEX idx_frames_dec ON frames(dec_deg);
CREATE INDEX idx_frames_target ON frames(target_id);

-- Scope a master to the session that produced it. NULL = sensor-only global pool (bias/darks);
-- a set session_id marks a session-specific master (flats) so they are not reused across nights.
ALTER TABLE master_frames ADD COLUMN session_id BIGINT NULL REFERENCES sessions(id) ON DELETE SET NULL;
CREATE INDEX idx_master_frames_session ON master_frames(session_id);
