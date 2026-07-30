package capture

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"
)

// driftFit is a straight line through the mount's uncorrected trajectory.
type driftFit struct {
	RARatePerSec   float64
	DecRatePerSec  float64
	ResidualArcsec float64
	Samples        int
}

// fitLocked regresses the uncorrected trajectory against time. Caller holds g.mu.
//
// The trajectory is the measured error with every correction added back, so the slope is the rate the
// mount would have drifted at had nothing been done — which is the thing worth predicting. Fitting the
// residual error instead would describe the servo, not the mount, and would head for zero exactly as
// the guider started working.
func (g *Guider) fitLocked() (driftFit, bool) {
	n := len(g.history)
	if n < g.opts.MinFitSamples {
		return driftFit{Samples: n}, false
	}
	var sumT, sumTT float64
	for _, s := range g.history {
		sumT += s.tSec
		sumTT += s.tSec * s.tSec
	}
	denom := float64(n)*sumTT - sumT*sumT
	if denom == 0 {
		return driftFit{Samples: n}, false
	}

	raSlope, raIntercept := slopeOf(g.history, sumT, sumTT, denom, func(s driftSample) float64 { return s.raArcsec })
	decSlope, decIntercept := slopeOf(g.history, sumT, sumTT, denom, func(s driftSample) float64 { return s.decArcsec })

	var sq float64
	for _, s := range g.history {
		dRA := s.raArcsec - (raIntercept + raSlope*s.tSec)
		dDec := s.decArcsec - (decIntercept + decSlope*s.tSec)
		sq += dRA*dRA + dDec*dDec
	}
	return driftFit{
		RARatePerSec:   raSlope,
		DecRatePerSec:  decSlope,
		ResidualArcsec: math.Sqrt(sq / float64(n)),
		Samples:        n,
	}, true
}

func slopeOf(h []driftSample, sumT, sumTT, denom float64, value func(driftSample) float64) (slope, intercept float64) {
	var sumY, sumTY float64
	for _, s := range h {
		v := value(s)
		sumY += v
		sumTY += s.tSec * v
	}
	n := float64(len(h))
	slope = (n*sumTY - sumT*sumY) / denom
	intercept = (sumY*sumTT - sumT*sumTY) / denom
	return slope, intercept
}

// Compensate spreads a train of small pulses across the next exposure to cancel the drift predicted
// for it, and returns a function that stops the train.
//
// This is the half that makes stars rounder. A correction applied between frames cannot: trailing
// depends on how far the mount moves DURING an exposure, and that is unchanged by where the exposure
// started. Cancelling the drift as it happens is the only way to shorten the trail, short of a guide
// camera fast enough to close a real loop.
//
// It is heavily gated, because a confident wrong prediction adds precisely as much trailing as a right
// one removes: enough samples to fit, a prediction big enough to be worth acting on, a residual small
// enough that the line is credible, and a hard ceiling on the total.
func (g *Guider) Compensate(ctx context.Context, exposure time.Duration) func() {
	noop := func() {}
	if g == nil || !g.opts.Compensate || exposure <= 0 {
		return noop
	}

	g.mu.Lock()
	fit, ok := g.fitLocked()
	g.mu.Unlock()
	if !ok {
		return noop
	}

	seconds := exposure.Seconds()
	dRA, dDec := fit.RARatePerSec*seconds, fit.DecRatePerSec*seconds
	total := math.Hypot(dRA, dDec)
	if total < guiderMinTrainArcsec {
		return noop
	}
	if fit.ResidualArcsec > guiderFitConfidence*total {
		// The mount is moving, but not steadily enough for a line to predict it. Feedback between frames
		// is the honest answer here; guessing would be worse than not guessing.
		return noop
	}
	if total > g.opts.MaxTrainArcsec {
		scale := g.opts.MaxTrainArcsec / total
		dRA, dDec, total = dRA*scale, dDec*scale, g.opts.MaxTrainArcsec
	}

	pulses := int(math.Ceil(total / guiderTrainStepArcsec))
	if pulses < 1 {
		pulses = 1
	}
	if pulses > guiderMaxTrainPulses {
		pulses = guiderMaxTrainPulses
	}

	trainCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		g.runTrain(trainCtx, -dRA/float64(pulses), -dDec/float64(pulses), exposure/time.Duration(pulses), pulses)
	}()

	g.mu.Lock()
	g.compensated++
	g.mu.Unlock()

	return func() {
		cancel()
		wg.Wait()
	}
}

// runTrain issues the compensating pulses, one per slice of the exposure.
func (g *Guider) runTrain(ctx context.Context, stepRA, stepDec float64, interval time.Duration, pulses int) {
	for i := 0; i < pulses; i++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
		if err := g.client.GuidePulse(ctx, stepRA, stepDec, g.opts.RateArcsecPerSec); err != nil {
			if ctx.Err() == nil {
				g.note(fmt.Errorf("compensation pulse: %w", err))
			}
			return
		}
		// Counted as it happens, not up front: an exposure aborted half way through has had half the
		// compensation applied, and the next fit has to know that.
		g.mu.Lock()
		g.cumRA += stepRA
		g.cumDec += stepDec
		g.mu.Unlock()
	}
}
