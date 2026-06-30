package pipeline

import (
	"context"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/inspect"
)

// PreviewCalibration reports, per inspected light channel, which library master dark/flat/bias would be
// applied to the given capture dirs — the data behind the Import "Calibration" panel. It runs no Siril
// and persists nothing (mirrors PreviewReuseMany). The Scanner lets callers share a directory-scan cache
// so re-inspecting an overlapping folder set doesn't re-read every header.
func PreviewCalibration(ctx context.Context, scanner Scanner, masters calib.MasterStore, dirs []string) (*calib.CalibPreview, error) {
	inv, err := scanner.ScanMany(ctx, dirs, inspect.DefaultScanOptions())
	if err != nil {
		return nil, err
	}
	lib, err := masters.ListMasters(ctx)
	if err != nil {
		return nil, err
	}
	pv := calib.SuggestForInventory(inv, lib)
	return &pv, nil
}
