// Low-disk staged input model. When Options.Stager is set (full-S3 processing with the low-disk mode
// on), the pipeline does not require every capture on local disk at once: it scans the run remotely
// (ranged FITS-header reads, no downloads), then stages one frame-type/channel set at a time — download
// the set, build/stack it, then verified-free its raws before the next set — so peak local disk is about
// one channel's worth of frames instead of the whole dataset. The interface is consumer-side (like
// Catalog); the S3-backed implementation lives in internal/job so the pipeline stays pure and testable.
package pipeline

import (
	"context"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

// InputStager supplies a run's inputs on demand from remote storage.
type InputStager interface {
	// Scan builds the run inventory without requiring the captures on disk (it may internally fall back
	// to a full download when a frame cannot be classified remotely). The returned Frame.Path values are
	// the local paths the files will occupy once Ensure'd.
	Scan(ctx context.Context, roots []string, opts inspect.ScanOptions) (*inspect.Inventory, error)
	// Ensure downloads the given frame paths locally (size-skip idempotent). label feeds the job step text.
	Ensure(ctx context.Context, label string, paths []string) error
	// Free verified-deletes ONLY the given paths it downloaded and that are proven present on S3; it never
	// deletes an unverified file and is never fatal (a failure just keeps the files for the end-of-run sweep).
	Free(ctx context.Context, label string, paths []string)
	// Notes returns end-of-run observability lines (bytes staged/freed, peak local usage) for run.json.
	Notes() []string
}

// StagePullError wraps a failure to stage (download) a run's inputs mid-compute, carrying the run
// identity so the job layer can pause+retry the compute phase (errors.As, like PausedError). A manual
// pause surfaced by the stager's transfer is wrapped here too (via Unwrap → transfer.ErrPaused).
type StagePullError struct {
	RunID  string
	OutDir string
	Err    error
}

func (e *StagePullError) Error() string { return "staged input pull failed: " + e.Err.Error() }
func (e *StagePullError) Unwrap() error { return e.Err }

// scanInputs scans the run's roots via the stager when one is set (remote, no downloads), else the
// ordinary local ScanMany. Keeps the Stager==nil path byte-identical to before.
func (o Options) scanInputs(ctx context.Context, scanOpts inspect.ScanOptions) (*inspect.Inventory, error) {
	if o.Stager != nil {
		return o.Stager.Scan(ctx, o.scanRoots(), scanOpts)
	}
	return inspect.ScanMany(ctx, o.scanRoots(), scanOpts)
}

// calibStagePaths is the paths of every calibration frame (dark/flat/bias/darkflat) in the inventory —
// the set staged before buildRunMasters and freed after. Prior-session calib is not staged (it lives on
// S3, freed): dropMissing drops those catalogued paths so the deep master builds from this session's
// staged calib alone (a valid, if not deep-pooled, master — the low-disk trade-off).
func calibStagePaths(inv *inspect.Inventory) []string {
	var paths []string
	for _, s := range inv.Sets {
		switch s.Key.Type {
		case inspect.Dark, inspect.Flat, inspect.Bias, inspect.DarkFlat:
			for _, fr := range s.Frames {
				paths = append(paths, fr.Path)
			}
		}
	}
	return paths
}

// currentGroupPaths is the paths of the CURRENT-session light frames for a filter (all gain groups) — the
// per-channel wave staged before that channel is stacked and freed after. Prior-session reuse frames are
// left to their existing local-or-skip semantics (they are catalogued paths, freed to S3 and dropped).
func currentGroupPaths(plan *ReusePlan, filter string) []string {
	var paths []string
	for _, g := range plan.byFilter[filter] {
		if !g.Current {
			continue
		}
		for _, fr := range g.Frames {
			paths = append(paths, fr.Path)
		}
	}
	return paths
}
