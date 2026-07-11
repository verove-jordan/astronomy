package job

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/mode"
)

// Full-S3 storage mode: a run pulls its capture folders from the S3 data mirror, processes locally
// (Siril/GIMP write absolute local paths, so the engine stays local-only) and pushes the inputs and results
// back to S3. The local copies are NOT freed automatically — freeing is an explicit task action
// ("Remove local files" → Manager.FreeLocal), so a param-retry can reuse the still-local files instead of
// re-downloading everything. Every free is verified: removeLocal deletes nothing unless each file is present
// on S3. A re-finish (denoise / refine / rerun) of a run whose results were freed re-hydrates the output dir
// from S3 first (ensureRunDirLocal); previews/results also serve local-first with an S3 fallback (see
// internal/api ensureServable / summarizeS3Run).

// wantsS3Storage reports whether this run should pull inputs from / push results to S3 and free the local
// copies. Excludes transfer, live-stacking and refine jobs, which manage their own I/O.
func (p RunRequest) wantsS3Storage() bool {
	return p.StorageMode == "s3" && p.S3 != nil && p.S3.Bucket != "" &&
		p.Transfer == nil && p.Live == nil && p.Refine == nil
}

// lowDiskActive reports whether this run should use the staged low-disk S3 mode: a full-S3 deep-sky/nebula
// PROCESSING run (not a rerun/denoise re-finish), with the per-run override or the server default enabled.
// When active, run() skips the whole-folder pull and the pipeline stages inputs one wave at a time.
func (m *Manager) lowDiskActive(p RunRequest) bool {
	if !p.wantsS3Storage() || p.Rerun != nil || p.DenoiseFinal != nil {
		return false
	}
	mo, err := mode.ParseMode(p.Mode)
	if err != nil || (mo != mode.Deepsky && mo != mode.Nebula) {
		return false // staged mode is scoped to the mono-FITS deep-sky/nebula pipeline
	}
	if p.LowDisk != nil {
		return *p.LowDisk
	}
	return m.cfg.S3LowDisk
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

// s3RunTargets resolves the run's DataDir-relative input folders and its output dir rel
// (OutputDir/<object>/<run_id>, blank when the result carries no addressable output). Shared by the push and
// the explicit free so both operate on exactly the same set.
func (m *Manager) s3RunTargets(p RunRequest, res any) (inputs []string, outRel string) {
	inputs = m.dataInputRels(p)
	var t s3ResultTarget
	if data, err := json.Marshal(res); err == nil {
		_ = json.Unmarshal(data, &t)
	}
	if t.Object != "" && t.RunID != "" {
		outRel = t.Object + "/" + t.RunID
	}
	return inputs, outRel
}

// pushS3Run backs up the run's inputs and results to S3 (sync = upload only what's missing). Any push
// failure fails the job — the data is not yet safe. Local copies are NOT freed here (see FreeLocal): keeping
// them lets a param-retry reuse the files. An input folder that is no longer on disk — a re-finish that
// skipped the input pull, or a low-disk run that freed inputs wave-by-wave — is skipped (already on S3).
func (m *Manager) pushS3Run(ctx context.Context, id int64, p RunRequest, res any) error {
	inputs, outRel := m.s3RunTargets(p, res)
	dataAbs, _ := filepath.Abs(m.cfg.DataDir)
	for _, rel := range inputs {
		if _, err := os.Stat(filepath.Join(dataAbs, filepath.FromSlash(rel))); err != nil {
			continue // nothing local to back up (already on S3)
		}
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
	return nil
}

// ensureRunDirLocal makes a finished run's output dir available locally for a re-finish (denoise / refine /
// rerun) when the run's results were pushed to S3 and freed. No-op when final.tif is already present or the
// run is not on S3; otherwise it pulls the run's output tree (OutputDir/<object>/<stamp>) from the S3 output
// mirror. The download skips same-size local files, so a retry reuses whatever is already on disk.
func (m *Manager) ensureRunDirLocal(ctx context.Context, id int64, p RunRequest, runDir string) error {
	if runDir == "" || p.S3 == nil || p.S3.Bucket == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(runDir, "final.tif")); err == nil {
		return nil // already local
	}
	outAbs, err := filepath.Abs(m.cfg.OutputDir)
	if err != nil {
		return nil
	}
	abs, err := filepath.Abs(runDir)
	if err != nil {
		return nil
	}
	rel, err := filepath.Rel(outAbs, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}
	tr := &TransferRequest{Op: "download", Bucket: p.S3.Bucket, Prefix: p.S3.Prefix, Namespace: "output", RelPath: filepath.ToSlash(rel)}
	if _, err := m.execTransfer(ctx, id, tr, "Fetching results"); err != nil {
		return fmt.Errorf("pull results %s: %w", rel, err)
	}
	return nil
}
