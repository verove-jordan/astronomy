-- app_settings: a tiny key/value store for engine-side preferences that are NOT per-run job params. Today
-- it holds the calibration-library S3 mirror location (bucket + prefix), recorded when the user copies the
-- library to S3, so any later run can pull a matched master back from that mirror when it is absent locally
-- (the library is kept as a synced mirror, but a given machine may not hold every file). Per the house DB
-- conventions: BIGINT millisecond timestamps, snake_case plural.
CREATE TABLE app_settings (
    key        TEXT   PRIMARY KEY,
    value      TEXT   NOT NULL,
    updated_at BIGINT NOT NULL
);
