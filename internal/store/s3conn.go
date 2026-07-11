package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// S3Connection is a UI-managed S3 endpoint whose secret access key is encrypted at rest (SecretEnc holds
// AES-256-GCM nonce||ciphertext — the store never decrypts; that is the s3conn service's job). SecretEnc is
// json:"-" so it can never leak to the UI even if the struct is marshaled directly.
type S3Connection struct {
	ID          int64  `json:"id" db:"id"`
	Name        string `json:"name" db:"name"`
	Endpoint    string `json:"endpoint" db:"endpoint"`
	Region      string `json:"region" db:"region"`
	AccessKeyID string `json:"access_key_id" db:"access_key_id"`
	SecretEnc   []byte `json:"-" db:"secret_enc"`
	UseSSL      bool   `json:"use_ssl" db:"use_ssl"`
	IsDefault   bool   `json:"is_default" db:"is_default"`
	CreatedAt   int64  `json:"created_at" db:"created_at"`
	UpdatedAt   int64  `json:"updated_at" db:"updated_at"`
}

const s3ConnCols = `id,name,endpoint,region,access_key_id,secret_enc,use_ssl,is_default,created_at,updated_at`

// CreateS3Connection inserts a connection (never default — the caller sets the default separately in a
// transaction) and returns its id.
func (s *Store) CreateS3Connection(ctx context.Context, c S3Connection) (int64, error) {
	now := nowMs()
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO s3_connections(name,endpoint,region,access_key_id,secret_enc,use_ssl,is_default,created_at,updated_at)
		 VALUES($1,$2,$3,$4,$5,$6,false,$7,$7) RETURNING id`,
		c.Name, c.Endpoint, c.Region, c.AccessKeyID, c.SecretEnc, c.UseSSL, now).Scan(&id)
	return id, err
}

// ListS3Connections returns all connections (oldest first for a stable UI order).
func (s *Store) ListS3Connections(ctx context.Context) ([]S3Connection, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+s3ConnCols+` FROM s3_connections ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[S3Connection])
}

// GetS3Connection returns one connection (pgx.ErrNoRows when missing).
func (s *Store) GetS3Connection(ctx context.Context, id int64) (S3Connection, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+s3ConnCols+` FROM s3_connections WHERE id=$1`, id)
	if err != nil {
		return S3Connection{}, err
	}
	defer rows.Close()
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[S3Connection])
}

// GetDefaultS3Connection returns the default connection, or ok=false when none is set.
func (s *Store) GetDefaultS3Connection(ctx context.Context) (S3Connection, bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+s3ConnCols+` FROM s3_connections WHERE is_default LIMIT 1`)
	if err != nil {
		return S3Connection{}, false, err
	}
	defer rows.Close()
	c, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[S3Connection])
	if errors.Is(err, pgx.ErrNoRows) {
		return S3Connection{}, false, nil
	}
	if err != nil {
		return S3Connection{}, false, err
	}
	return c, true, nil
}

// UpdateS3Connection updates a connection's fields. A nil secretEnc keeps the stored secret (so the UI can
// edit a connection without re-entering the secret key); a non-nil secretEnc replaces it.
func (s *Store) UpdateS3Connection(ctx context.Context, id int64, name, endpoint, region, accessKeyID string, secretEnc []byte, useSSL bool) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE s3_connections
		    SET name=$2, endpoint=$3, region=$4, access_key_id=$5,
		        secret_enc=COALESCE($6,secret_enc), use_ssl=$7, updated_at=$8
		  WHERE id=$1`,
		id, name, endpoint, region, accessKeyID, secretEnc, useSSL, nowMs())
	return err
}

// DeleteS3Connection removes a connection.
func (s *Store) DeleteS3Connection(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM s3_connections WHERE id=$1`, id)
	return err
}

// SetDefaultS3Connection makes id the sole default (clears any other in the same transaction, so the
// one-default partial-unique index is never violated).
func (s *Store) SetDefaultS3Connection(ctx context.Context, id int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // best-effort on the error path
	if _, err := tx.Exec(ctx, `UPDATE s3_connections SET is_default=false WHERE is_default`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE s3_connections SET is_default=true, updated_at=$2 WHERE id=$1`, id, nowMs()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
