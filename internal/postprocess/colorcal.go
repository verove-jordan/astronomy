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
// branch on it: both trustworthy rungs (SPCC and the warm-anchored star-field gains) establish a
// balance whose RESIDUAL green is safe to strip with a lightness-preserving SCNR, while the GIMP
// green trim stays reserved for genuinely uncalibrated colour (compose.go — stacking it on a
// calibrated field once tipped an M31 pink-galaxy/purple-star).
type CalMethod int

const (
	CalNone        CalMethod = iota // colour left uncalibrated (disabled + no green removal)
	CalSPCC                         // spectrophotometric SPCC (the most precise path)
	CalStarField                    // star-field per-channel gains, warm-anchored (trustworthy fallback)
	CalNeutralized                  // background neutralization + green strip (last resort; green already removed)
	CalPalette                      // narrowband palette mapping — the colour IS the channel assignment, no calibration applies
	CalPCC                          // photometric PCC (Gaia photometry; the rung when SPCC itself fails, e.g. the arm64 1.4.4 segfault)
)

// Calibrated reports whether the method established a TRUSTWORTHY channel balance (SPCC, PCC or
// star-field), worth preserving with a linked stretch and enough to suppress the GIMP green trim.
func (m CalMethod) Calibrated() bool { return m == CalSPCC || m == CalPCC || m == CalStarField }

// Photometric reports whether the balance came from real catalogue photometry (SPCC or PCC) — the
// rungs that need no "colours come from a fallback" caveat.
func (m CalMethod) Photometric() bool { return m == CalSPCC || m == CalPCC }

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
//  1. plate-solve + SPCC — spectrophotometric color (per-star Gaia spectra; the most precise);
//  2. plate-solve + PCC — catalogue photometry without the spectral synthesis. SPCC can fail where
//     PCC succeeds on the same solve (the distro arm64 Siril 1.4.4 segfaults inside SPCC's aperture
//     photometry — local and online catalogues alike — while `pcc` completes), and a photometric
//     balance is far closer to SPCC than any star-derived guess;
//  3. star-field gain calibration (StarField) — per-channel GAINS estimated from the field's own
//     stars against a warm median-star anchor, so results stay natural even fully offline/unsolvable
//     (the neutralization fallback below only equalizes the sky pedestal and leaves a red-strong
//     rig's signal systematically warm);
//  4. SCNR green removal over a degree-1 background neutralization — the last resort.
//
// It returns a human-readable note describing which path ran and the CalMethod that established the
// balance (SPCC/PCC/star-field are trustworthy; see CalMethod.Calibrated). The fallback note names WHY
// EACH photometric rung was unavailable (timeout / siril error / a matched log marker) so run.json
// explains the ladder step. Only a hard Siril failure of the last-resort fallback returns an error, so
// a calibration miss never aborts the run.
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
	pccReason := "not attempted"
	if opts.Enabled {
		// SPCC's script died (crash/timeout/no solution) — PCC re-solves and calibrates from Gaia
		// photometry. The SPCC script saves only AFTER its spcc line, so a mid-script failure left
		// the linear image untouched and this rung starts clean.
		pctx, cancel := context.WithTimeout(ctx, spccTimeout)
		res, perr := runner.Run(pctx, dir, siril.PhotometricCalibrateScript(base, base, opts.Solve), nil)
		cancel()
		if perr == nil && res != nil && !solveFailed(res.Log) {
			return fmt.Sprintf("PCC photometric color calibration applied (SPCC unavailable: %s)", spccReason), CalPCC, nil
		}
		pccReason = rungReason(res, perr)
	}
	// Both photometric rungs are reported from here on. Only SPCC's reason used to be, and because
	// SPCC dies on this platform for its own reasons (an arm64 crash inside its aperture photometry),
	// its "solve error" was read as "the plate solve failed" and hid what PCC — the rung that decides
	// whether the colour is photometric at all — actually hit. They fail for different reasons and
	// need different fixes: a wrong focal length breaks the solve, a missing catalogue breaks PCC.
	ladder := fmt.Sprintf("SPCC %s, PCC %s", spccReason, pccReason)
	if opts.Enabled && opts.StarField {
		fitsPath := filepath.Join(dir, base+".fits")
		if r, serr := StarFieldCalibrate(fitsPath, opts.StarCal); serr == nil && r.Applied {
			return fmt.Sprintf("no photometric calibration (%s) — star-field fallback (gains R=%.2f B=%.2f from %d stars)",
				ladder, r.GainR, r.GainB, r.Stars), CalStarField, nil
		} else if serr != nil {
			// Soft-fail into neutralization; the note keeps the miss visible.
			note = "star-field fallback failed (" + serr.Error() + "); "
		}
	}
	if !opts.RemoveGreen {
		if opts.Enabled {
			return note + fmt.Sprintf("no photometric calibration (%s) — left color uncalibrated", ladder), CalNone, nil
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
		return note + fmt.Sprintf("no photometric calibration (%s) — used background-neutralization fallback", ladder), CalNeutralized, nil
	}
	return "background-neutralization color balance", CalNeutralized, nil
}

// rungReason names why one calibration rung did not land, from the same evidence the SPCC rung is
// judged on: a process error (crash/timeout), a missing result, or a failure marker Siril logged
// while still exiting zero.
func rungReason(res *siril.Result, err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case err != nil:
		return "siril error"
	case res == nil:
		return "no result"
	}
	if m := solveMarker(res.Log); m != "" {
		return m
	}
	return "no solution"
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
