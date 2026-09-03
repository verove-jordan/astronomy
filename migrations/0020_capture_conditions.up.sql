-- What the sky was actually doing while the frames were taken.
--
-- capture_sessions already records WHAT was shot (sequence, per-filter tallies, every frame). It says
-- nothing about the conditions, and that is the half you need months later when deciding whether two
-- nights of the same target can be stacked together: a 60%-lit Moon 20 degrees off the target, or a
-- transparency that collapsed at 01:00, explains a set that will never blend.
--
-- These rows have to be written WHILE THE SESSION RUNS. The weather provider (internal/weather) is a
-- forecast service: Open-Meteo is asked for past_days=1/forecast_days=2, 7Timer and NOAA SWPC are
-- recent-or-forward only, and there is no archive endpoint anywhere in the engine. Conditions older
-- than about a day are simply not retrievable, so a nightly snapshot is the only honest record and
-- sessions captured before this migration can never be backfilled.
--
-- Per the house DB conventions: BIGINT millisecond timestamps, snake_case plural tables.

-- The site the session was shot from, plus the rolled-up conditions record.
--
-- The summary is denormalized onto the session on purpose: the logbook list shows one line per night
-- and must not join or aggregate to draw it. It is rewritten after EVERY sample rather than once at
-- the end, so a session killed mid-night (which InterruptOrphanedCaptureSessions flips to
-- 'interrupted' at the next boot) still carries an accurate record of the hours it did get.
ALTER TABLE capture_sessions
    ADD COLUMN IF NOT EXISTS site_lat           DOUBLE PRECISION NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS site_lon           DOUBLE PRECISION NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS site_elevation_m   DOUBLE PRECISION NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS conditions_summary JSONB            NOT NULL DEFAULT '{}';

-- One row per hourly observation of the sky over the session.
--
-- Hourly, not per-frame: the weather feeds themselves are hourly, so sampling faster would repeat the
-- same numbers while burning the free-tier request budget. The ephemeris half (Moon, target altitude)
-- does move continuously, but interpolating it later from at_ms is exact and free.
CREATE TABLE IF NOT EXISTS capture_conditions (
    id             BIGSERIAL PRIMARY KEY,
    session_id     BIGINT NOT NULL REFERENCES capture_sessions(id) ON DELETE CASCADE,
    -- Absolute epoch ms, unlike tracking_samples.t_sec: these rows are joined against a wall-clock
    -- weather timeline and against the frames' own DATE-OBS, so a session-relative offset would have
    -- to be undone at every read.
    at_ms          BIGINT NOT NULL,
    -- 'running' | 'paused'. Recorded because a pause is usually the observer waiting out cloud, which
    -- makes the paused samples the most interesting ones in the table.
    session_status TEXT   NOT NULL DEFAULT 'running',

    -- weather.Hour, flattened. Zero means "the feed did not supply it" — that is the weather package's
    -- own documented contract, and it is preserved here rather than being turned into NULL so readers
    -- need only one rule.
    cloud_pct      DOUBLE PRECISION NOT NULL DEFAULT 0,
    cloud_low      DOUBLE PRECISION NOT NULL DEFAULT 0,
    cloud_mid      DOUBLE PRECISION NOT NULL DEFAULT 0,
    cloud_high     DOUBLE PRECISION NOT NULL DEFAULT 0,
    seeing_arcsec  DOUBLE PRECISION NOT NULL DEFAULT 0,
    transparency   DOUBLE PRECISION NOT NULL DEFAULT 0,
    humidity_pct   DOUBLE PRECISION NOT NULL DEFAULT 0,
    dew_point_c    DOUBLE PRECISION NOT NULL DEFAULT 0,
    temp_c         DOUBLE PRECISION NOT NULL DEFAULT 0,
    dew_spread_c   DOUBLE PRECISION NOT NULL DEFAULT 0,
    dew_risk       TEXT             NOT NULL DEFAULT '',
    wind_kmh       DOUBLE PRECISION NOT NULL DEFAULT 0,
    gust_kmh       DOUBLE PRECISION NOT NULL DEFAULT 0,
    jet300_kmh     DOUBLE PRECISION NOT NULL DEFAULT 0,
    cape           DOUBLE PRECISION NOT NULL DEFAULT 0,
    lifted_index   DOUBLE PRECISION NOT NULL DEFAULT 0,
    visibility_m   DOUBLE PRECISION NOT NULL DEFAULT 0,
    precip_pct     DOUBLE PRECISION NOT NULL DEFAULT 0,
    aod            DOUBLE PRECISION NOT NULL DEFAULT 0,
    verdict        DOUBLE PRECISION NOT NULL DEFAULT 0,
    kp_now         DOUBLE PRECISION NOT NULL DEFAULT 0,
    kp_max         DOUBLE PRECISION NOT NULL DEFAULT 0,
    aurora         TEXT             NOT NULL DEFAULT '',

    -- Computed locally from internal/astro, never fetched: pure ephemeris that stays correct even on a
    -- night when every network feed was down.
    moon_illum           DOUBLE PRECISION NOT NULL DEFAULT 0,
    moon_alt_deg         DOUBLE PRECISION NOT NULL DEFAULT 0,
    moon_az_deg          DOUBLE PRECISION NOT NULL DEFAULT 0,
    moon_phase_angle_deg DOUBLE PRECISION NOT NULL DEFAULT 0,
    -- Angular distance from the Moon to where the scope was pointed. Zero when the session carried no
    -- target coordinates, which target_valid distinguishes from a genuine occultation-grade zero.
    moon_sep_deg         DOUBLE PRECISION NOT NULL DEFAULT 0,
    target_alt_deg       DOUBLE PRECISION NOT NULL DEFAULT 0,
    target_az_deg        DOUBLE PRECISION NOT NULL DEFAULT 0,
    -- Airmass at the target. 0 = unknown or below the horizon; a real value is >= 1.
    target_airmass       DOUBLE PRECISION NOT NULL DEFAULT 0,
    target_valid         BOOLEAN          NOT NULL DEFAULT FALSE,

    -- Light pollution at the site (internal/lightpollution). Near-static per site, but stored per row
    -- so a session shot from a dark site keeps its own value after the user moves home again.
    sqm    DOUBLE PRECISION NOT NULL DEFAULT 0,
    bortle INTEGER          NOT NULL DEFAULT 0,

    -- now - SiteForecast.IssuedMs: how stale the feed behind this row was. A large value means the
    -- provider served its cache (or its stale-grace fallback) rather than a fresh observation.
    forecast_age_ms BIGINT NOT NULL DEFAULT 0,
    -- 'live' | 'cached' | 'unavailable' — so an all-zero weather row reads as "the feed was down",
    -- not as "the sky was perfectly clear and perfectly still".
    source          TEXT   NOT NULL DEFAULT '',

    created_at BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS capture_conditions_session_idx
    ON capture_conditions (session_id, at_ms);

-- The whole hourly forecast as it stood at the start and at the end of the session.
--
-- Separate from capture_sessions because one payload is tens of kilobytes and the logbook list selects
-- every session column at once; keeping the blobs here means the list stays cheap and the pair can be
-- pruned later without touching the sessions table. Storing both ends is what makes "what was
-- forecast vs what actually happened" answerable.
CREATE TABLE IF NOT EXISTS capture_forecasts (
    id         BIGSERIAL PRIMARY KEY,
    session_id BIGINT NOT NULL REFERENCES capture_sessions(id) ON DELETE CASCADE,
    kind       TEXT   NOT NULL,               -- 'start' | 'end'
    at_ms      BIGINT NOT NULL,
    payload    JSONB  NOT NULL DEFAULT '{}',  -- weather.SiteForecast, verbatim
    created_at BIGINT NOT NULL
);

-- One snapshot per kind per session, so re-writing the 'end' snapshot (a resumed or retried finish)
-- is an idempotent upsert rather than a duplicate row.
CREATE UNIQUE INDEX IF NOT EXISTS capture_forecasts_session_kind_idx
    ON capture_forecasts (session_id, kind);
