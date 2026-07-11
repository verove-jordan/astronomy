-- Full-autonomy supervised finish: record which pipeline re-entry tier each iteration used
-- (A = composite, B = finish prep, C = re-stack) and the vision model's diagnosed defects, so the UI
-- can show what the agent saw and did. Additive columns with defaults so existing rows stay valid.
ALTER TABLE finish_iterations ADD COLUMN tier    TEXT  NOT NULL DEFAULT '';
ALTER TABLE finish_iterations ADD COLUMN defects JSONB NOT NULL DEFAULT '[]'; -- [{kind,severity,note}]
