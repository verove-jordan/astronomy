package postprocess

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/siril"
)

// spccTimeout bounds the SPCC step. SPCC plate-solves then fetches a photometric star catalogue
// online (Gaia DR3 xp_sampled); that fetch is normally seconds but can stall, and the CLI drives the
// pipeline with a background context (no deadline). Bound it so a slow fetch falls back to
// neutralization instead of hanging the whole run.
const spccTimeout = 4 * time.Minute

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
// already extracted upstream). It returns a human-readable note describing which path ran and
// spccApplied=true ONLY when photometric SPCC actually calibrated (false for every fallback), so the
// caller can decide whether a channel balance is trustworthy (e.g. linked vs unlinked stretch). Only a
// hard Siril failure of the fallback returns an error, so a calibration miss never aborts the run.
func ColorCalibrate(ctx context.Context, runner *siril.Runner, dir, base string, opts ColorCalOptions) (note string, spccApplied bool, err error) {
	if opts.Enabled {
		sctx, cancel := context.WithTimeout(ctx, spccTimeout)
		res, err := runner.Run(sctx, dir, siril.ColorCalibrateScript(base, base, opts.Solve, opts.Spcc), nil)
		cancel()
		if err == nil && res != nil && !solveFailed(res.Log) {
			return "SPCC color calibration applied", true, nil
		}
	}
	if !opts.RemoveGreen {
		if opts.Enabled {
			return "plate-solve/SPCC unavailable — left color uncalibrated", false, nil
		}
		return "", false, nil
	}
	// Fallback: extract each channel's background (degree-1 plane — equalizes the R/G/B pedestals
	// toward a neutral sky, not just green) then strip the residual green cast. A safety net mainly
	// for the no-GraXpert / offline path; harmless when the background was already flattened upstream.
	if _, err := runner.Run(ctx, dir, siril.NeutralizeScript(base, base, 1, 0), nil); err != nil {
		return "", false, fmt.Errorf("color neutralization: %w", err)
	}
	if opts.Enabled {
		return "plate-solve/SPCC unavailable — used background-neutralization fallback", false, nil
	}
	return "background-neutralization color balance", false, nil
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
