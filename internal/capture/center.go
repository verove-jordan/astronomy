package capture

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/device"
	"github.com/verove-jordan/astronomy/internal/platesolve"
)

// Plate-solve centring: put the target where the plan says it should be, by measuring rather than
// trusting.
//
// A GoTo lands where the mount's model thinks the target is, which on any real mount is an
// arcminute or two out — fine for visual use, not fine for a mosaic where every tile must abut its
// neighbours. So: take a short frame, solve it, compare where the telescope REALLY points against
// where it should, tell the mount the truth (sync) and slew again. Two iterations usually gets
// within a few arcseconds.
//
// This lives in the engine rather than the device server because it needs Siril, which the engine
// owns; the device server contributes the exposure and the mount commands.

// Solver is the plate-solving dependency, narrowed so tests can substitute one.
type Solver interface {
	Solve(ctx context.Context, path string, hint platesolve.Hint) (platesolve.Result, error)
}

// CenterOptions tune one centring run.
type CenterOptions struct {
	ExposureUs      int64 // short is fine: centring needs stars, not depth
	Bin             int   // binning speeds up both the download and the solve
	Gain            int64
	ToleranceArcsec float64 // stop once the error is below this
	MaxIterations   int
	FocalMM         float64
	PixelUm         float64
	// ScratchDir is where the temporary frames go; empty → the OS temp dir. They are deleted after.
	ScratchDir string
}

func (o CenterOptions) withDefaults() CenterOptions {
	out := o
	if out.ExposureUs <= 0 {
		out.ExposureUs = 5_000_000 // 5 s: enough stars to solve on a 100 mm scope
	}
	if out.Bin <= 0 {
		out.Bin = 2 // half the pixels, a quarter of the solve time, plenty of stars
	}
	if out.ToleranceArcsec <= 0 {
		out.ToleranceArcsec = 30
	}
	if out.MaxIterations <= 0 {
		out.MaxIterations = 3
	}
	return out
}

// CenterAttempt records one pass, so the UI can show the convergence rather than a spinner.
type CenterAttempt struct {
	Iteration    int     `json:"iteration"`
	SolvedRADeg  float64 `json:"solved_ra_deg"`
	SolvedDecDeg float64 `json:"solved_dec_deg"`
	ErrorArcsec  float64 `json:"error_arcsec"`
	Synced       bool    `json:"synced"`
	Message      string  `json:"message,omitempty"`
}

// CenterResult is the outcome of a centring run.
type CenterResult struct {
	Centered      bool            `json:"centered"`
	FinalArcsec   float64         `json:"final_arcsec"`
	Attempts      []CenterAttempt `json:"attempts"`
	ScaleArcsecPx float64         `json:"scale_arcsec_px,omitempty"`
}

// Center steers the telescope onto a J2000 target.
func (r *Runner) Center(ctx context.Context, solver Solver, raDeg, decDeg float64, opts CenterOptions) (CenterResult, error) {
	o := opts.withDefaults()
	if solver == nil {
		return CenterResult{}, fmt.Errorf("plate solving is unavailable — Siril is not configured")
	}
	scratch, err := os.MkdirTemp(o.ScratchDir, "astro-center-*")
	if err != nil {
		return CenterResult{}, err
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	var res CenterResult
	for i := 1; i <= o.MaxIterations; i++ {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		attempt := CenterAttempt{Iteration: i}

		framePath := filepath.Join(scratch, fmt.Sprintf("center_%02d.fit", i))
		if err := r.exposeForCentering(ctx, framePath, o); err != nil {
			return res, err
		}

		// The mount's own idea of where it points is the solve hint — even an arcminute off, it turns
		// a blind all-sky search into a couple of seconds.
		hint := platesolve.Hint{RADeg: raDeg, DecDeg: decDeg, HasHint: true,
			FocalMM: o.FocalMM, PixelUm: o.PixelUm}
		solved, err := solver.Solve(ctx, framePath, hint)
		if err != nil {
			attempt.Message = err.Error()
			res.Attempts = append(res.Attempts, attempt)
			return res, fmt.Errorf("centring stopped: %w", err)
		}
		attempt.SolvedRADeg, attempt.SolvedDecDeg = solved.RADeg, solved.DecDeg
		attempt.ErrorArcsec = astro.AngularSeparation(solved.RADeg, solved.DecDeg, raDeg, decDeg) * 3600
		res.ScaleArcsecPx = solved.ScaleArcsecPx

		if attempt.ErrorArcsec <= o.ToleranceArcsec {
			attempt.Message = "within tolerance"
			res.Attempts = append(res.Attempts, attempt)
			res.Centered = true
			res.FinalArcsec = attempt.ErrorArcsec
			return res, nil
		}

		// Tell the mount where it REALLY is, then ask again for the target. Sync-then-goto beats
		// nudging by the error: it also corrects the model, so the next tile starts closer.
		if err := r.client.Sync(ctx, solved.RADeg, solved.DecDeg); err != nil {
			attempt.Message = "sync failed: " + err.Error()
			res.Attempts = append(res.Attempts, attempt)
			return res, err
		}
		attempt.Synced = true
		if err := r.client.Goto(ctx, raDeg, decDeg); err != nil {
			attempt.Message = "goto failed: " + err.Error()
			res.Attempts = append(res.Attempts, attempt)
			return res, err
		}
		res.Attempts = append(res.Attempts, attempt)
		res.FinalArcsec = attempt.ErrorArcsec

		if err := r.waitForSlew(ctx); err != nil {
			return res, err
		}
	}
	return res, nil
}

// exposeForCentering takes one short frame and writes it where the solver can read it.
func (r *Runner) exposeForCentering(ctx context.Context, path string, o CenterOptions) error {
	if err := r.client.SetControl(ctx, device.ControlExposure, o.ExposureUs); err != nil {
		return fmt.Errorf("set exposure: %w", err)
	}
	if o.Gain > 0 {
		if err := r.client.SetControl(ctx, device.ControlGain, o.Gain); err != nil {
			return fmt.Errorf("set gain: %w", err)
		}
	}
	if err := r.client.StartExposure(ctx, false); err != nil {
		return fmt.Errorf("start exposure: %w", err)
	}
	if err := r.waitForExposure(ctx, Step{ExposureUs: o.ExposureUs}); err != nil {
		return err
	}
	_, err := r.client.Save(ctx, SaveRequest{Path: path, Type: "light", Object: "centering"})
	return err
}

// waitForSlew blocks until the mount stops moving, so the next frame is not taken mid-slew.
func (r *Runner) waitForSlew(ctx context.Context) error {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		st, err := r.client.Mount(ctx)
		if err != nil {
			return err
		}
		if !st.Mount.Slewing {
			// A brief settle: a mount that has just stopped is still ringing.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the mount was still slewing after two minutes")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
