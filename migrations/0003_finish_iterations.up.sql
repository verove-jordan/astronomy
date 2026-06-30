-- Finish-supervisor v2: per-iteration decisions during a supervised GIMP-composite tuning run.
-- Per the house DB conventions: BIGINT millisecond timestamps, snake_case plural, indexed foreign keys.
-- Rows are written only for job runs (the API path); CLI/MCP refine runs skip persistence.
CREATE TABLE finish_iterations (
    id             BIGSERIAL PRIMARY KEY,
    job_id         BIGINT REFERENCES jobs(id) ON DELETE CASCADE,
    iteration      INT    NOT NULL,
    params         JSONB  NOT NULL DEFAULT '{}', -- tuned composite params (saturation, ha_screen, …)
    metrics        JSONB  NOT NULL DEFAULT '{}', -- measured finishMetrics (clipping, cast, background)
    det_score      DOUBLE PRECISION NOT NULL DEFAULT 0, -- deterministic metrics score (0..10)
    model_score    DOUBLE PRECISION NOT NULL DEFAULT 0, -- vision model's vote (0..10)
    combined_score DOUBLE PRECISION NOT NULL DEFAULT 0, -- 0.6*det + 0.4*model (the ranking key)
    reasoning      TEXT    NOT NULL DEFAULT '',
    chosen         BOOLEAN NOT NULL DEFAULT FALSE, -- the winning iteration kept as final.*
    created_at     BIGINT  NOT NULL,
    updated_at     BIGINT  NOT NULL
);
CREATE INDEX idx_finish_iterations_job ON finish_iterations(job_id);
CREATE INDEX idx_finish_iterations_chosen ON finish_iterations(job_id, chosen);
