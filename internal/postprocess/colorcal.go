package postprocess

import (
	"context"
	"fmt"
	"path/filepath"
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
	StarField   bool // enable the star-field gain fallback between SPCC and neutralization
	StarCal     StarCalOptions
	Solve       siril.SolveOptions
	Spcc        siril.SpccOptions
}

// ColorCalibrate calibrates the named linear image in dir, overwriting it. The ladder:
//
//  1. plate-solve + SPCC — true photometric color (natural star/nebulosity hues);
//  2. star-field gain calibration (StarField) — per-channel GAINS estimated from the field's own
//     stars, so results stay neutral-natural even fully offline (the neutralization fallback below
//     only equalizes the sky pedestal and leaves a red-strong rig's signal systematically warm);
//  3. SCNR green removal over a degree-1 background neutralization — the last resort.
//
// It returns a human-readable note describing which path ran and calibrated=true when a TRUSTWORTHY
// channel balance was established (SPCC or star-field — both are worth preserving with a linked
// stretch); false for the neutralization fallback. Only a hard Siril failure of the last-resort
// fallback returns an error, so a calibration miss never aborts the run.
func ColorCalibrate(ctx context.Context, runner *siril.Runner, dir, base string, opts ColorCalOptions) (note string, calibrated bool, err error) {
	if opts.Enabled {
		sctx, cancel := context.WithTimeout(ctx, spccTimeout)
		res, err := runner.Run(sctx, dir, siril.ColorCalibrateScript(base, base, opts.Solve, opts.Spcc), nil)
		cancel()
		if err == nil && res != nil && !solveFailed(res.Log) {
			return "SPCC color calibration applied", true, nil
		}
	}
	if opts.Enabled && opts.StarField {
		fitsPath := filepath.Join(dir, base+".fits")
		if r, serr := StarFieldCalibrate(fitsPath, opts.StarCal); serr == nil && r.Applied {
			return fmt.Sprintf("SPCC unavailable — star-field photometric fallback (gains R=%.2f B=%.2f from %d stars)",
				r.GainR, r.GainB, r.Stars), true, nil
		} else if serr != nil {
			// Soft-fail into neutralization; the note keeps the miss visible.
			note = "star-field fallback failed (" + serr.Error() + "); "
		}
	}
	if !opts.RemoveGreen {
		if opts.Enabled {
			return note + "plate-solve/SPCC unavailable — left color uncalibrated", false, nil
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
		return note + "plate-solve/SPCC unavailable — used background-neutralization fallback", false, nil
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
