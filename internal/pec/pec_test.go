package pec

import (
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// avxGeometry is the real thing: 88 bins over a 180-tooth worm, one table unit per sidereal/1024.
func avxGeometry() Geometry {
	return Geometry{Bins: 88, WormPeriodSec: 7200 / 15.0410686, LSBArcsecPerSec: 15.0410686 / 1024}
}

// sineSamples generates a run of a pure worm sinusoid, optionally with drift.
func sineSamples(g Geometry, ppArcsec, phaseRad, driftPerSec float64, cycles int, perBin int) []Sample {
	n := cycles * g.Bins * perBin
	step := g.WormPeriodSec / float64(g.Bins*perBin)
	out := make([]Sample, 0, n)
	for i := 0; i < n; i++ {
		t := float64(i) * step
		phase := 2 * math.Pi * t / g.WormPeriodSec
		out = append(out, Sample{
			TimeSec:   t,
			PhaseBins: wrapPhase(t/g.BinSec(), g.Bins),
			Arcsec:    (ppArcsec/2)*math.Sin(phase+phaseRad) + driftPerSec*t,
		})
	}
	return out
}

func TestGeometry_AVXWorm(t *testing.T) {
	g := avxGeometry()
	assert.InDelta(t, 478.68, g.WormPeriodSec, 0.01)
	assert.InDelta(t, 5.44, g.BinSec(), 0.01)
	assert.InDelta(t, 10.88, g.NyquistPeriodSec(), 0.02,
		"two bins — the fastest wobble an 88-bin table can even represent")
}

// The correction must be the NEGATIVE derivative of the position error: if the axis is running ahead
// and getting further ahead, the mount has to slow down. Getting this backwards doubles the error
// instead of cancelling it, and is the single most likely first-contact bug.
func TestCorrection_IsTheNegativeDerivative(t *testing.T) {
	g := avxGeometry()
	fit, err := FitCurve(sineSamples(g, 15, 0, 0, 4, 3), g)
	require.NoError(t, err)

	rates := Correction(fit, g, 0)
	require.Len(t, rates, g.Bins)

	// E(φ) = A·sin(2πφ/B) ⇒ c = −dE/dt = −A·(2π/T)·cos(2πφ/T), so the correction is most negative
	// where the error rises fastest — at phase zero.
	amp := 15.0 / 2
	wantPeak := amp * 2 * math.Pi / g.WormPeriodSec
	assert.InDelta(t, -wantPeak, rates[0], wantPeak*0.05,
		"where the error is climbing fastest, the mount must slow down")

	quarter := g.Bins / 4
	assert.InDelta(t, 0, rates[quarter], wantPeak*0.05,
		"at the error's peak the rate correction passes through zero")
}

// A table with a net rate makes the mount walk a little further every revolution. The edge-evaluated
// difference of a periodic fit cannot produce one, and the quantiser must not reintroduce it.
func TestCorrection_HasNoNetRate(t *testing.T) {
	g := avxGeometry()
	fit, err := FitCurve(sineSamples(g, 15, 0.7, 0, 4, 3), g)
	require.NoError(t, err)

	rates := Correction(fit, g, 0)
	var sum float64
	for _, r := range rates {
		sum += r
	}
	assert.InDelta(t, 0, sum, 1e-9, "a periodic curve differences to exactly zero net rate")

	q := Quantise(rates, g)
	require.NotNil(t, q)
	total := 0
	for _, v := range q.Bins {
		total += int(v)
	}
	assert.Equal(t, 0, total, "and rounding must not put one back")
}

// Integrating the rates the mount will replay must reproduce the negative of the measured error, or
// the correction does not cancel what was measured.
func TestCorrection_IntegratesBackToTheMeasuredCurve(t *testing.T) {
	g := avxGeometry()
	const pp = 15.0
	fit, err := FitCurve(sineSamples(g, pp, 1.3, 0, 4, 3), g)
	require.NoError(t, err)
	rates := Correction(fit, g, 0)

	// A correction is only defined up to a constant — the mount starts correcting from wherever it
	// is — so compare accumulated correction against the error's CHANGE from the index.
	pos := 0.0
	worst := 0.0
	for b := 0; b < g.Bins; b++ {
		// The mount holds rates[b] across bin b, so position moves by rate × bin time.
		want := -(fit.Curve[b] - fit.Curve[0])
		if d := math.Abs(pos - want); d > worst {
			worst = d
		}
		pos += rates[b] * g.BinSec()
	}
	assert.Less(t, worst, 0.01,
		"the replayed correction must track the negative of the fitted error at every edge")
}

// Drift and periodic error are fitted together because a run that is not a whole number of cycles
// makes a slice of the worm's sine look exactly like a slope.
func TestFitCurve_SeparatesDriftFromTheWorm(t *testing.T) {
	g := avxGeometry()
	const pp, drift = 14.0, 0.005 // 0.3″/min

	// Deliberately 3.4 cycles, so sequential detrending would eat part of the sine.
	samples := sineSamples(g, pp, 0.4, drift, 4, 3)
	samples = samples[:int(float64(len(samples))*3.4/4)]

	fit, err := FitCurve(samples, g)
	require.NoError(t, err)

	assert.InDelta(t, drift, fit.DriftArcsecPerSec, drift*0.1, "the drift comes back")
	assert.InDelta(t, pp, PeakToPeak(fit.Curve), 0.5,
		"and the worm keeps its full amplitude rather than losing part of it to the slope")
}

// Harmonics indistinguishable from noise must be left out: writing them replays one night's seeing
// into the mount for ever.
func TestFitCurve_DropsHarmonicsLostInNoise(t *testing.T) {
	g := avxGeometry()
	samples := sineSamples(g, 15, 0, 0, 4, 3)
	// Realistic seeing noise, plus a wobble far below it. Without the noise there is no floor to be
	// below, and every numerical artefact would score as real.
	rng := rand.New(rand.NewSource(7))
	for i := range samples {
		samples[i].Arcsec += 0.5 * rng.NormFloat64()
		samples[i].Arcsec += 0.01 * math.Sin(11*2*math.Pi*samples[i].PhaseBins/float64(g.Bins))
	}
	fit, err := FitCurve(samples, g)
	require.NoError(t, err)

	var significant []int
	for _, h := range fit.Harmonics {
		if h.Significant {
			significant = append(significant, h.K)
		}
	}
	assert.Contains(t, significant, 1, "the worm itself is real")
	assert.NotContains(t, significant, 11, "a 0.01″ ripple under 0.5″ of seeing is not")
}

func TestFitCurve_RefusesTooFewSamples(t *testing.T) {
	g := avxGeometry()
	_, err := FitCurve(sineSamples(g, 15, 0, 0, 1, 1)[:5], g)
	assert.ErrorIs(t, err, ErrTooFewSamples)
}

// Rate quantisation errors INTEGRATE into position, so rounding each bin on its own random-walks the
// accumulated error. Carrying the residual forward has to keep it bounded.
func TestQuantise_KeepsAccumulatedPositionErrorBounded(t *testing.T) {
	g := avxGeometry()
	fit, err := FitCurve(sineSamples(g, 15, 0, 0, 4, 3), g)
	require.NoError(t, err)
	rates := Correction(fit, g, 0)

	q := Quantise(rates, g)
	require.NotNil(t, q)
	assert.Zero(t, q.Clipped, "a 15″ worm needs about 7 units of 127 — clipping means the fit is wrong")
	assert.Less(t, q.MaxAbs, 20)

	// Walk both the ideal and the quantised tables, comparing accumulated position.
	var ideal, actual, worst float64
	for b := range rates {
		ideal += rates[b] * g.BinSec()
		actual += float64(q.Bins[b]) * g.LSBArcsecPerSec * g.BinSec()
		if d := math.Abs(ideal - actual); d > worst {
			worst = d
		}
	}
	bound := g.LSBArcsecPerSec * g.BinSec() // half a unit of position, generously rounded up
	assert.Less(t, worst, bound,
		"error feedback bounds the drift at a fraction of an arcsecond, where naive rounding would not")
}

func TestQuantise_NeverEmitsTheAsymmetricRail(t *testing.T) {
	g := avxGeometry()
	// A correction far beyond anything the table can express.
	rates := make([]float64, g.Bins)
	for i := range rates {
		rates[i] = 50 * math.Sin(2*math.Pi*float64(i)/float64(g.Bins))
	}
	q := Quantise(rates, g)
	require.NotNil(t, q)
	assert.Positive(t, q.Clipped, "this curve cannot fit, and the caller must be told")
	for _, v := range q.Bins {
		assert.NotEqual(t, int8(-128), v, "-128 makes the range asymmetric and some firmware special-cases it")
	}
}

// A mount whose error does not repeat cannot be helped by a table that replays the same thing every
// revolution. Measuring that BEFORE writing is what keeps the feature honest.
func TestMeasureRepeatability_SeparatesTheWormFromTheWeather(t *testing.T) {
	g := avxGeometry()

	t.Run("a clean worm repeats", func(t *testing.T) {
		rep := MeasureRepeatability(sineSamples(g, 15, 0, 0, 6, 3), g)
		assert.Greater(t, rep.Coherent, 0.95)
		assert.InDelta(t, 6, rep.Cycles, 0.1)
	})

	t.Run("a non-repeating wobble does not", func(t *testing.T) {
		samples := sineSamples(g, 2, 0, 0, 6, 3)
		// A component at an incommensurate period is never in the same place twice.
		for i := range samples {
			samples[i].Arcsec += 6 * math.Sin(2*math.Pi*samples[i].TimeSec/(g.WormPeriodSec*0.371))
		}
		rep := MeasureRepeatability(samples, g)
		assert.Less(t, rep.Coherent, MinCoherent,
			"most of this is not the worm, and a table fitted to it would replay noise all night")
	})
}

func TestFold_ClipsOutliersAndFillsGaps(t *testing.T) {
	g := avxGeometry()
	samples := sineSamples(g, 15, 0, 0, 3, 4)

	// One catastrophic sample, of the kind a satellite trail produces.
	samples[100].Arcsec += 500

	folded := Fold(samples, g)
	require.NotNil(t, folded)
	assert.Equal(t, 0, folded.Empty)
	assert.InDelta(t, 15, PeakToPeak(folded.Mean), 1.0,
		"one wild sample must not drag a bin that then gets replayed all night")
}

// The honest before-and-after: measure the curve rather than model it as one sinusoid, because what
// survives PEC is the fast harmonics and those move a star far faster per arcsecond of amplitude.
func TestMaxUnguidedSec_MeasuresTheCurveRatherThanAssumingASineWave(t *testing.T) {
	g := avxGeometry()
	budget := BudgetArcsec(1.06) // FC-100DF + ASI1600

	fundamental := make([]float64, g.Bins+1)
	fast := make([]float64, g.Bins+1)
	for b := 0; b <= g.Bins; b++ {
		phase := 2 * math.Pi * float64(b) / float64(g.Bins)
		fundamental[b] = 7.5 * math.Sin(phase)
		fast[b] = 7.5 * math.Sin(10*phase) // same amplitude, ten times the frequency
	}

	slow := MaxUnguidedSec(fundamental, g, 0, budget)
	quick := MaxUnguidedSec(fast, g, 0, budget)

	assert.InDelta(t, 16, slow, 4, "a 15″ p-p worm on this rig is about a 16-second sub")
	assert.Less(t, quick, slow/5,
		"the same amplitude at ten times the frequency trails five times sooner — "+
			"which a single-sinusoid model would score identically")
}

func TestImprovement_FlagsAnInvertedCorrection(t *testing.T) {
	g := avxGeometry()
	before := make([]float64, g.Bins+1)
	worse := make([]float64, g.Bins+1)
	for b := range before {
		phase := 2 * math.Pi * float64(b) / float64(g.Bins)
		before[b] = 7.5 * math.Sin(phase)
		worse[b] = 15 * math.Sin(phase) // what an inverted table produces: double, not zero
	}
	got := Compare(before, worse, g, 0, 0, BudgetArcsec(1.06))
	assert.True(t, got.Worsened())
	assert.Less(t, got.AmplitudeRatio(), 1.0)
}
