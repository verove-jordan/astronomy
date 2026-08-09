-- The capture subsystem's bookkeeping: reusable auto-run sequences, the sessions shot from them,
-- and every frame written. This lives in the engine (not the device server) because a session is a
-- statement about a TARGET and a NIGHT — which object, which mosaic tile, how many frames still
-- owed — and that has to outlive any device restart.
--
-- Per the house DB conventions: BIGINT millisecond timestamps, snake_case plural tables.

-- A saved auto-run: the ordered filter/exposure/count plan, reusable across nights and targets.
CREATE TABLE capture_sequences (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT    NOT NULL,
    payload    JSONB   NOT NULL DEFAULT '{}',   -- capture.Sequence
    favorite   BOOLEAN NOT NULL DEFAULT FALSE,
    created_at BIGINT  NOT NULL,
    updated_at BIGINT  NOT NULL
);
CREATE UNIQUE INDEX idx_capture_sequences_name_lower ON capture_sequences (LOWER(name));

-- One night's run of a sequence.
CREATE TABLE capture_sessions (
    id             BIGSERIAL PRIMARY KEY,
    object         TEXT   NOT NULL DEFAULT '',
    root           TEXT   NOT NULL DEFAULT '',  -- capture folder the frames were written to
    panel          TEXT   NOT NULL DEFAULT '',  -- mosaic tile folder ("p03"), empty for a single pointing
    mosaic_plan_id BIGINT NOT NULL DEFAULT 0,
    tile_index     INTEGER NOT NULL DEFAULT -1,
    sequence       JSONB  NOT NULL DEFAULT '{}',
    status         TEXT   NOT NULL DEFAULT 'running',
    progress       JSONB  NOT NULL DEFAULT '{}',
    total_frames   INTEGER NOT NULL DEFAULT 0,
    frames_done    INTEGER NOT NULL DEFAULT 0,
    started_at     BIGINT NOT NULL DEFAULT 0,
    ended_at       BIGINT NOT NULL DEFAULT 0,
    created_at     BIGINT NOT NULL,
    updated_at     BIGINT NOT NULL
);
CREATE INDEX idx_capture_sessions_started ON capture_sessions (started_at DESC);
CREATE INDEX idx_capture_sessions_plan ON capture_sessions (mosaic_plan_id) WHERE mosaic_plan_id <> 0;

-- Every frame the sequencer wrote. Rows survive their session so "what did I actually shoot?" is
-- answerable even after a plan is deleted, and so per-filter totals never have to re-scan the disk.
CREATE TABLE capture_frames (
    id           BIGSERIAL PRIMARY KEY,
    session_id   BIGINT NOT NULL REFERENCES capture_sessions(id) ON DELETE CASCADE,
    path         TEXT   NOT NULL,
    filter       TEXT   NOT NULL DEFAULT '',
    frame_type   TEXT   NOT NULL DEFAULT 'light',
    exposure_us  BIGINT NOT NULL DEFAULT 0,
    gain         BIGINT NOT NULL DEFAULT 0,
    frame_offset BIGINT NOT NULL DEFAULT 0,
    bin          INTEGER NOT NULL DEFAULT 1,
    temp_milli_c INTEGER NOT NULL DEFAULT 0,
    panel        TEXT   NOT NULL DEFAULT '',
    sequence_no  INTEGER NOT NULL DEFAULT 0,
    started_at   BIGINT NOT NULL DEFAULT 0,
    created_at   BIGINT NOT NULL
);
CREATE INDEX idx_capture_frames_session ON capture_frames (session_id);
CREATE INDEX idx_capture_frames_started ON capture_frames (started_at DESC);
