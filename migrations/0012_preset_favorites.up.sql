-- Preset favorites: a star the user toggles on saved presets — starred presets sort first in the
-- Import picker. Built-in presets are code-defined and never rows here, so they cannot be starred.
ALTER TABLE processing_presets ADD COLUMN favorite BOOLEAN NOT NULL DEFAULT FALSE;
