-- A focal reducer is a SECOND, independent multiplier on the focal length, so it cannot share
-- barlow_x: a reducer usually lives permanently in the imaging train while a Barlow is swapped in for
-- planets, and a rig can carry both (740 × 2 × 0.66 = 977 mm). Folding them into one column would make
-- "×2 Barlow behind a ×0.66 reducer" unrepresentable and would silently rewrite whichever was saved
-- first. 0 means "not fitted" (×1) — the same convention barlow_x already uses — so every existing rig
-- keeps its exact optics after this migration.
ALTER TABLE equipment_setups ADD COLUMN reducer_x DOUBLE PRECISION NOT NULL DEFAULT 0;
