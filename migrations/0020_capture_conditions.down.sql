DROP INDEX IF EXISTS capture_forecasts_session_kind_idx;
DROP TABLE IF EXISTS capture_forecasts;

DROP INDEX IF EXISTS capture_conditions_session_idx;
DROP TABLE IF EXISTS capture_conditions;

ALTER TABLE capture_sessions
    DROP COLUMN IF EXISTS conditions_summary,
    DROP COLUMN IF EXISTS site_elevation_m,
    DROP COLUMN IF EXISTS site_lon,
    DROP COLUMN IF EXISTS site_lat;
