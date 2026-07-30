// Package tracking measures how well a mount actually tracks, from the frames it has already taken.
//
// This is the measurement half of the periodic-error question. A mount's RA worm repeats its error
// once per revolution — a few minutes — so the drift between consecutive subs, folded on that
// period, recovers the error curve exactly the way a light curve is folded on an orbital period.
// Even 60-second subs give a dozen samples per cycle, and a night of them gives hundreds.
//
// What it yields is immediately useful on its own, before any correction exists: how much periodic
// error this mount really has, how fast it drifts, and therefore how long an unguided sub can be
// before stars trail. Those are the numbers that decide a night's exposure setting, and today they
// are guesswork.
//
// The correction half (feeding this curve back as a rate trim) is deliberately NOT here. It moves
// hardware on a model fitted from data, needs on-sky iteration to trust, and the honest order is to
// measure first.
package tracking

import (
	"math"
	"sort"
)

// Sample is one measured pointing error: where the frame actually landed relative to the first
// frame of the run, at a known time.
type Sample struct {
	TimeSec   float64 // seconds since the run started (frame mid-exposure)
	RAArcsec  float64 // drift east-positive, corrected for cos(dec) so it is a real angle
	DecArcsec float64
}

// Report is what a night of samples says about the mount.
type Report struct {
	Samples int     `json:"samples"`
	SpanSec float64 `json:"span_sec"`

	// DriftRA/DecArcsecPerMin is the systematic, non-periodic drift: polar-alignment error in
	// declination, and any rate error in right ascension.
	DriftRAArcsecPerMin  float64 `json:"drift_ra_arcsec_per_min"`
	DriftDecArcsecPerMin float64 `json:"drift_dec_arcsec_per_min"`

	// PEAmplitudeArcsec is the peak-to-peak periodic error left after removing the drift, and
	// PEPeriodSec the worm period that best explains it.
	PEAmplitudeArcsec float64 `json:"pe_amplitude_arcsec"`
	PEPeriodSec       float64 `json:"pe_period_sec"`
	PEConfidence      float64 `json:"pe_confidence"` // 0–1: how much of the residual the fit explains

	// ResidualRMSArcsec is what remains after drift AND periodic error — seeing, wind, backlash.
	ResidualRMSArcsec float64 `json:"residual_rms_arcsec"`

	// MaxUnguidedSec is how long a sub can be before the total motion exceeds one pixel-ish of
	// blur, at the given image scale. The practical output of the whole analysis.
	MaxUnguidedSec float64 `json:"max_unguided_sec"`

	Warnings []string `json:"warnings,omitempty"`
}

// minSamples is the fewest measurements worth reporting on. Below this the fit would be describing
// noise, and a confident-looking wrong number is worse than no number.
const minSamples = 12

// Analyze turns a run's samples into a report. periodHintSec is the mount's nominal worm period
// (an AVX is around 8 minutes); the search runs around it rather than blindly over all periods.
func Analyze(samples []Sample, periodHintSec, imageScaleArcsecPx float64) *Report {
	if len(samples) < minSamples {
		return nil
	}
	sorted := append([]Sample(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].TimeSec < sorted[j].TimeSec })
	span := sorted[len(sorted)-1].TimeSec - sorted[0].TimeSec
	if span <= 0 {
		return nil
	}

	rep := &Report{Samples: len(sorted), SpanSec: span}

	// Straight-line drift first: polar misalignment and rate error are systematic, and leaving them
	// in would masquerade as an enormous long-period "periodic" error.
	raSlope, raIntercept := fitLine(sorted, func(s Sample) float64 { return s.RAArcsec })
	decSlope, decIntercept := fitLine(sorted, func(s Sample) float64 { return s.DecArcsec })
	rep.DriftRAArcsecPerMin = raSlope * 60
	rep.DriftDecArcsecPerMin = decSlope * 60

	// Periodic error lives in RA (it is the worm), so the fit runs on the RA residual.
	residual := make([]Sample, len(sorted))
	for i, s := range sorted {
		residual[i] = Sample{
			TimeSec:   s.TimeSec,
			RAArcsec:  s.RAArcsec - (raSlope*s.TimeSec + raIntercept),
			DecArcsec: s.DecArcsec - (decSlope*s.TimeSec + decIntercept),
		}
	}

	if periodHintSec <= 0 {
		periodHintSec = 478 // the AVX's worm period, near enough for the search to start from
	}
	best := fitPeriodic(residual, periodHintSec, span)
	rep.PEAmplitudeArcsec = best.amplitude * 2 // peak-to-peak, the number mounts are specified by
	rep.PEPeriodSec = best.period
	rep.PEConfidence = best.explained

	rep.ResidualRMSArcsec = rmsAfter(residual, best)
	rep.MaxUnguidedSec = maxUnguided(rep, imageScaleArcsecPx)
	rep.Warnings = warningsFor(rep, span, periodHintSec)
	return rep
}

// fitLine is an ordinary least-squares fit of value against time.
func fitLine(samples []Sample, value func(Sample) float64) (slope, intercept float64) {
	var sumT, sumV, sumTT, sumTV float64
	n := float64(len(samples))
	for _, s := range samples {
		v := value(s)
		sumT += s.TimeSec
		sumV += v
		sumTT += s.TimeSec * s.TimeSec
		sumTV += s.TimeSec * v
	}
	den := n*sumTT - sumT*sumT
	if den == 0 {
		return 0, sumV / n
	}
	slope = (n*sumTV - sumT*sumV) / den
	intercept = (sumV - slope*sumT) / n
	return slope, intercept
}

// periodicFit is one candidate worm period and the sinusoid that best matches at it.
type periodicFit struct {
	period    float64
	amplitude float64 // semi-amplitude
	phase     float64
	explained float64 // fraction of the residual variance the fit accounts for
}

// fitPeriodic searches periods around the hint and fits a sinusoid at each by least squares. A
// sinusoid rather than a free-form curve because the fundamental carries most of the worm's error,
// and with sparse samples a richer model would fit the noise.
func fitPeriodic(residual []Sample, hintSec, span float64) periodicFit {
	best := periodicFit{period: hintSec}
	// Searching ±40 % of the hint covers the spread between mount models and any error in the
	// nominal figure, without wandering into periods the data cannot support.
	lo, hi := hintSec*0.6, hintSec*1.4
	if span < hi {
		hi = math.Max(span, lo) // a period longer than the run is unmeasurable
	}
	total := variance(residual)
	if total <= 0 {
		return best
	}
	const steps = 240
	for i := 0; i <= steps; i++ {
		period := lo + (hi-lo)*float64(i)/steps
		if period <= 0 {
			continue
		}
		amp, phase, explained := fitSinusoid(residual, period, total)
		if explained > best.explained {
			best = periodicFit{period: period, amplitude: amp, phase: phase, explained: explained}
		}
	}
	return best
}

// fitSinusoid projects the residual onto sine and cosine at one period — the same projection a
// Lomb-Scargle periodogram uses, which is what makes it valid for UNEVENLY spaced samples (subs are
// not evenly spaced: filters change, clouds pass, dithers settle).
func fitSinusoid(residual []Sample, period, totalVar float64) (amplitude, phase, explained float64) {
	var sumSin, sumCos, sumSinSin, sumCosCos float64
	w := 2 * math.Pi / period
	for _, s := range residual {
		sn, cs := math.Sincos(w * s.TimeSec)
		sumSin += s.RAArcsec * sn
		sumCos += s.RAArcsec * cs
		sumSinSin += sn * sn
		sumCosCos += cs * cs
	}
	a, b := 0.0, 0.0
	if sumSinSin > 0 {
		a = sumSin / sumSinSin
	}
	if sumCosCos > 0 {
		b = sumCos / sumCosCos
	}
	amplitude = math.Hypot(a, b)
	phase = math.Atan2(b, a)

	// How much of the variance the fitted wave removes.
	var after float64
	for _, s := range residual {
		model := a*math.Sin(w*s.TimeSec) + b*math.Cos(w*s.TimeSec)
		d := s.RAArcsec - model
		after += d * d
	}
	after /= float64(len(residual))
	if totalVar > 0 {
		explained = 1 - after/totalVar
	}
	return amplitude, phase, math.Max(0, explained)
}

func variance(residual []Sample) float64 {
	if len(residual) == 0 {
		return 0
	}
	var mean float64
	for _, s := range residual {
		mean += s.RAArcsec
	}
	mean /= float64(len(residual))
	var v float64
	for _, s := range residual {
		d := s.RAArcsec - mean
		v += d * d
	}
	return v / float64(len(residual))
}

// rmsAfter is the scatter left once drift and the fitted wave are removed: seeing, wind, backlash —
// the part no correction can predict.
func rmsAfter(residual []Sample, fit periodicFit) float64 {
	if len(residual) == 0 || fit.period <= 0 {
		return 0
	}
	w := 2 * math.Pi / fit.period
	var sum float64
	for _, s := range residual {
		model := fit.amplitude * math.Sin(w*s.TimeSec+fit.phase)
		dRA := s.RAArcsec - model
		sum += dRA*dRA + s.DecArcsec*s.DecArcsec
	}
	return math.Sqrt(sum / float64(len(residual)))
}

// maxUnguided is how long a sub can run before the mount's own motion smears a star by more than
// about one and a half pixels — the point at which trailing becomes visible at 100 %.
func maxUnguided(rep *Report, scaleArcsecPx float64) float64 {
	if scaleArcsecPx <= 0 {
		return 0
	}
	budget := 1.5 * scaleArcsecPx

	// Periodic error is fastest at the zero crossing of the wave: 2π·semi-amplitude / period.
	peRate := 0.0
	if rep.PEPeriodSec > 0 {
		peRate = 2 * math.Pi * (rep.PEAmplitudeArcsec / 2) / rep.PEPeriodSec
	}
	driftRate := math.Hypot(rep.DriftRAArcsecPerMin, rep.DriftDecArcsecPerMin) / 60
	total := peRate + driftRate
	if total <= 0 {
		return 0
	}
	return budget / total
}

func warningsFor(rep *Report, span, hint float64) []string {
	var out []string
	if span < 2*hint {
		out = append(out, "the run is shorter than two worm periods — the periodic-error fit is provisional")
	}
	if rep.PEConfidence < 0.3 {
		out = append(out, "no clear periodic signal: the drift may be dominated by seeing, wind or backlash")
	}
	if rep.Samples < 30 {
		out = append(out, "few samples — take a longer run for a firmer figure")
	}
	return out
}
