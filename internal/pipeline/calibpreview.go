package pipeline

import (
	"context"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/inspect"
)

// PreviewCalibration reports, per inspected light channel, which master dark/flat/bias would be applied
// to the given capture dirs — the data behind the Import "Calibration" panel. Candidates mirror what a
// run would actually match against: the library masters PLUS the masters the run would build from the
// capture's own dark/flat/bias frames (calib.PreviewCandidates), so a selection that carries its own
// calibration previews as matched instead of "no matching dark". It runs no Siril and persists nothing
// (mirrors PreviewReuseMany). The Scanner lets callers share a directory-scan cache so re-inspecting an
// overlapping folder set doesn't re-read every header. When force is set the preview applies mismatched
// (gain/temperature/exposure) masters anyway, mirroring a forced run.
func PreviewCalibration(ctx context.Context, scanner Scanner, masters calib.MasterStore, dirs []string, force bool) (*calib.CalibPreview, error) {
	inv, err := scanner.ScanMany(ctx, dirs, inspect.DefaultScanOptions())
	if err != nil {
		return nil, err
	}
	lib, err := masters.ListMasters(ctx)
	if err != nil {
		return nil, err
	}
	pv := calib.SuggestForInventory(inv, calib.PreviewCandidates(inv, lib), force)
	return &pv, nil
}
