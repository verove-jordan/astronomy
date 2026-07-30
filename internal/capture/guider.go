// Self-guiding: correcting the mount from the subs the session is already taking, with no guide scope
// and no second camera.
//
// # Why this needs two halves
//
// Measuring the error at the end of one sub and cancelling it before the next keeps the field from
// walking — every frame lands on the same pixels, so nothing is lost to coverage at stacking time. It
// does NOT make the stars rounder. Trailing within an exposure depends on how fast the mount drifts
// DURING it, not on where the exposure started, so a correction applied between frames cannot touch it.
//
// So there is a second half. Once enough samples have accumulated to fit the drift rate confidently,
// the guider spreads a small train of pulses across the next exposure that cancels the drift as it
// happens. That is the part that sharpens stars, and it is gated behind the fit because a confident
// wrong prediction adds exactly as much trailing as it would have removed.
//
// # What it predicts, and what it does not
//
// The fit is a straight line: polar-misalignment drift and any rate error. It deliberately does not
// try to predict the worm. At one sample per sub the 478-second periodic error is aliased almost to
// nothing — four samples per cycle at a two-minute cadence — and a periodic term fitted to that would
// be noise dressed as signal. The worm belongs to PEC training, which measures it properly at one
// sample a second, or to a real guide camera.
//
// # Cost
//
// No files are written and none are kept: the guider reads the sub that was just saved, measures one
// star in it, and lets it go. The only lasting state is a few dozen bytes of history per frame.
package capture

import (
	"context"
	"fmt"

	"strings"
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/guide"
	"github.com/verove-jordan/astronomy/internal/guidestar"
)

// Guider defaults.
const (
	// guiderMinFitSamples is how many measurements are needed before predictive compensation engages.
	// Five is enough for a straight line to mean something without making the operator wait half an
	// hour at a three-minute cadence.
	guiderMinFitSamples = 5
	// guiderMaxTrainArcsec caps what one exposure's compensation may add up to. A prediction larger
	// than this is not a drifting mount, it is a bad fit.
	guiderMaxTrainArcsec = 30.0
	// guiderTrainStepArcsec is how finely the compensation is chopped up. Small enough to be smooth at
	// the scale trailing is judged on, large enough that a night is not spent on serial traffic.
	guiderTrainStepArcsec = 0.5
	// guiderMaxTrainPulses bounds the pulse train regardless of size, so a long sub cannot saturate the
	// 9600-baud link that the exposure polling also shares.
	guiderMaxTrainPulses = 60
	// guiderMinTrainArcsec is the predicted drift below which compensation is not worth its own noise.
	guiderMinTrainArcsec = 0.5
	// guiderFitConfidence is the largest residual, as a fraction of the predicted move, that still
	// counts as a trustworthy line. Above it the mount is not drifting steadily and feedback alone is
	// the honest answer.
	guiderFitConfidence = 0.5
	// guiderSettleSec is the pause after a correction before the shutter opens again.
	guiderSettleSec = 1.0
	// guiderSearchPx is how far from its last position the star is looked for. Generous, because a whole
	// sub has passed since the last measurement, but still bounded: a tracker that silently re-acquires
	// a DIFFERENT star injects a step of tens of arcseconds that then gets fitted as though it were
	// drift.
	guiderSearchPx = 60
)

// GuiderOptions tunes self-guiding.
type GuiderOptions struct {
	// RateArcsecPerSec is the speed corrections are delivered at, normally the mount's own configured
	// autoguide rate times sidereal.
	RateArcsecPerSec float64
	MinFitSamples    int
	MaxTrainArcsec   float64
	SettleSec        float64
	// Compensate enables the predictive pulse train. Off leaves recentring between subs, which is
	// still worth having on its own.
	Compensate bool
}

func (o GuiderOptions) withDefaults() GuiderOptions {
	if o.RateArcsecPerSec <= 0 {
		o.RateArcsecPerSec = guide.GuideRateArcsecPerSec(0)
	}
	if o.MinFitSamples <= 0 {
		o.MinFitSamples = guiderMinFitSamples
	}
	if o.MaxTrainArcsec <= 0 {
		o.MaxTrainArcsec = guiderMaxTrainArcsec
	}
	if o.SettleSec <= 0 {
		o.SettleSec = guiderSettleSec
	}
	return o
}

// GuideStats is what the UI and the session record show about guiding.
type GuideStats struct {
	Mode      string        `json:"mode"`
	Phase     string        `json:"phase"`
	Metrics   guide.Metrics `json:"metrics"`
	Observed  int           `json:"observed"`
	Corrected int           `json:"corrected"`
	// Compensated counts exposures that ran with a predictive pulse train.
	Compensated int `json:"compensated"`
	// DriftRAArcsecPerMin is the fitted rate, reported because it is the number that tells an operator
	// whether to go and improve the polar alignment.
	DriftRAArcsecPerMin  float64 `json:"drift_ra_arcsec_per_min"`
	DriftDecArcsecPerMin float64 `json:"drift_dec_arcsec_per_min"`
	FitSamples           int     `json:"fit_samples"`
	LastError            string  `json:"last_error,omitempty"`
}

// driftSample is one point of the raw, uncorrected trajectory.
type driftSample struct {
	tSec      float64
	raArcsec  float64
	decArcsec float64
}

// Guider corrects the mount from saved subs.
type Guider struct {
	client  *Client
	session *guide.Session
	opts    GuiderOptions

	mu      sync.Mutex
	started time.Time
	hasRef  bool
	starX   float64
	starY   float64

	// cumRA and cumDec are every correction commanded so far. The fit needs the trajectory the mount
	// WOULD have followed uncorrected, which is the measured error with the corrections REMOVED: the
	// corrections already moved the mount, so their effect is part of what was measured. Adding them
	// instead of subtracting them double-counts, and produces a slope of the wrong sign as soon as the
	// guider starts working.
	cumRA, cumDec float64
	history       []driftSample

	observed    int
	corrected   int
	compensated int
	lastErr     string
}

// NewGuider builds a self-guider. It returns nil when its dependencies are missing, so a caller can
// attach the result unconditionally and a session with no mount simply runs as it always did —
// the same contract NewTrackMonitor follows.
func NewGuider(client *Client, session *guide.Session, opts GuiderOptions) *Guider {
	if client == nil || session == nil {
		return nil
	}
	return &Guider{client: client, session: session, opts: opts.withDefaults()}
}

// Stats reports guiding progress. Nil-safe.
func (g *Guider) Stats() (GuideStats, bool) {
	if g == nil {
		return GuideStats{}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	st := GuideStats{
		Mode:        string(guide.ModeSelfGuide),
		Phase:       string(g.session.Phase()),
		Metrics:     g.session.Metrics(),
		Observed:    g.observed,
		Corrected:   g.corrected,
		Compensated: g.compensated,
		FitSamples:  len(g.history),
		LastError:   g.lastErr,
	}
	if fit, ok := g.fitLocked(); ok {
		st.DriftRAArcsecPerMin = fit.RARatePerSec * 60
		st.DriftDecArcsecPerMin = fit.DecRatePerSec * 60
	}
	return st, true
}

// Observe measures one saved sub and corrects the mount.
//
// Unlike the tracking monitor, this is synchronous: its whole purpose is to have acted before the next
// exposure opens, and a correction that arrives two frames late is worse than none. It still never
// fails the session — every error is recorded and swallowed, because losing a night's frames to a
// guiding problem would be a poor trade.
func (g *Guider) Observe(ctx context.Context, path string, midExposure time.Time) {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.started.IsZero() {
		g.started = midExposure
	}
	tSec := midExposure.Sub(g.started).Seconds()
	g.observed++
	g.mu.Unlock()

	obs, err := g.measure(path, tSec)
	if err != nil {
		g.note(err)
		return
	}

	sample, err := g.session.Update(obs)
	if err != nil {
		// A terminal guiding condition — diverging, or the star gone for too long. Stop correcting and
		// say so; the sequence carries on unguided rather than stopping.
		g.note(err)
		return
	}
	if obs.Found {
		g.mu.Lock()
		g.starX, g.starY, g.hasRef = obs.X, obs.Y, true
		g.recordDriftLocked(tSec, sample)
		g.mu.Unlock()
	}

	g.applyLocked(ctx, sample)
}

// measure reads the sub and finds the guide star in it.
func (g *Guider) measure(path string, tSec float64) (guide.Observation, error) {
	im, err := fits.ReadImage(path)
	if err != nil {
		return guide.Observation{}, fmt.Errorf("read %s: %w", path, err)
	}
	normalizeForDetection(im)

	g.mu.Lock()
	hasRef, x, y := g.hasRef, g.starX, g.starY
	g.mu.Unlock()

	var star guidestar.Star
	if hasRef {
		star, err = guidestar.Refind(im, x, y, guiderSearchPx, guidestar.Options{})
	} else {
		star, err = guidestar.Pick(im, guidestar.Options{})
	}
	if err != nil {
		// No usable star is an ordinary outcome — cloud, dew, a frame full of nebula. The session is told
		// so it can count the gap rather than have one silently missing from the series.
		return guide.Observation{TSec: tSec, Found: false}, nil
	}
	return guide.Observation{
		TSec: tSec, Found: true, X: star.X, Y: star.Y, SNR: star.SNR, HFD: star.HFD,
	}, nil
}

// applyLocked commands the recentring correction and waits for it to settle.
func (g *Guider) applyLocked(ctx context.Context, sample guide.Sample) {
	if sample.RACorrArcsec == 0 && sample.DecCorrArcsec == 0 {
		return
	}
	if err := g.client.GuidePulse(ctx, sample.RACorrArcsec, sample.DecCorrArcsec, g.opts.RateArcsecPerSec); err != nil {
		g.note(fmt.Errorf("guide pulse: %w", err))
		return
	}
	g.mu.Lock()
	g.cumRA += sample.RACorrArcsec
	g.cumDec += sample.DecCorrArcsec
	g.corrected++
	g.mu.Unlock()

	sleepCtx(ctx, g.opts.SettleSec)
}

// recordDriftLocked appends the uncorrected trajectory point. Caller holds g.mu.
func (g *Guider) recordDriftLocked(tSec float64, sample guide.Sample) {
	g.history = append(g.history, driftSample{
		tSec:      tSec,
		raArcsec:  sample.RAErrArcsec - g.cumRA,
		decArcsec: sample.DecErrArcsec - g.cumDec,
	})
	// The fit wants the recent past, not the whole night: a mount that was re-polar-aligned at midnight
	// should not be described by where it was drifting at dusk.
	const maxHistory = 40
	if len(g.history) > maxHistory {
		g.history = g.history[len(g.history)-maxHistory:]
	}
}

// note records a guiding failure without propagating it.
func (g *Guider) note(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lastErr = err.Error()
}

func sleepCtx(ctx context.Context, seconds float64) {
	if seconds <= 0 {
		return
	}
	t := time.NewTimer(time.Duration(seconds * float64(time.Second)))
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// isLight reports whether a step's frames have sky in them to guide on. A dark or a flat has none, and
// guiding on one would mean following a hot pixel.
func isLight(stepType string) bool {
	return stepType == "" || strings.EqualFold(stepType, "light")
}

// normalizeForDetection scales a freshly captured frame into the 0–1 range the star detector expects.
//
// This is not cosmetic. guidestar and postprocess.DetectStarPeaks express their saturation limits as
// FRACTIONS of full scale — 0.7 and 0.9 — because everything that fed them until now came either from
// device.Frame through guidestar.ImageFrom, which divides by 65535, or from a Siril output, which is
// already 0–1. A frame the sequencer just wrote is neither: fits.Write16 stores raw ADU, so every pixel
// in it reads as far above the saturation limit and the detector rejects the entire image. The symptom
// is not an error, it is a guider that silently never finds a star.
//
// The test is on the data rather than the header because both sources are legitimate here. A raw frame
// whose brightest pixel is under 1.5 ADU is a dead sensor, and a normalised frame can never exceed 1 by
// that margin, so there is no overlap to get wrong.
func normalizeForDetection(im *fits.Image) {
	const (
		fullScale     = 65535.0
		alreadyScaled = 1.5
	)
	var max float32
	for _, plane := range im.Pix {
		for _, v := range plane {
			if v > max {
				max = v
			}
		}
	}
	if max <= alreadyScaled {
		return
	}
	for _, plane := range im.Pix {
		for i, v := range plane {
			plane[i] = v / fullScale
		}
	}
}
