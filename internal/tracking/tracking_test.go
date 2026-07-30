package tracking

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// synth builds a run with a known drift and a known periodic error, so the fit can be checked against
// ground truth rather than against itself. seedNoise is deterministic — a real random source would
// make the assertions flaky for no benefit.
func synth(n int, cadenceSec, driftPerMin, peSemiAmp, period, noise float64) []Sample {
	out := make([]Sample, n)
	for i := range out {
		t := float64(i) * cadenceSec
		// A repeatable pseudo-noise: irrational multiples give an uncorrelated-looking sequence.
		jitter := noise * (math.Mod(float64(i)*0.6180339887, 1.0) - 0.5) * 2
		out[i] = Sample{
			TimeSec:   t,
			RAArcsec:  driftPerMin*t/60 + peSemiAmp*math.Sin(2*math.Pi*t/period) + jitter,
			DecArcsec: jitter * 0.5,
		}
	}
	return out
}

// The headline claim of the whole package: a night of subs recovers the mount's periodic error.
func TestAnalyze_RecoversDriftAndPeriodicError(t *testing.T) {
	// 120 subs at 60 s = 2 hours, an AVX-class 478 s worm, ±7″ semi-amplitude (so 14″ peak-to-peak,
	// typical for this mount), on top of 0.3″/min of polar-alignment drift.
	samples := synth(120, 60, 0.3, 7, 478, 0.4)

	rep := Analyze(samples, 478, 1.06)
	require.NotNil(t, rep)

	assert.InDelta(t, 0.3, rep.DriftRAArcsecPerMin, 0.05, "drift rate must come back out")
	assert.InDelta(t, 14, rep.PEAmplitudeArcsec, 2.0, "peak-to-peak periodic error")
	assert.InDelta(t, 478, rep.PEPeriodSec, 30, "worm period")
	assert.Greater(t, rep.PEConfidence, 0.8, "a clean signal must fit convincingly")
	assert.Less(t, rep.ResidualRMSArcsec, 1.5, "only the injected noise should remain")
}

// The practical output: how long a sub can run. Doubling the periodic error must halve it.
func TestAnalyze_MaxUnguidedScalesWithError(t *testing.T) {
	calm := Analyze(synth(120, 60, 0.1, 3, 478, 0.2), 478, 1.06)
	rough := Analyze(synth(120, 60, 0.1, 12, 478, 0.2), 478, 1.06)
	require.NotNil(t, calm)
	require.NotNil(t, rough)

	assert.Positive(t, calm.MaxUnguidedSec)
	assert.Greater(t, calm.MaxUnguidedSec, rough.MaxUnguidedSec*2,
		"a mount with four times the periodic error must not be given the same exposure advice")
	// Sanity against the physics: ±3″ over 478 s peaks at 2π·3/478 ≈ 0.039″/s, so 1.5 px at
	// 1.06″/px ≈ 1.59″ of budget gives roughly 40 s — the right order for an unguided AVX.
	assert.InDelta(t, 40, calm.MaxUnguidedSec, 15)
}

// A mount with no periodic error must not have one invented for it.
func TestAnalyze_NoPeriodicSignalIsReportedAsSuch(t *testing.T) {
	rep := Analyze(synth(120, 60, 0.5, 0, 478, 1.0), 478, 1.06)
	require.NotNil(t, rep)

	assert.Less(t, rep.PEConfidence, 0.3,
		"fitting noise must not be reported as a confident periodic error")
	assert.Contains(t, rep.Warnings[0]+rep.Warnings[len(rep.Warnings)-1], "periodic")
	assert.InDelta(t, 0.5, rep.DriftRAArcsecPerMin, 0.05, "the drift is still measurable")
}

// Drift must be removed before the periodic fit: otherwise a steady drift looks like the rising half
// of an enormous slow wave, and the reported amplitude is nonsense. This is the failure mode the
// two-stage fit exists to prevent.
func TestAnalyze_DriftIsNotMistakenForPeriodicError(t *testing.T) {
	// Heavy drift, no periodic error at all.
	rep := Analyze(synth(120, 60, 3.0, 0, 478, 0.2), 478, 1.06)
	require.NotNil(t, rep)

	assert.InDelta(t, 3.0, rep.DriftRAArcsecPerMin, 0.1)
	assert.Less(t, rep.PEAmplitudeArcsec, 2.0,
		"a pure drift must not be reported as several arcseconds of periodic error")
}

// Samples arriving out of order is normal (frames finish in whatever order a queue drains them).
func TestAnalyze_SortsSamplesByTime(t *testing.T) {
	ordered := synth(60, 60, 0.3, 7, 478, 0.2)
	shuffled := make([]Sample, len(ordered))
	for i, s := range ordered {
		shuffled[(i*37)%len(ordered)] = s // a coprime stride: a deterministic shuffle
	}
	a, b := Analyze(ordered, 478, 1.06), Analyze(shuffled, 478, 1.06)
	require.NotNil(t, a)
	require.NotNil(t, b)
	assert.InDelta(t, a.PEAmplitudeArcsec, b.PEAmplitudeArcsec, 1e-6)
	assert.InDelta(t, a.DriftRAArcsecPerMin, b.DriftRAArcsecPerMin, 1e-9)
}

// Too little data must produce nothing rather than a confident-looking guess.
func TestAnalyze_RefusesTooFewSamples(t *testing.T) {
	assert.Nil(t, Analyze(synth(5, 60, 0.3, 7, 478, 0.2), 478, 1.06))
	assert.Nil(t, Analyze(nil, 478, 1.06))
}

// A short run still reports, but says out loud that the period is provisional.
func TestAnalyze_WarnsOnAShortRun(t *testing.T) {
	rep := Analyze(synth(15, 30, 0.3, 7, 478, 0.2), 478, 1.06) // 7 minutes: under one worm period
	require.NotNil(t, rep)
	require.NotEmpty(t, rep.Warnings)
	joined := ""
	for _, w := range rep.Warnings {
		joined += w + " "
	}
	assert.Contains(t, joined, "worm period")
	assert.Contains(t, joined, "few samples")
}

// Without an image scale there is no pixel budget, so no exposure advice can be given.
func TestAnalyze_NoScaleMeansNoExposureAdvice(t *testing.T) {
	rep := Analyze(synth(120, 60, 0.3, 7, 478, 0.2), 478, 0)
	require.NotNil(t, rep)
	assert.Zero(t, rep.MaxUnguidedSec)
	assert.Positive(t, rep.PEAmplitudeArcsec, "the mount measurement is still valid")
}

// The hint is a starting point, not an answer: a mount whose real worm period differs must still be
// measured correctly.
func TestAnalyze_FindsAPeriodAwayFromTheHint(t *testing.T) {
	rep := Analyze(synth(200, 45, 0.2, 6, 600, 0.3), 478, 1.06)
	require.NotNil(t, rep)
	assert.InDelta(t, 600, rep.PEPeriodSec, 40, "the search must follow the data, not the hint")
	assert.Greater(t, rep.PEConfidence, 0.7)
}
