package photom

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// skyCurve builds a reference FrameCurve from a SKY-DOMINATED fixture — 85% near-pedestal pixels with
// a mild spread plus a 15% signal ramp tail — exercising the real MeasureImage path.
//
// CONTRACT-CHANGE JUSTIFICATION: this replaces the old uniform-ramp fixture. A uniform distribution
// has MAD ≈ 25% of its full range, so its P5..P97.5 span is only ~2.5 noise sigmas — BELOW even pure
// Gaussian noise (~3.6σ) — which the flat-curve gate (the narrowband mis-measure fix) rightly reads
// as "nothing but noise". Real lights are sky-dominated with a signal tail, which is the premise the
// package itself is built on (Bg at P40, MAD ≈ sky noise); the fixtures now model that domain.
func skyCurve(n int) FrameCurve {
	im := fits.NewImage(n, 1, 1)
	for i := 0; i < n; i++ {
		if i%100 < 85 {
			im.Pix[0][i] = 0.05 + 0.004*float32(i%17)/17 // sky: pedestal + mild spread
		} else {
			im.Pix[0][i] = 0.05 + 0.95*float32(i)/float32(n-1) // signal tail: stars/nebulosity
		}
	}
	return MeasureImage(im)
}

// flatCurve builds a signal-free fixture: pure pedestal + noise-scale jitter — a narrowband sky whose
// percentile spans measure nothing but noise (curveFlat must trip on it).
func flatCurve(n int) FrameCurve {
	return flatCurveAt(n, 0.05)
}

// flatCurveAt is flatCurve with a chosen sky-pedestal level. Cross-config seed tests need pedestals
// CONSISTENT with the seeded flux ratio: a brighter capture's sky pedestal really is brighter, and
// the bg-ratio cross-check (deliberately) overrides a seed the measured backgrounds disprove.
func flatCurveAt(n int, pedestal float32) FrameCurve {
	im := fits.NewImage(n, 1, 1)
	for i := 0; i < n; i++ {
		im.Pix[0][i] = pedestal + 0.003*float32(i%31)/31
	}
	return MeasureImage(im)
}

// affineFrame returns a FrameCurve whose probes are the exact inverse affine (ref-offset)/scale of ref,
// so an ideal fit recovers scale and offset. Noise scales with the distribution (MAD is affine).
func affineFrame(ref FrameCurve, scale, offset float64) FrameCurve {
	var f FrameCurve
	for i := range CurveQ {
		f.Q[i] = (ref.Q[i] - offset) / scale
	}
	f.Bg = f.Q[bgIdx]
	f.Noise = ref.Noise / scale
	return f
}

func TestFitCurves_ExactAffineRecovery(t *testing.T) {
	ref := skyCurve(10000)
	frame := affineFrame(ref, 1.8, 0.02)

	tr := FitCurves(frame, ref, Meta{}, Meta{})

	assert.InDelta(t, 1.8, tr.Scale, 1e-9, "Theil–Sen recovers the exact slope")
	assert.InDelta(t, 0.02, tr.Offset, 1e-9, "median offset recovers the exact intercept")
	assert.False(t, tr.Clamped)
	assert.False(t, tr.MetaDisagree)
	assert.False(t, tr.MetaSeeded, "a signal-bearing curve is measured, never seeded")
	assert.Less(t, tr.Resid, 1e-6, "an exact affine fit has ~zero residual")
}

func TestFitCurves_RobustToSaturatedProbe(t *testing.T) {
	ref := skyCurve(10000)
	frame := affineFrame(ref, 1.8, 0.02)
	frame.Q[fitHiIdx] *= 3 // corrupt the P97.5 probe (star saturation)

	tr := FitCurves(frame, ref, Meta{}, Meta{})

	assert.InDelta(t, 1.8, tr.Scale, 1.8*0.01, "Theil–Sen stays within 1% despite one corrupt probe")
}

// CONTRACT-CHANGE JUSTIFICATION: the absolute clamp widened from [0.2, 5.0] to [0.1, 10.0] — the old
// 5.0 ceiling sat BELOW genuine ZWO cross-gain flux ratios (g250↔g400 ≈ 5.6×), so healthy mixed-gain
// measurements were systematically clamped wrong ("Ha clamped at 5×"). The probe slope moves from 8
// (now in range) to 12 to keep exercising the ceiling.
func TestFitCurves_ClampsHighScale(t *testing.T) {
	ref := skyCurve(10000)
	frame := affineFrame(ref, 12.0, 0) // slope 12 → out of [0.1, 10.0]

	tr := FitCurves(frame, ref, Meta{}, Meta{})

	assert.Equal(t, scaleMax, tr.Scale, "scale clamped to the upper bound")
	assert.True(t, tr.Clamped)
}

// The original mixed-gain regression: a REAL cross-gain ratio above the old 5.0 ceiling must now be
// measured and applied un-clamped (ASI1600 g250 → g400 predicts 10^(150/200) ≈ 5.62×).
func TestFitCurves_WideClampAllowsRealCrossGain(t *testing.T) {
	ref := skyCurve(10000)
	const trueScale = 5.623
	frame := affineFrame(ref, trueScale, 0.01)
	fm := Meta{ExposureMs: 30000, Gain: 250, GainKnown: true, Instrument: "ZWO ASI1600MM Pro"}
	rm := Meta{ExposureMs: 30000, Gain: 400, GainKnown: true, Instrument: "ZWO ASI1600MM Pro"}

	tr := FitCurves(frame, ref, fm, rm)

	assert.InDelta(t, trueScale, tr.Scale, trueScale*0.01, "a genuine 5.6× cross-gain scale survives")
	assert.False(t, tr.Clamped, "the old 5.0 ceiling clamped this healthy measurement")
	assert.False(t, tr.MetaDisagree, "measurement matches the gain prediction")
	assert.False(t, tr.MetaSeeded)
}

// The narrowband mis-measure fix: a flat (sky-pedestal) curve cannot be fitted — its quantile map
// yields the noise-width ratio, not the flux ratio — so the scale is seeded from the header
// exposure/gain prediction instead.
func TestFitCurves_FlatNarrowbandSeedsFromMeta(t *testing.T) {
	// Pedestals consistent with the flux ratio (a g400 sky really is ~5.6× a g250 one), so the
	// bg-ratio cross-check agrees and the header seed stands.
	ref := flatCurveAt(10000, 0.281)
	frame := flatCurveAt(10000, 0.05)
	fm := Meta{ExposureMs: 30000, Gain: 250, GainKnown: true, Instrument: "ZWO ASI1600MM Pro"}
	rm := Meta{ExposureMs: 30000, Gain: 400, GainKnown: true, Instrument: "ZWO ASI1600MM Pro"}

	tr := FitCurves(frame, ref, fm, rm)

	assert.InDelta(t, 5.623, tr.Scale, 0.01, "scale IS the g250→g400 prediction (10^(150/200))")
	assert.True(t, tr.MetaSeeded)
	assert.False(t, tr.MetaDisagree, "a seeded scale agrees with its own prediction by construction")
	assert.False(t, tr.Clamped)
}

// pedestalCurve is flatCurve with a controllable pedestal level and jitter amplitude — two of these
// model sky-limited frames of the same field at different exposures (sky flux ∝ exposure, noise
// width ∝ √exposure, no measurable signal).
func pedestalCurve(n int, level, amp float32) FrameCurve {
	im := fits.NewImage(n, 1, 1)
	for i := 0; i < n; i++ {
		im.Pix[0][i] = level + amp*float32(i%31)/31
	}
	return MeasureImage(im)
}

func TestFitCurves_SeededScaleBypassesClamp(t *testing.T) {
	// A header-derived seed is not a measurement: a legitimate 20× exposure ratio (30 s vs 600 s)
	// must survive un-clamped — the absolute clamp exists to bound mis-measurements only.
	ref := flatCurveAt(10000, 1.0) // 600 s sky pedestal ≈ 20× the 30 s one — consistent with the seed
	frame := flatCurveAt(10000, 0.05)
	fm := Meta{ExposureMs: 30000, Gain: 200, GainKnown: true, Instrument: "ZWO ASI1600MM Pro"}
	rm := Meta{ExposureMs: 600000, Gain: 200, GainKnown: true, Instrument: "ZWO ASI1600MM Pro"}

	tr := FitCurves(frame, ref, fm, rm)

	assert.InDelta(t, 20.0, tr.Scale, 1e-9, "the exposure prediction stands beyond scaleMax")
	assert.True(t, tr.MetaSeeded)
	assert.False(t, tr.Clamped, "seeds bypass the mis-measurement clamp")
}

func TestFitCurves_SkyLimitedBroadbandSeedsNotNoiseRatio(t *testing.T) {
	// The task #312 two-night L physics: a star-sparse broadband frame's FIT probes are pure sky,
	// and the quantile-onto-quantile slope of two sky-limited frames is their NOISE-WIDTH ratio
	// (≈ √ of the flux ratio — here √3 ≈ 0.58 for 90 s → 30 s), not the flux ratio (1/3). The flat
	// gate must route such pairs to the header seed; measuring them would be confidently wrong.
	frame := pedestalCurve(10000, 0.15, 0.005) // 90 s: 3× the sky flux, wider noise
	ref := pedestalCurve(10000, 0.05, 0.003)   // 30 s reference: dimmer sky, narrower noise
	fm := Meta{ExposureMs: 90000, Gain: 250, GainKnown: true, Instrument: "ZWO ASI1600MM Pro"}
	rm := Meta{ExposureMs: 30000, Gain: 250, GainKnown: true, Instrument: "ZWO ASI1600MM Pro"}

	tr := FitCurves(frame, ref, fm, rm)

	assert.True(t, tr.MetaSeeded, "sky-limited broadband cannot be measured from sky quantiles")
	assert.InDelta(t, 1.0/3.0, tr.Scale, 1e-9, "the scale is the exposure prediction, NOT the ~0.6 noise ratio")
}

func TestFitCurves_FlatNoMetaIdentity(t *testing.T) {
	tr := FitCurves(flatCurve(10000), flatCurve(10000), Meta{}, Meta{})

	assert.InDelta(t, 1.0, tr.Scale, 1e-9)
	assert.False(t, tr.MetaSeeded, "no prediction to seed from — plain identity, not meta-seeded")
	assert.False(t, tr.Clamped)
}

func TestFitCurves_MetaDisagreeZWOGain(t *testing.T) {
	ref := skyCurve(10000)
	frame := ref // identical signal-bearing curves → measured scale 1

	fm := Meta{ExposureMs: 60000, Gain: 0, GainKnown: true, Instrument: "ZWO ASI1600MM Pro"}
	rm := Meta{ExposureMs: 60000, Gain: 120, GainKnown: true, Instrument: "ZWO ASI1600MM Pro"}
	tr := FitCurves(frame, ref, fm, rm)

	// Same exposure, +120 gain → predicted scale 10^0.6 ≈ 3.98, but measured scale is 1.
	assert.InDelta(t, 1.0, tr.Scale, 1e-9, "the measurement wins over the prediction")
	assert.True(t, tr.MetaDisagree, "measured scale 1 disagrees with the gain-predicted ~4×")
	assert.False(t, tr.MetaSeeded)
}

// CONTRACT CHANGE (task #354): the old TestFitCurves_NonZWOUsesExposureOnly pinned a silent
// exposure-only fallback whenever the gain convention was unconfirmed — exactly the behaviour that
// seeded ×8 (instead of ×0.045) across a g0–g450 five-night merge whose old-ASICAP headers carry no
// INSTRUME card. The contract splits: equal known gains still use the exposure ratio (gain cancels
// under any convention); DIFFERING gains with no confirmed convention now yield NO prediction at all.
func TestFitCurves_NonZWOEqualGainsUseExposureOnly(t *testing.T) {
	ref := skyCurve(10000)
	frame := ref

	fm := Meta{ExposureMs: 60000, Gain: 120, GainKnown: true, Instrument: "Canon EOS Ra"}
	rm := Meta{ExposureMs: 60000, Gain: 120, GainKnown: true, Instrument: "Canon EOS Ra"}
	tr := FitCurves(frame, ref, fm, rm)

	assert.InDelta(t, 1.0, tr.Scale, 1e-9)
	assert.False(t, tr.MetaDisagree, "equal gains cancel ⇒ prediction is the exposure ratio, matching the measurement")
}

func TestFitCurves_NonZWODifferingGainsHaveNoPrediction(t *testing.T) {
	// Flat curves + differing gains on an unconfirmed convention: seeding the exposure ratio would
	// be silently wrong by up to the whole gain range — identity (no seed) is the honest result.
	fm := Meta{ExposureMs: 30000, Gain: 0, GainKnown: true, Instrument: "Canon EOS Ra"}
	rm := Meta{ExposureMs: 60000, Gain: 120, GainKnown: true, Instrument: "Canon EOS Ra"}
	tr := FitCurves(flatCurve(10000), flatCurve(10000), fm, rm)

	assert.InDelta(t, 1.0, tr.Scale, 1e-9, "no honest prediction ⇒ backgrounds matched (×1 here), never the 2× exposure ratio")
	assert.False(t, tr.MetaSeeded)
}

func TestFitCurves_UnknownGainHasNoPrediction(t *testing.T) {
	// A ZWO pair whose gain metadata is absent (GainKnown=false — e.g. catalog rows): the zero
	// value is NOT a real gain 0, so no gain factor and no exposure-only guess either.
	fm := Meta{ExposureMs: 30000, Gain: 0, Instrument: "ZWO ASI1600MM Pro"}
	rm := Meta{ExposureMs: 600000, Gain: 0, Instrument: "ZWO ASI1600MM Pro"}
	tr := FitCurves(flatCurve(10000), flatCurve(10000), fm, rm)

	assert.InDelta(t, 1.0, tr.Scale, 1e-9)
	assert.False(t, tr.MetaSeeded)
}

// CONTRACT CHANGE (task #355 real-run evidence): the earlier bg-overrides-disproven-seed rule was
// removed. It existed for the pre-fix broken seeds (exposure-only ×8); with the gain-law gate fixed
// a confirmed-law seed maps OBJECT flux — the thing a stack must align — while the bg ratio is
// contaminated by moonlit-sky differences and residual pedestals (task #355's Ha groups were
// bg-overridden to ×16–27 where physics said ×0.09). The seed now always stands; a gross bg
// disagreement is FLAGGED (BgDisagree) for the run record, never applied.
func TestFitCurves_BgRatioDisagreementIsFlaggedNotApplied(t *testing.T) {
	frame := flatCurveAt(10000, 0.288) // bright sky (moon / pedestal)
	ref := flatCurveAt(10000, 0.014)   // dim reference sky → bg ratio ≈ 0.049
	fm := Meta{ExposureMs: 15000, Gain: 450, GainKnown: true, Instrument: "ZWO ASI1600MM Pro"}
	rm := Meta{ExposureMs: 120000, Gain: 450, GainKnown: true, Instrument: "ZWO ASI1600MM Pro"}
	// Equal gains ⇒ prediction = exposure ratio ×8; the bg ratio ~0.049 disagrees ~160×.
	tr := FitCurves(frame, ref, fm, rm)

	assert.Equal(t, MethodSeeded, tr.Method, "a confirmed-law seed is never overridden by the bg ratio")
	assert.InDelta(t, 8.0, tr.Scale, 1e-9)
	assert.True(t, tr.BgDisagree, "the gross disagreement is flagged for the run record")
	assert.True(t, tr.MetaSeeded)
}

func TestFitCurves_BgRatioNeedsRealPedestals(t *testing.T) {
	// An over-subtracted (near-zero) sky has no meaningful background ratio: with no prediction
	// either, the pair degrades to identity rather than a garbage ratio.
	frame := flatCurveAt(10000, 0.0002) // pedestal below the 2σ noise floor
	ref := flatCurveAt(10000, 0.05)
	tr := FitCurves(frame, ref, Meta{}, Meta{})

	assert.Equal(t, MethodIdentity, tr.Method)
	assert.InDelta(t, 1.0, tr.Scale, 1e-9)
}

func TestFitCurves_ASICAPCreatorConfirmsGainLaw(t *testing.T) {
	// The task #354 shape: old-ASICAP frames write NO INSTRUME card, only SWCREATE='ASICAP  '.
	// A g450 15s frame mapped onto a g0 120s reference must get the full gain law:
	// (120/15) × 10^((0−450)/200) ≈ 0.0450 — not the bare ×8 exposure ratio.
	fm := Meta{ExposureMs: 15000, Gain: 450, GainKnown: true, Creator: "ASICAP  "}
	rm := Meta{ExposureMs: 120000, Gain: 0, GainKnown: true, Creator: "ASICap"}
	// The g450 15 s sky pedestal is ~22× the g0 120 s one — bg ratio ≈ the seed, cross-check passes.
	tr := FitCurves(flatCurveAt(10000, 0.45), flatCurveAt(10000, 0.0203), fm, rm)

	assert.True(t, tr.MetaSeeded)
	assert.InDelta(t, 8*math.Pow(10, -450.0/200.0), tr.Scale, 1e-6)
}

func TestFitCurves_MixedInstrumentAndCreatorConfirmGainLaw(t *testing.T) {
	// A newer capture with INSTRUME paired against an old-ASICAP one with only SWCREATE: both sides
	// confirm the ZWO law through different evidence, so the gain factor applies.
	fm := Meta{ExposureMs: 90000, Gain: 110, GainKnown: true, Instrument: "ZWO ASI1600MM Pro"}
	rm := Meta{ExposureMs: 120000, Gain: 0, GainKnown: true, Creator: "ASICAP"}
	tr := FitCurves(flatCurveAt(10000, 0.08), flatCurveAt(10000, 0.03), fm, rm)

	assert.True(t, tr.MetaSeeded)
	assert.InDelta(t, (120.0/90.0)*math.Pow(10, -110.0/200.0), tr.Scale, 1e-6)
}
