-- Core schema. Per house DB conventions: surrogate id, created_at/updated_at as int64
-- millisecond timestamps, snake_case plural tables, indexed foreign keys. Durations are ms;
-- temperatures are milli-°C. ("offset" is a reserved word, so the camera offset is cam_offset.)

CREATE TABLE sessions (
    id         BIGSERIAL PRIMARY KEY,
    root_path  TEXT   NOT NULL,
    object     TEXT   NOT NULL DEFAULT '',
    note       TEXT   NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL
);

CREATE TABLE frames (
    id           BIGSERIAL PRIMARY KEY,
    session_id   BIGINT  NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    path         TEXT    NOT NULL,
    frame_type   TEXT    NOT NULL,
    filter       TEXT    NOT NULL DEFAULT '',
    exposure_ms  BIGINT  NOT NULL DEFAULT 0,
    gain         BIGINT  NOT NULL DEFAULT 0,
    cam_offset   BIGINT  NOT NULL DEFAULT 0,
    temp_milli_c BIGINT  NOT NULL DEFAULT 0,
    has_temp     BOOLEAN NOT NULL DEFAULT FALSE,
    bin_x        INT     NOT NULL DEFAULT 1,
    bin_y        INT     NOT NULL DEFAULT 1,
    width        INT     NOT NULL DEFAULT 0,
    height       INT     NOT NULL DEFAULT 0,
    object       TEXT    NOT NULL DEFAULT '',
    instrument   TEXT    NOT NULL DEFAULT '',
    date_obs_ms  BIGINT  NOT NULL DEFAULT 0,
    class_source TEXT    NOT NULL DEFAULT '',
    created_at   BIGINT  NOT NULL,
    updated_at   BIGINT  NOT NULL
);
CREATE INDEX idx_frames_session ON frames(session_id);
CREATE INDEX idx_frames_type ON frames(frame_type);
CREATE INDEX idx_frames_filter ON frames(filter);

CREATE TABLE frame_metrics (
    id             BIGSERIAL PRIMARY KEY,
    frame_id       BIGINT NOT NULL REFERENCES frames(id) ON DELETE CASCADE,
    fwhm           DOUBLE PRECISION NOT NULL DEFAULT 0,
    wfwhm          DOUBLE PRECISION NOT NULL DEFAULT 0,
    roundness      DOUBLE PRECISION NOT NULL DEFAULT 0,
    star_count     INT    NOT NULL DEFAULT 0,
    background     DOUBLE PRECISION NOT NULL DEFAULT 0,
    quality        DOUBLE PRECISION NOT NULL DEFAULT 0,
    trail_detected BOOLEAN NOT NULL DEFAULT FALSE,
    trail_score    DOUBLE PRECISION NOT NULL DEFAULT 0,
    rejected       BOOLEAN NOT NULL DEFAULT FALSE,
    reject_reason  TEXT   NOT NULL DEFAULT '',
    created_at     BIGINT NOT NULL,
    updated_at     BIGINT NOT NULL
);
CREATE UNIQUE INDEX idx_frame_metrics_frame ON frame_metrics(frame_id);

CREATE TABLE master_frames (
    id           BIGSERIAL PRIMARY KEY,
    master_type  TEXT   NOT NULL,
    filter       TEXT   NOT NULL DEFAULT '',
    exposure_ms  BIGINT NOT NULL DEFAULT 0,
    gain         BIGINT NOT NULL DEFAULT 0,
    cam_offset   BIGINT NOT NULL DEFAULT 0,
    temp_milli_c BIGINT NOT NULL DEFAULT 0,
    bin          INT    NOT NULL DEFAULT 1,
    frame_count  INT    NOT NULL DEFAULT 0,
    path         TEXT   NOT NULL,
    instrument   TEXT   NOT NULL DEFAULT '',
    created_at   BIGINT NOT NULL,
    updated_at   BIGINT NOT NULL
);
CREATE INDEX idx_master_frames_match ON master_frames(master_type, gain, cam_offset, bin);

CREATE TABLE jobs (
    id             BIGSERIAL PRIMARY KEY,
    session_id     BIGINT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    kind           TEXT   NOT NULL,
    status         TEXT   NOT NULL DEFAULT 'queued',
    progress       INT    NOT NULL DEFAULT 0,
    current_step   TEXT   NOT NULL DEFAULT '',
    log_tail       TEXT   NOT NULL DEFAULT '',
    error          TEXT   NOT NULL DEFAULT '',
    params         JSONB  NOT NULL DEFAULT '{}',
    result         JSONB  NOT NULL DEFAULT '{}',
    started_at_ms  BIGINT NOT NULL DEFAULT 0,
    finished_at_ms BIGINT NOT NULL DEFAULT 0,
    created_at     BIGINT NOT NULL,
    updated_at     BIGINT NOT NULL
);
CREATE INDEX idx_jobs_status ON jobs(status);
CREATE INDEX idx_jobs_session ON jobs(session_id);

CREATE TABLE outputs (
    id         BIGSERIAL PRIMARY KEY,
    job_id     BIGINT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    kind       TEXT   NOT NULL,
    filter     TEXT   NOT NULL DEFAULT '',
    path       TEXT   NOT NULL,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL
);
CREATE INDEX idx_outputs_job ON outputs(job_id);
