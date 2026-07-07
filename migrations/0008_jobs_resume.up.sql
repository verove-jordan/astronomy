-- Pause/resume: a job can enter a resumable "paused" state (manually, or automatically on a transient
-- S3 network error) instead of failing. `resume` is a small JSON checkpoint describing where to pick up
-- from — phase (pull|compute|push), the run id + output dir (so completed per-channel masters on disk are
-- reused) and a human reason. Empty object '{}' when the job is not paused. `status` stays free-text TEXT,
-- so the new 'paused' value needs no schema change. Additive + default, existing rows stay valid.
ALTER TABLE jobs ADD COLUMN resume JSONB NOT NULL DEFAULT '{}';
