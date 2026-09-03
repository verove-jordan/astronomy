-- Measured tracking error, one row per frame.
--
-- Each row says where the telescope REALLY pointed when a sub was taken, relative to where the run
-- started. Folded on the mount's worm period these recover its periodic error, which is the whole
-- point: the mount's own PEC training needs a clear night and a fiddly hand-controller dance, while
-- this accumulates for free from frames that were being taken anyway.
CREATE TABLE IF NOT EXISTS tracking_samples (
    id           BIGSERIAL PRIMARY KEY,
    session_id   BIGINT NOT NULL REFERENCES capture_sessions(id) ON DELETE CASCADE,
    -- Seconds since the first sample of the session, at mid-exposure. Relative rather than absolute
    -- because the phase fold only ever cares about elapsed time within one run, and a power cycle
    -- loses the worm's phase reference anyway.
    t_sec        DOUBLE PRECISION NOT NULL,
    -- Drift in arcseconds. RA is already multiplied by cos(dec), so both are true angles on the sky
    -- and can be compared directly.
    ra_arcsec    DOUBLE PRECISION NOT NULL,
    dec_arcsec   DOUBLE PRECISION NOT NULL,
    -- How the offset was obtained: 'solve' (a full plate solve) or 'match' (a cheap star match
    -- against the previous frame). Kept because a match is relative and can accumulate error.
    source       TEXT NOT NULL DEFAULT 'match',
    created_at   BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS tracking_samples_session_idx
    ON tracking_samples (session_id, t_sec);
