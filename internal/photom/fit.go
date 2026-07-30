package photom

import (
	"math"
	"sort"
	"strings"
)

const (
	// fitLoIdx..fitHiIdx select the CurveQ probes used in the affine fit: P5..P97.5. Indices 12,13
	// (P99, P99.5) are excluded because they track star saturation, not the shared photometric scale.
	fitLoIdx = 0
	fitHiIdx = 11

	// Absolute clamp bounds — used only when no metadata prior exists. Deliberately wide: real
	// cross-gain ratios are large (ASI1600 g0↔g300 ≈ 31.6×, g250↔g400 ≈ 5.6×), and the old 5.0
	// ceiling sat BELOW genuine ratios, so healthy cross-gain measurements were systematically
	// clamped wrong ("Ha clamped at 5×").
	scaleMin = 0.1
	scaleMax = 10.0
	tiny     = 1e-9

	// metaLo..metaHi bound the accepted ratio of measured scale to the exposure/gain-predicted scale.
	metaLo = 0.67
	metaHi = 1.5
	// gainDecades scales the ZWO/ASI gain (in 0.1 dB units) into a linear-flux multiplier: 10^(g/200).
	gainDecades = 200.0

	// bgFloorSigmas requires a sky background to sit meaningfully above its own noise before the
	// background-ratio rung trusts it.
	bgFloorSigmas = 2.0
	// seedDisagreeK bounds the accepted seed-vs-background-ratio disagreement on flat curves: beyond
	// it the measured background ratio replaces the header prediction (task #354's wrong seeds were
	// off by ~178×; night-to-night sky brightness only moves the background ratio a few ×).
	seedDisagreeK = 3.0

	// flatSpanSigmas marks a curve as FLAT (no measurable signal) when its P5..P97.5 span is under
	// this many noise sigmas. Pure Gaussian noise already spans (1.96+1.64)σ ≈ 3.6σ, and mapping one
	// noise distribution's quantiles onto another's is a perfectly self-consistent affine whose slope
	// is the NOISE-WIDTH ratio, not the flux ratio — the exact mis-measurement that broke real
	// narrowband (sky-pedestal) groups. 5σ sits safely above the noise floor while any genuine
	// signal tail (stars/nebulosity) pushes the span far beyond it.
	flatSpanSigmas = 5.0
)

// Meta carries the capture parameters used by the metadata prior. Its zero value disables the prior.
type Meta struct {
	ExposureMs, Gain int64
	// GainKnown distinguishes real gain metadata (including a genuine gain of 0 — a legitimate ZWO
	// setting) from the int zero-value of frames that carry no gain at all. Without it a five-night
	// g0–g450 merge seeded pure exposure ratios (task #354: ×8 where the true flux factor was 0.045).
	GainKnown  bool
	Instrument string
	// Creator is the capture software (SWCREATE). Old ASICAP writes NO INSTRUME card, so it is the
	// only in-header evidence that the ZWO 0.1 dB gain law applies to Gain.
	Creator string
}

// Method names how a Transform's scale was established — the ladder position, recorded per group
// so a run's record says WHICH evidence set each night's flux scale.
const (
	MethodMeasured   = "measured"    // Theil–Sen fit over signal-bearing quantile curves
	MethodSeeded     = "seeded"      // header exposure/gain prediction (flat curves)
	MethodBgMatched  = "bg-matched"  // measured sky-background ratio (no trustworthy prediction, or the prediction was disproven)
	MethodOffsetOnly = "offset-only" // background offset alone (the no-clip degrade of last resort)
	MethodIdentity   = "identity"    // nothing usable — group left on its own scale
)

// Transform is the affine map ref ≈ Scale*frame + Offset recovered by FitCurves, plus diagnostic flags.
type Transform struct {
	Scale, Offset, Resid float64
	// Method records which ladder rung produced Scale (see the Method* constants).
	Method string
	// Clamped: the measured scale fell outside the sane absolute bounds and was clamped.
	// MetaDisagree: the measured scale is far from the exposure/gain prediction (measurement kept).
	// MetaSeeded: the curves were too flat to measure (narrowband/pedestal) — the scale IS the
	// exposure/gain prediction, not a measurement.
	Clamped, MetaDisagree, MetaSeeded bool
	// BgDisagree: the measured sky-background ratio grossly contradicts a kept confirmed-law seed —
	// a pedestal/darks or sky-brightness tell worth a note, never an override (see flatScale).
	BgDisagree bool
}

// FitCurves recovers a robust affine transform mapping frame's curve onto ref's curve: ref ≈
// Scale*frame + Offset. Scale is a Theil–Sen slope (median of pairwise slopes) over the P5..P97.5
// probes; Offset is the median residual level; Resid is the median absolute misfit normalised by the
// reference's signal span. A FLAT curve on either side (span ≈ noise — a narrowband sky pedestal)
// cannot be measured: its quantile map yields the noise-width ratio, not the flux ratio — there the
// scale is SEEDED from the exposure/gain prediction instead (MetaSeeded), falling back to identity
// with no metadata. A real measurement is clamped only to the wide absolute [0.1, 10] bounds; when
// both metas carry an exposure it is also compared against the prediction and MetaDisagree flagged on
// a large mismatch — the measured Scale/Offset always win regardless.
func FitCurves(frame, ref FrameCurve, fm, rm Meta) Transform {
	expected := metaExpectedScale(fm, rm)
	raw, method := 1.0, MethodMeasured
	bgDisagree := false
	switch {
	case curveFlat(frame) || curveFlat(ref):
		raw, method = flatScale(frame, ref, expected)
		if bg, ok := bgScale(frame, ref); ok && method == MethodSeeded &&
			(raw > seedDisagreeK*bg || bg > seedDisagreeK*raw) {
			bgDisagree = true
		}
	default:
		raw = theilSenSlope(frame, ref)
	}
	offset := medianOffset(frame, ref, raw)
	resid := fitResidual(frame, ref, raw, offset)
	disagree := metaDisagrees(raw, expected)

	// The absolute clamp bounds MIS-measurements of the quantile fit; a header seed or a measured
	// background ratio is not that kind of measurement, and legitimate cross-config ratios exceed
	// the bounds (30 s vs 600 s = 20×) — clamping them would re-break the case they exist for.
	scale, clamped := raw, false
	if method == MethodMeasured {
		scale, clamped = clampScale(raw)
	}
	return Transform{
		Scale:        scale,
		Offset:       offset,
		Resid:        resid,
		Method:       method,
		Clamped:      clamped,
		MetaDisagree: disagree,
		MetaSeeded:   method == MethodSeeded,
		BgDisagree:   bgDisagree,
	}
}

// flatScale resolves the scale for an unmeasurable (flat) curve pair. A header prediction ALWAYS
// leads when one exists: it comes from a confirmed gain convention (metaExpectedScale returns 0
// otherwise) and maps OBJECT flux — the thing a stack must align — while the measured sky-
// background ratio is contaminated by night-to-night sky brightness (moon) and residual pedestals
// (missing darks), which the stack's addscale absorbs additively anyway. Task #355 evidence: Ha
// groups whose bg ratio (×16–27) overrode a correct confirmed-law seed (×0.09) were mis-scaled
// ~180×; the override protected against the pre-fix broken seeds, which no longer exist. With no
// prediction the background ratio stands alone (order-of-magnitude-correct); with neither, identity.
func flatScale(frame, ref FrameCurve, expected float64) (float64, string) {
	if expected > 0 {
		return expected, MethodSeeded
	}
	if bg, ok := bgScale(frame, ref); ok {
		return bg, MethodBgMatched
	}
	return 1, MethodIdentity
}

// bgScale is the measured sky-background ratio ref/frame — the flat-curve rung used when no header
// prediction exists and the cross-check that catches a wrong one. ok is false when either
// background sits too close to zero or its own noise to be a meaningful pedestal (over-subtracted
// skies would produce a garbage ratio).
func bgScale(frame, ref FrameCurve) (float64, bool) {
	if frame.Bg <= tiny || frame.Bg < bgFloorSigmas*frame.Noise {
		return 0, false
	}
	if ref.Bg <= tiny || ref.Bg < bgFloorSigmas*ref.Noise {
		return 0, false
	}
	return ref.Bg / frame.Bg, true
}

// curveFlat reports whether a curve's fit span is indistinguishable from its own noise — nothing but
// sky/pedestal in the probes, so a slope over it would measure noise widths, not photometric flux.
// Deliberately NOT relaxed by a star-tail (P99+) test: a star-sparse broadband frame has a real star
// tail yet pure-sky FIT probes — un-gating it would measure the noise-width ratio (≈ √ of the flux
// ratio for sky-limited frames), a confidently-wrong slope no residual check can catch, because the
// quantile-onto-quantile map of two Gaussians is perfectly affine. Header seeding is the honest
// answer for such frames (task #312's two-night L/R: seeded 1/3 was exactly right).
func curveFlat(c FrameCurve) bool {
	return c.Q[fitHiIdx]-c.Q[fitLoIdx] < flatSpanSigmas*c.Noise
}

// metaExpectedScale is the exposure/gain-predicted flux ratio ref/frame. The exposure ratio always
// contributes; the gain factor additionally needs BOTH gains known AND the ZWO 0.1 dB convention
// confirmed on both sides (INSTRUME, or the ASICAP capture software — old ASICAP writes no INSTRUME
// card at all, which silently dropped the factor across a g0–g450 five-night merge, task #354).
// Known-and-EQUAL gains cancel under any convention, so the exposure ratio alone is exact there.
// Differing or unknown gains with no confirmed convention have NO honest prediction — 0, never the
// silently-wrong exposure-only ratio (the flat-curve caller then falls back to the background-
// matched rung or identity instead).
func metaExpectedScale(fm, rm Meta) float64 {
	if fm.ExposureMs <= 0 || rm.ExposureMs <= 0 {
		return 0
	}
	expected := float64(rm.ExposureMs) / float64(fm.ExposureMs)
	gainsKnown := fm.GainKnown && rm.GainKnown
	switch {
	case gainsKnown && zwoGainLaw(fm) && zwoGainLaw(rm):
		expected *= gLin(rm.Gain) / gLin(fm.Gain)
	case gainsKnown && fm.Gain == rm.Gain:
		// equal gains cancel whatever the law — keep the exposure ratio
	default:
		return 0
	}
	if expected <= 0 || math.IsNaN(expected) || math.IsInf(expected, 0) {
		return 0
	}
	return expected
}

// zwoGainLaw reports whether a group's metadata confirms the ZWO 0.1 dB gain convention: an
// explicit ZWO/ASI instrument name, or the ASICAP capture software (which only drives ZWO cameras
// and, in old versions, writes no INSTRUME card).
func zwoGainLaw(m Meta) bool {
	if isZWO(m.Instrument) {
		return true
	}
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(m.Creator)), "ASICAP")
}

// theilSenSlope returns the median of the pairwise slopes (ref[j]-ref[i])/(frame[j]-frame[i]) across
// the fit probes, skipping pairs whose frame gap is numerically flat. A flat frame yields 1 (identity).
func theilSenSlope(frame, ref FrameCurve) float64 {
	var slopes []float64
	for i := fitLoIdx; i <= fitHiIdx; i++ {
		for j := i + 1; j <= fitHiIdx; j++ {
			d := frame.Q[j] - frame.Q[i]
			if math.Abs(d) <= tiny {
				continue
			}
			slopes = append(slopes, (ref.Q[j]-ref.Q[i])/d)
		}
	}
	if len(slopes) == 0 {
		return 1
	}
	return medianFloat(slopes)
}

// medianOffset returns the median over the fit probes of (ref - scale*frame).
func medianOffset(frame, ref FrameCurve, scale float64) float64 {
	offs := make([]float64, 0, fitHiIdx-fitLoIdx+1)
	for k := fitLoIdx; k <= fitHiIdx; k++ {
		offs = append(offs, ref.Q[k]-scale*frame.Q[k])
	}
	return medianFloat(offs)
}

// fitResidual returns the median absolute misfit over the fit probes, normalised by the reference
// signal span (P97.5 minus background) so it reads as a fraction independent of the frame's scale.
func fitResidual(frame, ref FrameCurve, scale, offset float64) float64 {
	denom := math.Max(tiny, ref.Q[fitHiIdx]-ref.Bg)
	res := make([]float64, 0, fitHiIdx-fitLoIdx+1)
	for k := fitLoIdx; k <= fitHiIdx; k++ {
		res = append(res, math.Abs(ref.Q[k]-(scale*frame.Q[k]+offset))/denom)
	}
	return medianFloat(res)
}

// clampScale confines scale to [scaleMin, scaleMax], reporting whether it was out of range.
func clampScale(scale float64) (float64, bool) {
	if scale < scaleMin {
		return scaleMin, true
	}
	if scale > scaleMax {
		return scaleMax, true
	}
	return scale, false
}

// metaDisagrees reports whether the measured scale is far from the exposure/gain-predicted scale
// (see metaExpectedScale). Inert when no prediction exists (expected 0).
func metaDisagrees(scale, expected float64) bool {
	if expected <= 0 {
		return false
	}
	ratio := scale / expected
	return ratio < metaLo || ratio > metaHi
}

// gLin converts a ZWO/ASI gain (0.1 dB units) into a linear-flux multiplier.
func gLin(gain int64) float64 {
	return math.Pow(10, float64(gain)/gainDecades)
}

// isZWO reports whether an instrument name denotes a ZWO/ASI camera whose gain follows the 0.1 dB law.
func isZWO(instrument string) bool {
	s := strings.ToUpper(strings.TrimSpace(instrument))
	return strings.HasPrefix(s, "ZWO") || strings.HasPrefix(s, "ASI")
}

// medianFloat returns the median of vals (average of the two middle elements for an even count). It
// copies before sorting and never mutates the input. An empty slice returns 0.
func medianFloat(vals []float64) float64 {
	n := len(vals)
	if n == 0 {
		return 0
	}
	buf := make([]float64, n)
	copy(buf, vals)
	sort.Float64s(buf)
	if n%2 == 1 {
		return buf[n/2]
	}
	return 0.5 * (buf[n/2-1] + buf[n/2])
}
