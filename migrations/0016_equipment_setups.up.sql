-- Named telescope + camera rigs, moved server-side from the browser's localStorage. They were
-- per-browser, which broke the actual workflow: a mosaic is planned on the desktop and executed from
-- the phone at the telescope, where the rig has to be identical or every tile lands wrong. Field
-- names match the wire optics (sensor_w_px/sensor_h_px/barlow_x) rather than the old localStorage
-- shorthand, so the plan request needs no renaming layer. camera_name is filled by the capture
-- subsystem when a camera is connected ("fill from connected camera"). eyepieces stays JSONB — it is
-- display-only for the Tonight eyepiece calculator and never queried. Per the house DB conventions:
-- BIGINT millisecond timestamps, snake_case plural, case-insensitive unique name.
CREATE TABLE equipment_setups (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT             NOT NULL,
    focal_mm    DOUBLE PRECISION NOT NULL DEFAULT 0,
    aperture_mm DOUBLE PRECISION NOT NULL DEFAULT 0,
    pixel_um    DOUBLE PRECISION NOT NULL DEFAULT 0,
    sensor_w_px INTEGER          NOT NULL DEFAULT 0,
    sensor_h_px INTEGER          NOT NULL DEFAULT 0,
    barlow_x    DOUBLE PRECISION NOT NULL DEFAULT 0,
    camera_name TEXT             NOT NULL DEFAULT '',
    eyepieces   JSONB            NOT NULL DEFAULT '[]',
    favorite    BOOLEAN          NOT NULL DEFAULT FALSE,
    created_at  BIGINT           NOT NULL,
    updated_at  BIGINT           NOT NULL
);
-- Saving the same rig name again updates it in place (the old localStorage behaviour), so the name
-- must be unique case-insensitively.
CREATE UNIQUE INDEX idx_equipment_setups_name_lower ON equipment_setups (LOWER(name));
