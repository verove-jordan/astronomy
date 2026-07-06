// Package s3conn is the service layer for UI-managed S3 connections: it encrypts/decrypts their secret
// access keys (via internal/secret), persists them (via internal/store), and hands out s3store clients/
// configs. The default connection is what the pipeline (import/process/results/backup) reads and writes;
// the explorer can target any connection by id. Secret keys are decrypted only here, only to build a client
// — never returned to the UI.
package s3conn

import (
	"context"
	"fmt"

	"github.com/verove-jordan/astronomy/internal/s3store"
	"github.com/verove-jordan/astronomy/internal/secret"
	"github.com/verove-jordan/astronomy/internal/store"
)

// Service ties the connection table to encryption and s3store client construction.
type Service struct {
	store *store.Store
	box   *secret.Box
}

// New builds the service. box must be non-nil (callers gate the whole feature on encryption being available).
func New(st *store.Store, box *secret.Box) *Service { return &Service{store: st, box: box} }

// List returns all connections (their SecretEnc is json:"-", so marshaling is safe — the secret never ships).
func (svc *Service) List(ctx context.Context) ([]store.S3Connection, error) {
	return svc.store.ListS3Connections(ctx)
}

// Create encrypts secretKey, stores the connection, and makes it the default when asked or when it is the
// first one. Returns the new id.
func (svc *Service) Create(ctx context.Context, name, endpoint, region, accessKeyID, secretKey string, useSSL, makeDefault bool) (int64, error) {
	enc, err := svc.box.Seal([]byte(secretKey))
	if err != nil {
		return 0, err
	}
	id, err := svc.store.CreateS3Connection(ctx, store.S3Connection{
		Name: name, Endpoint: endpoint, Region: region, AccessKeyID: accessKeyID, SecretEnc: enc, UseSSL: useSSL,
	})
	if err != nil {
		return 0, err
	}
	conns, _ := svc.store.ListS3Connections(ctx)
	if makeDefault || len(conns) == 1 {
		if err := svc.store.SetDefaultS3Connection(ctx, id); err != nil {
			return id, err
		}
	}
	return id, nil
}

// Update changes a connection's fields. A blank newSecret keeps the stored secret (edit without re-entry).
func (svc *Service) Update(ctx context.Context, id int64, name, endpoint, region, accessKeyID, newSecret string, useSSL bool) error {
	var enc []byte
	if newSecret != "" {
		var err error
		if enc, err = svc.box.Seal([]byte(newSecret)); err != nil {
			return err
		}
	}
	return svc.store.UpdateS3Connection(ctx, id, name, endpoint, region, accessKeyID, enc, useSSL)
}

// Delete removes a connection.
func (svc *Service) Delete(ctx context.Context, id int64) error {
	return svc.store.DeleteS3Connection(ctx, id)
}

// SetDefault makes id the sole default connection.
func (svc *Service) SetDefault(ctx context.Context, id int64) error {
	return svc.store.SetDefaultS3Connection(ctx, id)
}

// ConfigFor decrypts one connection into an s3store.Config.
func (svc *Service) ConfigFor(ctx context.Context, id int64) (s3store.Config, error) {
	c, err := svc.store.GetS3Connection(ctx, id)
	if err != nil {
		return s3store.Config{}, err
	}
	return svc.toConfig(c)
}

// ClientFor builds an S3 client for a connection id.
func (svc *Service) ClientFor(ctx context.Context, id int64) (*s3store.Client, error) {
	cfg, err := svc.ConfigFor(ctx, id)
	if err != nil {
		return nil, err
	}
	return s3store.New(cfg)
}

// DefaultConfig returns the default connection's decrypted config, ok=false when none is set. This is what
// makes a UI connection drive the pipeline: the S3-config resolution falls back to env only when ok=false.
func (svc *Service) DefaultConfig(ctx context.Context) (cfg s3store.Config, ok bool, err error) {
	c, ok, err := svc.store.GetDefaultS3Connection(ctx)
	if err != nil || !ok {
		return s3store.Config{}, ok, err
	}
	cfg, err = svc.toConfig(c)
	if err != nil {
		return s3store.Config{}, false, err
	}
	return cfg, true, nil
}

func (svc *Service) toConfig(c store.S3Connection) (s3store.Config, error) {
	secretKey, err := svc.box.Open(c.SecretEnc)
	if err != nil {
		return s3store.Config{}, fmt.Errorf("s3 connection %q (id %d): %w", c.Name, c.ID, err)
	}
	return s3store.Config{
		Endpoint:    c.Endpoint,
		Region:      c.Region,
		AccessKeyID: c.AccessKeyID,
		SecretKey:   string(secretKey),
		UseSSL:      c.UseSSL,
	}, nil
}
