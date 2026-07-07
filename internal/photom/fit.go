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

	scaleMin = 0.2
	scaleMax = 5.0
	tiny     = 1e-9

	// metaLo..metaHi bound the accepted ratio of measured scale to the exposure/gain-predicted scale.
	metaLo = 0.67
	metaHi = 1.5
	// gainDecades scales the ZWO/ASI gain (in 0.1 dB units) into a linear-flux multiplier: 10^(g/200).
	gainDecades = 200.0
)

// Meta carries the capture parameters used by the metadata prior. Its zero value disables the prior.
type Meta struct {
	ExposureMs, Gain int64
	Instrument       string
}

// Transform is the affine map ref ≈ Scale*frame + Offset recovered by FitCurves, plus diagnostic flags.
type Transform struct {
	Scale, Offset, Resid  float64
	Clamped, MetaDisagree bool
}

// FitCurves recovers a robust affine transform mapping frame's curve onto ref's curve: ref ≈
// Scale*frame + Offset. Scale is a Theil–Sen slope (median of pairwise slopes) over the P5..P97.5
// probes; Offset is the median residual level; Resid is the median absolute misfit normalised by the
// reference's signal span. Scale is clamped to [0.2, 5.0]. When both metas carry an exposure the fit is
// compared against an exposure/gain prediction and MetaDisagree is flagged on a large mismatch — the
// measured Scale/Offset always win regardless.
func FitCurves(frame, ref FrameCurve, fm, rm Meta) Transform {
	raw := theilSenSlope(frame, ref)
	offset := medianOffset(frame, ref, raw)
	resid := fitResidual(frame, ref, raw, offset)
	disagree := metaDisagrees(raw, fm, rm)

	scale, clamped := clampScale(raw)
	return Transform{
		Scale:        scale,
		Offset:       offset,
		Resid:        resid,
		Clamped:      clamped,
		MetaDisagree: disagree,
	}
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

// metaDisagrees reports whether the measured scale is far from the exposure/gain-predicted scale. The
// gain term applies only when BOTH instruments are ZWO/ASI; otherwise only the exposure ratio is used.
// It is inert unless both metas carry a positive exposure.
func metaDisagrees(scale float64, fm, rm Meta) bool {
	if fm.ExposureMs <= 0 || rm.ExposureMs <= 0 {
		return false
	}
	expected := float64(rm.ExposureMs) / float64(fm.ExposureMs)
	if isZWO(fm.Instrument) && isZWO(rm.Instrument) {
		expected *= gLin(rm.Gain) / gLin(fm.Gain)
	}
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
