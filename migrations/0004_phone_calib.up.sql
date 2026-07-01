-- Phone/DSLR calibration masters (iPhone DNG darks/bias/flats) for the milky-way nightscape path.
-- Kept in a dedicated table — NOT master_frames — because phone masters key by ISO / exposure /
-- sensor dimensions and are applied per-pixel in Go, not by Siril `calibrate` on gain/offset/bin; a
-- shared table would risk the deep-sky matcher (which ignores dimensions) selecting a phone master.
-- Per the house DB conventions: BIGINT millisecond timestamps, snake_case plural, indexed match columns.
CREATE TABLE phone_calib_masters (
    id           BIGSERIAL PRIMARY KEY,
    master_type  TEXT   NOT NULL,           -- DARK | FLAT | BIAS
    iso          BIGINT NOT NULL DEFAULT 0,
    exposure_ms  BIGINT NOT NULL DEFAULT 0, -- matters for DARK (dark current scales with exposure)
    camera_model TEXT   NOT NULL DEFAULT '',
    width        INT    NOT NULL DEFAULT 0,
    height       INT    NOT NULL DEFAULT 0,
    frame_count  INT    NOT NULL DEFAULT 0,
    path         TEXT   NOT NULL,           -- linear FITS master under the library dir
    created_at   BIGINT NOT NULL,
    updated_at   BIGINT NOT NULL
);
CREATE INDEX idx_phone_calib_match ON phone_calib_masters(master_type, iso, width, height);
