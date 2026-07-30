package job

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"time"

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

// transferLocalRoot resolves the absolute local root a transfer walks: an explicit external LocalRoot (an
// external drive, already validated by the API against the browse allowlist) when set, else the namespace's
// DataDir/OutputDir.
func (m *Manager) transferLocalRoot(tr *TransferRequest) (string, error) {
	if tr.LocalRoot != "" {
		return filepath.Abs(tr.LocalRoot)
	}
	return filepath.Abs(m.transferRoot(tr.Namespace))
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
	rootAbs, err := m.transferLocalRoot(tr)
	if err != nil {
		return transfer.Result{}, err
	}
	req := transfer.Request{
		Op:           transfer.Op(tr.Op),
		LocalRoot:    rootAbs,
		RelPath:      tr.RelPath,
		Bucket:       tr.Bucket,
		KeyPrefix:    path.Join(tr.Prefix, tr.Namespace),
		Verify:       tr.Verify,
		ExcludeDirs:  tr.ExcludeDirs,
		SkipSymlinks: tr.SkipSymlinks,
		Concurrency:  m.cfg.S3Concurrency, // 0 → the transfer engine's default parallelism
	}
	// Let a manual pause stop the transfer BETWEEN files (checked in the ops loops), so a long S3 copy —
	// pull, push or a standalone transfer — pauses instead of ignoring the request until it finishes.
	if gate := m.gateFor(id); gate != nil {
		req.PauseRequested = gate.requested
	}
	// Classified layout: a data-namespace transfer places files at darks/offsets/flats/lum keys (recorded
	// in the s3_objects ledger) instead of the flat data/<rel> mirror. Nil plan → legacy behaviour. An
	// external-drive copy (LocalRoot set) is arbitrary files, not a classifiable capture — always a plain
	// mirror, never the classified plan.
	if tr.LocalRoot == "" {
		if plan, onStored := m.buildDataPlan(ctx, tr); plan != nil {
			req.Plan = plan
			req.OnStored = onStored
		}
	}
	return m.runTransferReq(ctx, id, client, req, label)
}

// runTransferReq runs an already-built transfer.Request, publishing throttled byte-level progress + a
// throughput EMA through the job event stream. Shared by execTransfer (whole-folder ops) and the low-disk
// s3Stager (subset staged ops, which build their own subset Plan). label overrides the step prefix.
func (m *Manager) runTransferReq(ctx context.Context, id int64, client *s3store.Client, req transfer.Request, label string) (transfer.Result, error) {
	name := label
	if name == "" {
		name = string(req.Op)
	}
	m.publish(Event{JobID: id, Status: store.JobRunning, Progress: 0, Step: name + " scanning…"})
	return transfer.Run(ctx, client, req, m.newTransferProgress(ctx, id, name))
}

// newTransferProgress builds the throttled byte-progress callback shared by every transfer-style job (the S3
// folder ops AND the S3→S3 move). The engine can fire a callback per file (thousands for a big folder), so
// it throttles the DB write + SSE publish to a few per second and smooths the throughput (débit) with an EMA
// so the UI shows a steady MB/s rather than a spiky instantaneous rate. A throttled log line names the file
// currently moving. name prefixes the step text ("upload"/"download"/"Moving"/…).
func (m *Manager) newTransferProgress(ctx context.Context, id int64, name string) func(transfer.Progress) {
	var lastPub, lastLog, lastSample time.Time
	var lastBytes int64
	var emaRate float64
	return func(pr transfer.Progress) {
		now := time.Now()
		if !lastSample.IsZero() {
			if dt := now.Sub(lastSample).Seconds(); dt > 0 {
				inst := float64(pr.BytesDone-lastBytes) / dt
				if inst < 0 {
					inst = 0 // a retried file restreams from zero — never report a negative rate
				}
				if emaRate == 0 {
					emaRate = inst
				} else {
					emaRate = 0.8*emaRate + 0.2*inst
				}
			}
		}
		lastSample, lastBytes = now, pr.BytesDone

		final := pr.TotalFiles > 0 && pr.Files >= pr.TotalFiles
		if !final && now.Sub(lastPub) < 250*time.Millisecond {
			return
		}
		lastPub = now

		pct := 0
		if pr.BytesTotal > 0 {
			pct = int(pr.BytesDone * 100 / pr.BytesTotal)
			if pct > 99 {
				pct = 99
			}
		}
		rate := int64(emaRate)
		step := fmt.Sprintf("%s %d/%d files · %s / %s · %s/s", name, pr.Files, pr.TotalFiles,
			humanBytes(pr.BytesDone), humanBytes(pr.BytesTotal), humanBytes(rate))
		_ = m.store.UpdateJobProgress(ctx, id, pct, step, "")
		ev := Event{JobID: id, Status: store.JobRunning, Progress: pct, Step: step,
			BytesDone: pr.BytesDone, BytesTotal: pr.BytesTotal, BytesPerSec: rate}
		if pr.Name != "" && now.Sub(lastLog) >= 900*time.Millisecond {
			lastLog = now
			ev.Line = "↑ " + pr.Name
			ev.Ts = now.UnixMilli()
		}
		m.publish(ev)
	}
}

// humanBytes formats a byte count in binary units (e.g. "3.4 GiB", "18.0 MiB"). Used for transfer step
// text + throughput; a rate is formatted the same and suffixed "/s" by the caller.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
