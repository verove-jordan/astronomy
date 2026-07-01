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

// s3Client builds an S3 client from the host-environment credentials in config (never from the request).
func (m *Manager) s3Client() (*s3store.Client, error) {
	return s3store.New(s3store.Config{
		Endpoint:    m.cfg.S3Endpoint,
		Region:      m.cfg.S3Region,
		AccessKeyID: m.cfg.S3AccessKeyID,
		SecretKey:   m.cfg.S3SecretAccessKey,
		UseSSL:      m.cfg.S3UseSSL,
	})
}

// transferRoot maps a transfer namespace to its local root directory.
func (m *Manager) transferRoot(namespace string) string {
	if namespace == "output" {
		return m.cfg.OutputDir
	}
	return m.cfg.DataDir
}

// runTransfer executes an S3 transfer job (upload/sync/download/remove-local), publishing byte-level
// progress through the same event stream as a pipeline run (so it shows up in Tasks with a progress bar).
func (m *Manager) runTransfer(ctx context.Context, id int64, tr *TransferRequest) (any, error) {
	client, err := m.s3Client()
	if err != nil {
		return nil, err
	}
	rootAbs, err := filepath.Abs(m.transferRoot(tr.Namespace))
	if err != nil {
		return nil, err
	}
	req := transfer.Request{
		Op:        transfer.Op(tr.Op),
		LocalRoot: rootAbs,
		RelPath:   tr.RelPath,
		Bucket:    tr.Bucket,
		KeyPrefix: path.Join(tr.Prefix, tr.Namespace),
	}

	m.publish(Event{JobID: id, Status: store.JobRunning, Progress: 0, Step: string(tr.Op) + " starting"})
	onProg := func(pr transfer.Progress) {
		pct := 0
		if pr.BytesTotal > 0 {
			pct = int(pr.BytesDone * 100 / pr.BytesTotal)
			if pct > 99 {
				pct = 99
			}
		}
		step := fmt.Sprintf("%s %d/%d files", tr.Op, pr.Files, pr.TotalFiles)
		_ = m.store.UpdateJobProgress(ctx, id, pct, step, "")
		m.publish(Event{JobID: id, Status: store.JobRunning, Progress: pct, Step: step,
			BytesDone: pr.BytesDone, BytesTotal: pr.BytesTotal})
	}

	res, err := transfer.Run(ctx, client, req, onProg)
	if err != nil {
		return nil, err
	}
	return res, nil
}
