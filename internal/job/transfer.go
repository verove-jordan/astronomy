package job

import (
	"context"
	"fmt"
	"path"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/s3store"
	"github.com/verove-jordan/astronomy/internal/store"
	"github.com/verove-jordan/astronomy/internal/transfer"
)

// s3Client builds an S3 client from the resolved pipeline config (default UI connection, else env).
func (m *Manager) s3Client(ctx context.Context) (*s3store.Client, error) {
	return s3store.New(m.s3ConfigResolved(ctx))
}

// s3ConfigResolved returns the default UI-managed connection's config (decrypted) when one is set, else the
// host-environment credentials — the same "default connection drives the pipeline" resolution the API uses.
// Credentials still never come from the request body.
func (m *Manager) s3ConfigResolved(ctx context.Context) s3store.Config {
	if m.s3conn != nil {
		if cfg, ok, err := m.s3conn.DefaultConfig(ctx); err == nil && ok {
			return cfg
		}
	}
	return s3store.Config{
		Endpoint:    m.cfg.S3Endpoint,
		Region:      m.cfg.S3Region,
		AccessKeyID: m.cfg.S3AccessKeyID,
		SecretKey:   m.cfg.S3SecretAccessKey,
		UseSSL:      m.cfg.S3UseSSL,
	}
}

// transferRoot maps a transfer namespace to its local root directory.
func (m *Manager) transferRoot(namespace string) string {
	if namespace == "output" {
		return m.cfg.OutputDir
	}
	return m.cfg.DataDir
}

// runTransfer executes a standalone S3 transfer job (upload/sync/download/remove-local) — the whole job is
// the transfer. It reuses the same event stream as a pipeline run (so it shows up in Tasks with a bar).
func (m *Manager) runTransfer(ctx context.Context, id int64, tr *TransferRequest) (any, error) {
	res, err := m.execTransfer(ctx, id, tr, "")
	if err != nil {
		return nil, err
	}
	return res, nil
}

// execTransfer runs one transfer (upload/sync/download/remove-local) under job id, publishing byte-level
// progress through the job event stream. label overrides the step prefix (empty → the op name); the
// full-S3 run orchestration passes friendly labels ("Uploading results", "Freeing local inputs", …) so its
// internal transfer steps read clearly in the Tasks view. Shared by standalone transfers and full-S3 runs.
func (m *Manager) execTransfer(ctx context.Context, id int64, tr *TransferRequest, label string) (transfer.Result, error) {
	client, err := m.s3Client(ctx)
	if err != nil {
		return transfer.Result{}, err
	}
	rootAbs, err := filepath.Abs(m.transferRoot(tr.Namespace))
	if err != nil {
		return transfer.Result{}, err
	}
	req := transfer.Request{
		Op:        transfer.Op(tr.Op),
		LocalRoot: rootAbs,
		RelPath:   tr.RelPath,
		Bucket:    tr.Bucket,
		KeyPrefix: path.Join(tr.Prefix, tr.Namespace),
	}
	name := label
	if name == "" {
		name = string(tr.Op)
	}

	m.publish(Event{JobID: id, Status: store.JobRunning, Progress: 0, Step: name + " starting"})
	onProg := func(pr transfer.Progress) {
		pct := 0
		if pr.BytesTotal > 0 {
			pct = int(pr.BytesDone * 100 / pr.BytesTotal)
			if pct > 99 {
				pct = 99
			}
		}
		step := fmt.Sprintf("%s %d/%d files", name, pr.Files, pr.TotalFiles)
		_ = m.store.UpdateJobProgress(ctx, id, pct, step, "")
		m.publish(Event{JobID: id, Status: store.JobRunning, Progress: pct, Step: step,
			BytesDone: pr.BytesDone, BytesTotal: pr.BytesTotal})
	}
	return transfer.Run(ctx, client, req, onProg)
}
