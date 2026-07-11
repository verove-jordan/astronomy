package postprocess

import (
	"context"
	"errors"
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

// CalMethod is which rung of the colour-calibration ladder actually produced the balance — callers
// branch on it (e.g. an SCNR green removal is right after SPCC but tips a star-field-calibrated field
// magenta, since the star-field gains already make the median star neutral by construction).
type CalMethod int

const (
	CalNone        CalMethod = iota // colour left uncalibrated (disabled + no green removal)
	CalSPCC                         // photometric SPCC (the trustworthy, green-residual-leaving path)
	CalStarField                    // star-field per-channel gains (neutral by construction — no SCNR)
	CalNeutralized                  // background neutralization + green strip (last resort; green already removed)
)

// Calibrated reports whether the method established a TRUSTWORTHY channel balance (SPCC or star-field),
// worth preserving with a linked stretch and enough to suppress the GIMP green trim.
func (m CalMethod) Calibrated() bool { return m == CalSPCC || m == CalStarField }

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
// It returns a human-readable note describing which path ran and the CalMethod that established the
// balance (SPCC/star-field are trustworthy; see CalMethod.Calibrated). The fallback note now names WHY
// SPCC was unavailable (timeout / solve error / a matched log marker) so run.json explains the ladder
// step. Only a hard Siril failure of the last-resort fallback returns an error, so a calibration miss
// never aborts the run.
func ColorCalibrate(ctx context.Context, runner *siril.Runner, dir, base string, opts ColorCalOptions) (note string, method CalMethod, err error) {
	spccReason := "unavailable"
	if opts.Enabled {
		sctx, cancel := context.WithTimeout(ctx, spccTimeout)
		res, rerr := runner.Run(sctx, dir, siril.ColorCalibrateScript(base, base, opts.Solve, opts.Spcc), nil)
		cancel()
		if rerr == nil && res != nil && !solveFailed(res.Log) {
			return "SPCC color calibration applied", CalSPCC, nil
		}
		switch {
		case errors.Is(rerr, context.DeadlineExceeded):
			spccReason = "timeout"
		case rerr != nil:
			spccReason = "solve error"
		case res == nil:
			spccReason = "no result"
		default:
			if m := solveMarker(res.Log); m != "" {
				spccReason = m
			} else {
				spccReason = "no solution"
			}
		}
	}
	if opts.Enabled && opts.StarField {
		fitsPath := filepath.Join(dir, base+".fits")
		if r, serr := StarFieldCalibrate(fitsPath, opts.StarCal); serr == nil && r.Applied {
			return fmt.Sprintf("SPCC unavailable (%s) — star-field photometric fallback (gains R=%.2f B=%.2f from %d stars)",
				spccReason, r.GainR, r.GainB, r.Stars), CalStarField, nil
		} else if serr != nil {
			// Soft-fail into neutralization; the note keeps the miss visible.
			note = "star-field fallback failed (" + serr.Error() + "); "
		}
	}
	if !opts.RemoveGreen {
		if opts.Enabled {
			return note + fmt.Sprintf("plate-solve/SPCC unavailable (%s) — left color uncalibrated", spccReason), CalNone, nil
		}
		return "", CalNone, nil
	}
	// Fallback: extract each channel's background (degree-1 plane — equalizes the R/G/B pedestals
	// toward a neutral sky, not just green) then strip the residual green cast. A safety net mainly
	// for the no-GraXpert / offline path; harmless when the background was already flattened upstream.
	if _, nerr := runner.Run(ctx, dir, siril.NeutralizeScript(base, base, 1, 0), nil); nerr != nil {
		return "", CalNone, fmt.Errorf("color neutralization: %w", nerr)
	}
	if opts.Enabled {
		return note + fmt.Sprintf("plate-solve/SPCC unavailable (%s) — used background-neutralization fallback", spccReason), CalNeutralized, nil
	}
	return "background-neutralization color balance", CalNeutralized, nil
}

// solveMarker returns the first plate-solve/SPCC failure marker found in the Siril log (in case Siril
// logs the error but still exits zero), or "" when none — conservative: unknown output is success.
func solveMarker(log string) string {
	l := strings.ToLower(log)
	for _, marker := range []string{
		"plate solving failed", "failed to solve", "no astrometric solution",
		"could not solve", "not been plate", "spcc failed", "no stars detected",
	} {
		if strings.Contains(l, marker) {
			return marker
		}
	}
	return ""
}

// solveFailed reports whether the Siril log contains any plate-solve/SPCC failure marker.
func solveFailed(log string) bool { return solveMarker(log) != "" }
