package devsrv

import (
	"context"
	"fmt"
	"math"

	"github.com/verove-jordan/astronomy/internal/device"
	"github.com/verove-jordan/astronomy/internal/guidestar"
	"github.com/verove-jordan/astronomy/internal/pec"
)

// Writing the curve, and proving it helped.
//
// This is the only thing the application does that changes state inside a piece of hardware and
// outlives the session, so it is deliberately the most cautious code here. Three rules:
//
//  1. Back up first. The curve already in the mount may be the only copy of an hour somebody spent
//     with a hand controller, and it is not stored anywhere else in the world.
//  2. Never write a curve that the measurement said would not help. The repeatability gate is checked
//     again here rather than trusted from upstream.
//  3. Measure afterwards, and put the old curve back if it got worse. A correction with an inverted
//     sign does not fail — it tracks twice as badly, quietly, all night.

// verifyCycles is how many worm revolutions the after-measurement watches. Fewer than the training
// run, because it is answering a much easier question — did the big periodic term shrink — and
// because it is asking it at the end of a long night.
const verifyCycles = 2

// writeAndVerify computes the table, writes it, and checks the mount really is better for it.
func (p *pecSession) writeAndVerify(
	ctx context.Context,
	req PECTrainRequest,
	mount device.PECMount,
	geom pec.Geometry,
	cal PECCalibration,
	star guidestar.Star,
	report *PECReport,
) error {
	if !report.Correctable {
		return fmt.Errorf(
			"refusing to write: only %.0f%% of this mount's error repeats from one worm revolution to the "+
				"next (at least %.0f%% is needed), so a stored curve would replay noise rather than correct anything",
			report.Coherent*100, pec.MinCoherent*100)
	}

	p.setPhase(PECWriting, "backing up the curve already in the mount")
	backup, err := mount.PECReadCurve(ctx)
	if err != nil {
		return fmt.Errorf("backing up the existing curve: %w", err)
	}
	p.mu.Lock()
	p.state.Backup = intsFrom(backup)
	p.mu.Unlock()

	table, err := p.computeTable(geom, report)
	if err != nil {
		return err
	}

	p.setPhase(PECWriting, "writing %d bins", len(table.Bins))
	if err := mount.PECWriteCurve(ctx, table.Bins); err != nil {
		return fmt.Errorf("writing the curve: %w", err)
	}
	if err := mount.PECPlayback(ctx, true); err != nil {
		return fmt.Errorf("starting playback: %w", err)
	}
	p.mu.Lock()
	p.state.Written = intsFrom(table.Bins)
	p.mu.Unlock()

	return p.verify(ctx, req, mount, geom, cal, star, report, backup)
}

// computeTable turns the fitted curve into bins the mount can hold.
func (p *pecSession) computeTable(geom pec.Geometry, report *PECReport) (*pec.Quantised, error) {
	fit := &pec.Fit{Curve: report.Curve}
	// The index offset is zero until the probe measures it. Folding on the mount's own bin counter
	// means a constant offset between the counter and playback already cancels; what a probe would
	// add is detecting a firmware that numbers them differently, which no AVX is known to do.
	rates := pec.Correction(fit, geom, 0)
	if rates == nil {
		return nil, fmt.Errorf("the fitted curve does not match the mount's %d bins", geom.Bins)
	}
	table := pec.Quantise(rates, geom)
	if table == nil {
		return nil, fmt.Errorf("the correction could not be expressed in the mount's table")
	}
	if table.Clipped > 0 {
		// A 15″ worm needs about seven of the 127 available units, so clipping does not mean an
		// unusually bad mount — it means the fit produced something implausible.
		return nil, fmt.Errorf(
			"refusing to write: %d bins exceed what the table can hold, which means the measurement is wrong "+
				"rather than the mount being bad", table.Clipped)
	}
	return table, nil
}

// verify re-measures with the curve playing and reverts if the mount got worse.
func (p *pecSession) verify(
	ctx context.Context,
	req PECTrainRequest,
	mount device.PECMount,
	geom pec.Geometry,
	cal PECCalibration,
	star guidestar.Star,
	before *PECReport,
	backup []int8,
) error {
	p.setPhase(PECVerifying, "checking the mount really is better for it")

	p.mu.Lock()
	measured := p.samples
	p.samples = nil
	p.mu.Unlock()

	check := req
	check.Cycles = math.Min(verifyCycles, req.Cycles)
	if err := p.measure(ctx, check, mount, geom, cal, star); err != nil {
		// A verification that could not finish is not evidence the curve is bad, but it is not
		// evidence it is good either. Leave it playing and say so.
		p.mu.Lock()
		p.samples = measured
		p.mu.Unlock()
		p.setPhase(PECDone, "the curve is written and playing, but the check could not finish: %v", err)
		return nil
	}

	after, err := p.analyse(geom, check)
	if err != nil {
		p.setPhase(PECDone, "the curve is written and playing, but the check could not be read: %v", err)
		return nil
	}

	budget := pec.BudgetArcsec(p.srv.arcsecPerPixel())
	improvement := pec.Compare(before.Curve, after.Curve, geom, 0, 0, budget)
	p.mu.Lock()
	p.state.Verify = after
	p.state.Improvement = &improvement
	p.mu.Unlock()

	if improvement.Worsened() {
		return p.revert(ctx, mount, backup, improvement)
	}
	p.setPhase(PECDone,
		"periodic error %.1f″ → %.1f″ peak-to-peak; longest unguided sub %.0f s → %.0f s",
		improvement.BeforePPArcsec, improvement.AfterPPArcsec,
		improvement.BeforeMaxUnguided, improvement.AfterMaxUnguided)
	return nil
}

// revert puts the mount back exactly as it was found.
func (p *pecSession) revert(ctx context.Context, mount device.PECMount, backup []int8, imp pec.Improvement) error {
	p.setPhase(PECWriting, "the mount tracks worse with this curve — putting the old one back")
	if err := mount.PECPlayback(ctx, false); err != nil {
		return fmt.Errorf("the curve made tracking worse and playback could not be stopped: %w", err)
	}
	if err := mount.PECWriteCurve(ctx, backup); err != nil {
		return fmt.Errorf("the curve made tracking worse and the backup could not be restored: %w", err)
	}
	p.mu.Lock()
	p.state.Reverted = true
	p.mu.Unlock()

	// Doubling, rather than merely failing to help, is the signature of an inverted sign — the
	// correction is being applied the wrong way round, so it adds the error instead of removing it.
	hint := ""
	if imp.AfterPPArcsec > imp.BeforePPArcsec*1.7 {
		hint = " The error roughly doubled, which is what an inverted correction looks like."
	}
	p.setPhase(PECDone,
		"periodic error went from %.1f″ to %.1f″ peak-to-peak, so the original curve has been restored.%s",
		imp.BeforePPArcsec, imp.AfterPPArcsec, hint)
	return nil
}

func intsFrom(bins []int8) []int {
	out := make([]int, len(bins))
	for i, v := range bins {
		out[i] = int(v)
	}
	return out
}
