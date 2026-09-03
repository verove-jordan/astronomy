-- Per-connection default S3 storage class: the class every upload on this connection writes with (empty →
-- the provider default, STANDARD). MUST be an instant class (the app rejects an archived default at save
-- time) so the pipeline's own control writes stay readable; true archival is applied after the fact by the
-- storage-class/tier job. See internal/s3store glacier.go.
ALTER TABLE s3_connections ADD COLUMN default_storage_class TEXT NOT NULL DEFAULT '';
