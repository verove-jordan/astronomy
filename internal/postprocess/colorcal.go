package postprocess

import (
	"context"
	"fmt"
	"strings"

	"github.com/verove-jordan/astronomy/internal/siril"
)

// ColorCalOptions parameterize the color-calibration stage.
type ColorCalOptions struct {
	Enabled     bool
	RemoveGreen bool
	Solve       siril.SolveOptions
	Spcc        siril.SpccOptions
}

// ColorCalibrate calibrates the named linear image in dir, overwriting it. It first tries
// plate-solve + SPCC (natural color, neutral background); if that fails — no catalog, offline, or
// an unsolvable field — it falls back to SCNR green removal (background gradients are assumed
// already extracted upstream). It returns a human-readable note describing which path ran; only a
// hard Siril failure of the fallback returns an error, so a calibration miss never aborts the run.
func ColorCalibrate(ctx context.Context, runner *siril.Runner, dir, base string, opts ColorCalOptions) (string, error) {
	if opts.Enabled {
		res, err := runner.Run(ctx, dir, siril.ColorCalibrateScript(base, base, opts.Solve, opts.Spcc), nil)
		if err == nil && res != nil && !solveFailed(res.Log) {
			return "SPCC color calibration applied", nil
		}
	}
	if !opts.RemoveGreen {
		if opts.Enabled {
			return "plate-solve/SPCC unavailable — left color uncalibrated", nil
		}
		return "", nil
	}
	if _, err := runner.Run(ctx, dir, siril.NeutralizeScript(base, base, 0, 0), nil); err != nil {
		return "", fmt.Errorf("color neutralization: %w", err)
	}
	if opts.Enabled {
		return "plate-solve/SPCC unavailable — used background-neutralization fallback", nil
	}
	return "background-neutralization color balance", nil
}

// solveFailed scans the Siril log for plate-solve/SPCC failure markers, in case Siril logs the
// error but still exits zero. Conservative: unknown output is treated as success.
func solveFailed(log string) bool {
	l := strings.ToLower(log)
	for _, marker := range []string{
		"plate solving failed", "failed to solve", "no astrometric solution",
		"could not solve", "not been plate", "spcc failed", "no stars detected",
	} {
		if strings.Contains(l, marker) {
			return true
		}
	}
	return false
}
