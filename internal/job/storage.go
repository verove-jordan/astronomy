package job

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/store"
)

// Full-S3 storage mode: a run pulls its capture folders from the S3 data mirror, processes locally
// (Siril/GIMP write absolute local paths, so the engine stays local-only), pushes the inputs and results
// back to S3, then frees the local copies — but only after each file is verified present on S3. This keeps
// local disk clean without ever losing data. Freeing the output dir is safe because previews/results serve
// local-first with an S3 fallback (see internal/api ensureServable / summarizeS3Run).

// wantsS3Storage reports whether this run should pull inputs from / push results to S3 and free the local
// copies. Excludes transfer, live-stacking and refine jobs, which manage their own I/O.
func (p RunRequest) wantsS3Storage() bool {
	return p.StorageMode == "s3" && p.S3 != nil && p.S3.Bucket != "" &&
		p.Transfer == nil && p.Live == nil && p.Refine == nil
}

// s3ResultTarget is the run's output object/run-id (run.json shape) — used to locate its output dir
// (OutputDir/<object>/<run_id>) so it can be pushed to and freed from the mirror.
type s3ResultTarget struct {
	Object string `json:"object"`
	RunID  string `json:"run_id"`
}

// dataInputRels returns the DataDir-relative capture folders for this run (deduped), skipping any that fall
// outside DataDir. These are pulled before, and freed after, a full-S3 run.
func (m *Manager) dataInputRels(p RunRequest) []string {
	dataAbs, err := filepath.Abs(m.cfg.DataDir)
	if err != nil {
		return nil
	}
	var rels []string
	seen := make(map[string]bool)
	for _, r := range p.inputRoots() {
		if r == "" {
			continue
		}
		abs, err := filepath.Abs(r)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(dataAbs, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		slash := filepath.ToSlash(rel)
		if !seen[slash] {
			seen[slash] = true
			rels = append(rels, slash)
		}
	}
	return rels
}

// pullS3Inputs downloads the run's capture folders from the S3 data mirror so a full-S3 run can process
// files that live only on S3. The download op skips same-size local files, so a partially-present folder is
// cheap and a fully-local one is a no-op.
func (m *Manager) pullS3Inputs(ctx context.Context, id int64, p RunRequest) error {
	for _, rel := range m.dataInputRels(p) {
		tr := &TransferRequest{Op: "download", Bucket: p.S3.Bucket, Prefix: p.S3.Prefix, Namespace: "data", RelPath: rel}
		if _, err := m.execTransfer(ctx, id, tr, "Downloading inputs"); err != nil {
			return fmt.Errorf("pull inputs %s: %w", rel, err)
		}
	}
	return nil
}

// pushAndFreeS3Run backs up the run's inputs and outputs to S3, then frees the local copies. Backing up the
// inputs first (a no-op when they were just pulled) makes freeing them safe even when they originated
// locally. A failed *push* fails the job (nothing is freed — the data is not yet safe); a failed *free* is
// reported but non-fatal (removeLocal aborts unless every file is verified on S3, so the data stays safe —
// only the local cleanup is incomplete, and the user can retry Free-local from the browser).
func (m *Manager) pushAndFreeS3Run(ctx context.Context, id int64, p RunRequest, res any) error {
	inputs := m.dataInputRels(p)

	// Resolve the run's output dir (OutputDir/<object>/<run_id>) from the result; blank when the run
	// produced no addressable output (then only inputs are handled).
	var t s3ResultTarget
	if data, err := json.Marshal(res); err == nil {
		_ = json.Unmarshal(data, &t)
	}
	var outRel string
	if t.Object != "" && t.RunID != "" {
		outRel = t.Object + "/" + t.RunID
	}

	// 1) Back up to S3 (sync = upload only what's missing). Any push failure fails the job.
	for _, rel := range inputs {
		tr := &TransferRequest{Op: "sync", Bucket: p.S3.Bucket, Prefix: p.S3.Prefix, Namespace: "data", RelPath: rel}
		if _, err := m.execTransfer(ctx, id, tr, "Backing up inputs"); err != nil {
			return fmt.Errorf("back up inputs %s: %w", rel, err)
		}
	}
	if outRel != "" {
		tr := &TransferRequest{Op: "sync", Bucket: p.S3.Bucket, Prefix: p.S3.Prefix, Namespace: "output", RelPath: outRel}
		if _, err := m.execTransfer(ctx, id, tr, "Uploading results"); err != nil {
			return fmt.Errorf("upload results %s: %w", outRel, err)
		}
	}

	// 2) Free local — verified against S3 (removeLocal deletes nothing unless every file is backed up).
	for _, rel := range inputs {
		tr := &TransferRequest{Op: "removeLocal", Bucket: p.S3.Bucket, Prefix: p.S3.Prefix, Namespace: "data", RelPath: rel}
		if _, err := m.execTransfer(ctx, id, tr, "Freeing local inputs"); err != nil {
			m.publish(Event{JobID: id, Status: store.JobRunning, Step: "kept local inputs (not fully backed up): " + rel})
		}
	}
	if outRel != "" {
		tr := &TransferRequest{Op: "removeLocal", Bucket: p.S3.Bucket, Prefix: p.S3.Prefix, Namespace: "output", RelPath: outRel}
		if _, err := m.execTransfer(ctx, id, tr, "Freeing local results"); err != nil {
			m.publish(Event{JobID: id, Status: store.JobRunning, Step: "kept local results (not fully backed up)"})
		}
	}
	return nil
}
