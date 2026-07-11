-- Agentic processing: (1) finish_iterations grows the columns that turn it into the supervisor's
-- MEMORY — the full working preset that produced each pass (warm-start priors + history diffs) and
-- the rendered PNG path (so a later attempt can show/see prior results); (2) agent_series is the
-- durable "improvement campaign" over one target — each attempt is a normal job linked by
-- jobs.series_id, so cross-job iteration history is a join, not new plumbing. Additive + defaults,
-- existing rows stay valid. House conventions: BIGINT ms timestamps, snake_case plural.
ALTER TABLE finish_iterations ADD COLUMN png_path TEXT  NOT NULL DEFAULT '';
ALTER TABLE finish_iterations ADD COLUMN preset   JSONB NOT NULL DEFAULT '{}'; -- versioned {"v":1,...} full working preset

CREATE TABLE agent_series (
    id            BIGSERIAL PRIMARY KEY,
    object        TEXT    NOT NULL,             -- run object name (output/<object>/...)
    kind          TEXT    NOT NULL,             -- processing mode (deepsky|nebula|milkyway|planetary|comet)
    input_path    TEXT    NOT NULL DEFAULT '',  -- primary capture folder
    goal          TEXT    NOT NULL DEFAULT '',  -- free-text objective the agent pursues
    status        TEXT    NOT NULL DEFAULT 'active', -- active | done | stopped
    auto_continue BOOLEAN NOT NULL DEFAULT FALSE,    -- keep improving unattended after each attempt
    max_attempts  INTEGER NOT NULL DEFAULT 3,
    target_score  DOUBLE PRECISION NOT NULL DEFAULT 8,
    best_job_id   BIGINT  NOT NULL DEFAULT 0,
    best_score    DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at    BIGINT  NOT NULL,
    updated_at    BIGINT  NOT NULL
);
CREATE INDEX idx_agent_series_object ON agent_series(object, kind);

ALTER TABLE jobs ADD COLUMN series_id BIGINT NOT NULL DEFAULT 0; -- 0 = not part of a series
CREATE INDEX idx_jobs_series ON jobs(series_id) WHERE series_id <> 0;
