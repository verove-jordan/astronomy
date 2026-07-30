package devsrv

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
	"github.com/verove-jordan/astronomy/internal/guidestar"
	"github.com/verove-jordan/astronomy/internal/pec"
)

// Watching the worm go round.
//
// Each sample needs three things: where the star is, when, and where the worm was. The last of those
// comes from the mount's own bin counter rather than from a clock, read either side of the exposure
// so the sample lands at the phase the shutter was actually open across. Taking phase from the mount
// has a property no clock can offer — if the counter the mount reports is offset from the one its
// playback uses, that offset appears identically in the measurement and in the write, and cancels.

// maxLostFrames is how many consecutive frames may fail to find the star before the run gives up.
// Cloud passes, aeroplanes cross, and a dozen seconds of nothing is not a reason to lose an hour's
// work — but a star that has not come back in a couple of minutes is not coming back.
const maxLostFrames = 90

// measure samples the star for the requested number of worm revolutions.
func (p *pecSession) measure(
	ctx context.Context,
	req PECTrainRequest,
	mount device.PECMount,
	geom pec.Geometry,
	cal PECCalibration,
	star guidestar.Star,
) error {
	p.setPhase(PECMeasuring, "watching the worm for %.1f revolutions", req.Cycles)

	total := req.Cycles * geom.WormPeriodSec
	start := nowFunc()
	ref := star
	last := star
	lost := 0

	for {
		if ctx.Err() != nil {
			// A cancelled run keeps what it measured: an hour of samples is worth reporting on even
			// if the user stopped it early.
			return ctx.Err()
		}
		elapsed := nowFunc().Sub(start).Seconds()
		if elapsed >= total {
			return nil
		}

		sample, next, mid, err := p.sampleOnce(ctx, req, mount, geom, cal, ref, last)
		switch {
		case err == nil:
			lost = 0
			last = next
			p.appendSample(sample, next, mid.Sub(start).Seconds(), total, geom)
		case errors.Is(err, guidestar.ErrNoStar):
			// Keep the phase clock running and keep looking where the star was. Never move the mount
			// to re-acquire: an unmodelled nudge in the middle of a run is fitted as though the worm
			// had done it.
			lost++
			p.noteLost(lost)
			if lost >= maxLostFrames {
				return fmt.Errorf("the guide star has been lost for %d frames — cloud, or it has drifted off the sensor", lost)
			}
		default:
			return err
		}
		p.refreshMountCache(ctx)
	}
}

// sampleOnce takes one exposure and turns it into a sample.
func (p *pecSession) sampleOnce(
	ctx context.Context,
	req PECTrainRequest,
	mount device.PECMount,
	geom pec.Geometry,
	cal PECCalibration,
	ref, last guidestar.Star,
) (pec.Sample, guidestar.Star, time.Time, error) {
	// Straddle the exposure with the two phase reads, so the sample lands at the phase the shutter
	// was actually open across rather than at whichever end happened to be measured.
	binBefore, err := mount.PECBin(ctx)
	if err != nil {
		return pec.Sample{}, guidestar.Star{}, time.Time{}, err
	}
	openedAt := nowFunc()

	next, err := p.grabStar(ctx, req, &last)
	if err != nil {
		return pec.Sample{}, guidestar.Star{}, time.Time{}, err
	}
	closedAt := nowFunc()

	binAfter, err := mount.PECBin(ctx)
	if err != nil {
		return pec.Sample{}, guidestar.Star{}, time.Time{}, err
	}

	sample := pec.Sample{
		PhaseBins: midBin(binBefore, binAfter, geom.Bins),
		Arcsec:    cal.AxisArcsec(next.X-ref.X, next.Y-ref.Y),
	}
	mid := openedAt.Add(closedAt.Sub(openedAt) / 2)
	return sample, next, mid, nil
}

// midBin is the worm phase the exposure was centred on.
//
// The two readings straddle the exposure, so their midpoint is where the shutter was open across. The
// wrap has to be handled explicitly: an exposure spanning the index sees 87 then 0, whose arithmetic
// mean is 43 — the far side of the worm from where the mount actually was.
func midBin(before, after, bins int) float64 {
	span := after - before
	if span < 0 {
		span += bins
	}
	if span > bins/2 {
		// More than half a revolution between two reads means something stalled, not that the worm
		// raced round; trust the first reading rather than inventing a midpoint.
		return float64(before)
	}
	mid := float64(before) + float64(span)/2
	if mid >= float64(bins) {
		mid -= float64(bins)
	}
	return mid
}

func (p *pecSession) appendSample(s pec.Sample, star guidestar.Star, elapsed, total float64, geom pec.Geometry) {
	s.TimeSec = elapsed
	p.mu.Lock()
	p.samples = append(p.samples, s)
	p.state.Samples = len(p.samples)
	p.state.Cycles = elapsed / geom.WormPeriodSec
	p.state.Progress = math.Min(1, elapsed/total)
	p.state.StarSNR, p.state.StarHFD = star.SNR, star.HFD
	p.mu.Unlock()
	p.notify()
}

func (p *pecSession) noteLost(lost int) {
	p.mu.Lock()
	p.state.Lost = lost
	p.mu.Unlock()
	p.notify()
}

// mountCacheEvery is how often the cached mount state is refreshed while a run owns the serial port.
const mountCacheEvery = 5 * time.Second

// refreshMountCache keeps the ordinary status endpoint answerable without letting it near the port.
func (p *pecSession) refreshMountCache(ctx context.Context) {
	p.mu.RLock()
	fresh := nowFunc().Sub(p.mountCachedAt) < mountCacheEvery
	p.mu.RUnlock()
	if fresh {
		return
	}
	mount := p.srv.currentMount()
	if mount == nil {
		return
	}
	st, err := mount.State(ctx)
	if err != nil {
		return
	}
	p.mu.Lock()
	p.mountCache, p.mountCachedAt = &st, nowFunc()
	p.mu.Unlock()
}

// PECReport is what a run concluded.
type PECReport struct {
	Bins    int       `json:"bins"`
	Curve   []float64 `json:"curve"`   // fitted axis error at each bin edge
	Folded  []float64 `json:"folded"`  // measured mean per bin, for the chart
	Scatter []float64 `json:"scatter"` // spread within each bin

	PeakToPeakArcsec  float64 `json:"peak_to_peak_arcsec"`
	DriftArcsecPerMin float64 `json:"drift_arcsec_per_min"`
	ResidualRMSArcsec float64 `json:"residual_rms_arcsec"`
	MaxUnguidedSec    float64 `json:"max_unguided_sec"`

	Coherent      float64 `json:"coherent"`
	CoherentFloor float64 `json:"coherent_floor"`
	Cycles        float64 `json:"cycles"`
	Correctable   bool    `json:"correctable"`

	// Predicted is what the mount should manage once the curve is playing, computed the same way as
	// MaxUnguidedSec so the comparison means something.
	PredictedMaxUnguidedSec float64 `json:"predicted_max_unguided_sec"`

	Warnings []string `json:"warnings,omitempty"`
}

// analyse turns the samples into a report, and decides whether writing is defensible.
func (p *pecSession) analyse(geom pec.Geometry, req PECTrainRequest) (*PECReport, error) {
	p.mu.RLock()
	samples := append([]pec.Sample(nil), p.samples...)
	p.mu.RUnlock()

	fit, err := pec.FitCurve(samples, geom)
	if err != nil {
		return nil, err
	}
	folded := pec.Fold(samples, geom)
	rep := pec.MeasureRepeatability(samples, geom)
	budget := pec.BudgetArcsec(p.srv.arcsecPerPixel())

	out := &PECReport{
		Bins:              geom.Bins,
		Curve:             fit.Curve,
		PeakToPeakArcsec:  pec.PeakToPeak(fit.Curve),
		DriftArcsecPerMin: fit.DriftArcsecPerSec * 60,
		ResidualRMSArcsec: fit.ResidualRMSArcsec,
		MaxUnguidedSec:    pec.MaxUnguidedSec(fit.Curve, geom, fit.DriftArcsecPerSec, budget),
		Coherent:          rep.Coherent,
		CoherentFloor:     pec.MinCoherent,
		Cycles:            rep.Cycles,
		Correctable:       rep.Coherent >= pec.MinCoherent,
	}
	if folded != nil {
		out.Folded, out.Scatter = folded.Mean, folded.Scatter
	}
	out.PredictedMaxUnguidedSec = predictedMaxUnguided(fit, geom, rep, budget)
	out.Warnings = warningsFor(out, rep, folded, geom)
	return out, nil
}

// predictedMaxUnguided estimates what will be left once the curve plays.
//
// What survives is the part that did not repeat — PEC replays one curve every revolution, so anything
// that differs between revolutions passes straight through. Modelling that as a residual of the
// measured curve scaled by the incoherent fraction is rough, and it is deliberately pessimistic
// rather than optimistic: promising more than the mount delivers is the failure mode that matters.
func predictedMaxUnguided(fit *pec.Fit, geom pec.Geometry, rep pec.Repeatability, budget float64) float64 {
	residual := make([]float64, len(fit.Curve))
	leftover := math.Sqrt(math.Max(0, 1-rep.Coherent))
	for i, v := range fit.Curve {
		residual[i] = v * leftover
	}
	return pec.MaxUnguidedSec(residual, geom, fit.DriftArcsecPerSec, budget)
}

func warningsFor(out *PECReport, rep pec.Repeatability, folded *pec.Folded, geom pec.Geometry) []string {
	var w []string
	if rep.Cycles < 3 {
		w = append(w, fmt.Sprintf(
			"only %.1f worm revolutions were watched — three is the least that can show whether the error repeats",
			rep.Cycles))
	}
	if !out.Correctable {
		w = append(w, fmt.Sprintf(
			"only %.0f%% of the error repeats from one revolution to the next, so a stored curve would replay "+
				"noise rather than correct anything", rep.Coherent*100))
	}
	if folded != nil && folded.Empty > 0 {
		w = append(w, fmt.Sprintf("%d of %d worm bins were never sampled and have been interpolated",
			folded.Empty, geom.Bins))
	}
	w = append(w, fmt.Sprintf(
		"correction cannot touch anything faster than %.0f s — that is two bins, the limit of an %d-bin table",
		geom.NyquistPeriodSec(), geom.Bins))
	return w
}

// arcsecPerPixel is the image scale the trailing budget is judged against.
func (s *Server) arcsecPerPixel() float64 {
	if s.cfg == nil || s.cfg.FocalLenMM <= 0 || s.cfg.PixelSizeUm <= 0 {
		return 1
	}
	return 206.264806 * s.cfg.PixelSizeUm / s.cfg.FocalLenMM
}
