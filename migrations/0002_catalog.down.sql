-- Reverse 0002_catalog.up.sql.

DROP INDEX IF EXISTS idx_master_frames_session;
ALTER TABLE master_frames DROP COLUMN IF EXISTS session_id;

DROP INDEX IF EXISTS idx_frames_target;
DROP INDEX IF EXISTS idx_frames_dec;
DROP INDEX IF EXISTS idx_frames_object_lower;
ALTER TABLE frames DROP COLUMN IF EXISTS target_id;
ALTER TABLE frames DROP COLUMN IF EXISTS has_coords;
ALTER TABLE frames DROP COLUMN IF EXISTS dec_deg;
ALTER TABLE frames DROP COLUMN IF EXISTS ra_deg;

DROP TABLE IF EXISTS targets;
