-- UI-managed S3 connections: endpoint + region + access key + the secret access key AES-256-GCM encrypted
-- at rest (the master key lives in the environment / a key file, never in this table). One connection may
-- be flagged default — the store the pipeline (import/process/results/backup) reads and writes. Per the
-- house DB conventions: BIGINT millisecond timestamps, snake_case plural, indexed match columns.
CREATE TABLE s3_connections (
    id            BIGSERIAL PRIMARY KEY,
    name          TEXT    NOT NULL,
    endpoint      TEXT    NOT NULL DEFAULT '',       -- host[:port], no scheme; empty → AWS S3 for the region
    region        TEXT    NOT NULL DEFAULT 'us-east-1',
    access_key_id TEXT    NOT NULL,
    secret_enc    BYTEA   NOT NULL,                  -- AES-256-GCM (nonce||ciphertext) of the secret access key
    use_ssl       BOOLEAN NOT NULL DEFAULT TRUE,
    is_default    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at    BIGINT  NOT NULL,
    updated_at    BIGINT  NOT NULL
);
-- At most one default connection at a time (partial unique index over the truthy rows).
CREATE UNIQUE INDEX idx_s3_connections_default ON s3_connections(is_default) WHERE is_default;
