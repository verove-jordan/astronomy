package job

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/transfer"
)

// s3Stager implements pipeline.InputStager for the low-disk staged S3 mode: it scans a run remotely
// (ranged FITS-header reads — see stager_scan.go), then downloads and verified-frees ONE frame-type or
// channel wave at a time through the transfer engine, so peak local disk stays about one channel's frames
// instead of the whole capture set. The subset each wave moves is a plan restricted to that wave's files
// (their ledger keys); a legacy capture with no ledger rows was already fully pulled by Scan, so its
// per-wave Ensure/Free become no-ops and it relies on the end-of-run sweep.
type s3Stager struct {
	m       *Manager
	jobID   int64
	bucket  string
	prefix  string
	dataDir string   // absolute DataDir (the "data" namespace local root)
	roots   []string // DataDir-relative capture folders (e.g. "M92")

	downloaded int64
	freed      int64
	waves      int64
	notes      []string
}

// newS3Stager builds the stager for a low-disk run from its request (bucket/prefix + capture folders).
func (m *Manager) newS3Stager(id int64, p RunRequest) (*s3Stager, error) {
	dataAbs, err := filepath.Abs(m.cfg.DataDir)
	if err != nil {
		return nil, err
	}
	return &s3Stager{
		m: m, jobID: id, bucket: p.S3.Bucket, prefix: p.S3.Prefix,
		dataDir: dataAbs, roots: m.dataInputRels(p),
	}, nil
}

// Ensure downloads the given frame paths locally, one capture-folder subset at a time. A pull failure
// returns the error so the pipeline pauses+retries the compute phase (StagePullError).
func (s *s3Stager) Ensure(ctx context.Context, label string, paths []string) error {
	byRoot := s.splitByRoot(paths)
	if len(byRoot) == 0 {
		return nil
	}
	client, err := s.m.s3Client(ctx)
	if err != nil {
		return err
	}
	s.waves++
	for root, want := range byRoot {
		plan := s.planFor(ctx, root, want)
		if len(plan) == 0 {
			continue // no ledger rows (legacy → Scan already full-pulled) or nothing to stage
		}
		req := s.baseReq(transfer.OpDownload, root, plan, false)
		if _, terr := s.m.runTransferReq(ctx, s.jobID, client, req, "Staging "+label); terr != nil {
			return terr
		}
		s.downloaded += planBytes(plan)
	}
	return nil
}

// Free verified-deletes the given (just-processed) frame paths, one subset at a time. Non-fatal: a verify
// mismatch or transient error keeps the files for the end-of-run sweep and records a note — a run never
// fails because a free did not complete.
func (s *s3Stager) Free(ctx context.Context, label string, paths []string) {
	byRoot := s.splitByRoot(paths)
	if len(byRoot) == 0 {
		return
	}
	client, err := s.m.s3Client(ctx)
	if err != nil {
		s.notes = append(s.notes, "low-disk: could not free "+label+": "+err.Error())
		return
	}
	for root, want := range byRoot {
		plan := s.planFor(ctx, root, want)
		if len(plan) == 0 {
			continue
		}
		req := s.baseReq(transfer.OpRemoveLocal, root, plan, true)
		if _, terr := s.m.runTransferReq(ctx, s.jobID, client, req, "Freeing "+label); terr != nil {
			s.notes = append(s.notes, fmt.Sprintf("low-disk: kept %s local (not fully verified on S3 yet): %v", label, terr))
			continue
		}
		s.freed += planBytes(plan)
	}
}

// Notes returns the end-of-run low-disk summary for run.json.
func (s *s3Stager) Notes() []string {
	out := s.notes
	if s.waves > 0 {
		out = append(out, fmt.Sprintf(
			"low-disk: staged %d wave(s) — downloaded %s, freed %s (peak local ≈ one channel's frames, not the whole set)",
			s.waves, humanBytes(s.downloaded), humanBytes(s.freed)))
	}
	return out
}

// baseReq builds the transfer.Request for one staged subset op against a capture folder, wired for the
// job's pause gate so a long copy can be paused between files.
func (s *s3Stager) baseReq(op transfer.Op, root string, plan []transfer.PlannedFile, plannedOnly bool) transfer.Request {
	req := transfer.Request{
		Op:          op,
		LocalRoot:   s.dataDir,
		RelPath:     root,
		Bucket:      s.bucket,
		KeyPrefix:   path.Join(s.prefix, "data"),
		Plan:        plan,
		PlannedOnly: plannedOnly,
		Concurrency: s.m.cfg.S3Concurrency,
	}
	if gate := s.m.gateFor(s.jobID); gate != nil {
		req.PauseRequested = gate.requested
	}
	return req
}

// planFor restricts the folder's classified download plan (from the s3_objects ledger) to the wanted
// folder-relative files. Empty when the folder has no ledger rows (a legacy capture Scan already pulled).
func (s *s3Stager) planFor(ctx context.Context, root string, want map[string]bool) []transfer.PlannedFile {
	tr := &TransferRequest{Op: "download", Bucket: s.bucket, Prefix: s.prefix, Namespace: "data", RelPath: root}
	var out []transfer.PlannedFile
	for _, pf := range s.m.downloadPlan(ctx, tr) {
		if want[pf.Rel] {
			out = append(out, pf)
		}
	}
	return out
}

// splitByRoot groups absolute frame paths by the capture folder (root) they live under, as a set of
// folder-relative slash paths — the Rel keys a transfer plan matches on. Paths outside every root are
// dropped (they are not staged inputs).
func (s *s3Stager) splitByRoot(paths []string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		for _, root := range s.roots {
			rootAbs := filepath.Join(s.dataDir, filepath.FromSlash(root))
			rel, rerr := filepath.Rel(rootAbs, abs)
			if rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			if out[root] == nil {
				out[root] = map[string]bool{}
			}
			out[root][filepath.ToSlash(rel)] = true
			break
		}
	}
	return out
}

// planBytes sums a plan's file sizes (for the staged download/free accounting).
func planBytes(plan []transfer.PlannedFile) int64 {
	var n int64
	for _, pf := range plan {
		n += pf.Size
	}
	return n
}
