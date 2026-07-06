DROP INDEX IF EXISTS idx_jobs_series;
ALTER TABLE jobs DROP COLUMN IF EXISTS series_id;
DROP TABLE IF EXISTS agent_series;
ALTER TABLE finish_iterations DROP COLUMN IF EXISTS preset;
ALTER TABLE finish_iterations DROP COLUMN IF EXISTS png_path;
