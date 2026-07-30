package mosaic

// Pair-level photometric fitting: from the paired overlap samples (overlap.go), fit
// v_B ≈ Gain·v_A + Offset robustly. Quantile matching needs no exact pixel pairing, so residual
// registration error cannot bias the fit.

import "math"

// gainMin/gainMax clamp a pair gain: same-rig panels never legitimately drift beyond 2×.
const (
	gainMin = 0.5
	gainMax = 2.0
)

// matchQ are the probe percentiles of the P10–P80 quantile-matching band (2.5% pitch — a dense
// curve so the least-squares line extracts most of the paired information the overlap carries).
var matchQ = quantileProbes(10, 80, 2.5)

func quantileProbes(lo, hi, step float64) []float64 {
	var out []float64
	for p := lo; p <= hi+1e-9; p += step {
		out = append(out, p)
	}
	return out
}

// fitPair fits v_B ≈ Gain·v_A + Offset over one overlap: quantile matching in the P10–P80 band
// for the gain, a band-median for the offset, plus a contamination guard — when the sky-band
// (P10–P30) and signal-band (P50–P70) offsets AT THE FITTED GAIN disagree by more than 3× the
// sky-band residual sigma, the overlap holds structure the affine model does not explain (a
// nebula filling the seam), and the pair degrades to offset-only: gain pinned to 1, offset
// re-measured on the sky band alone.
func fitPair(a, b int, va, vb []float32, gainAllowed bool) PairFit {
	sa := sortedF64(va)
	sb := sortedF64(vb)
	skyOff, skySig, _ := bandOffset(va, vb, sa, 1, 10, 30)
	degraded := PairFit{A: a, B: b, Samples: len(va), Gain: 1, Offset: skyOff, ResidualSigma: skySig, OffsetOnly: true}
	if !gainAllowed {
		return degraded
	}
	gain, ok := quantileGain(sa, sb)
	if !ok {
		return degraded
	}
	gain = math.Min(math.Max(gain, gainMin), gainMax)
	o20, s20, _ := bandOffset(va, vb, sa, gain, 10, 30)
	o60, _, _ := bandOffset(va, vb, sa, gain, 50, 70)
	if math.Abs(o60-o20) > 3*s20 {
		return degraded
	}
	off, sig, _ := bandOffset(va, vb, sa, gain, 10, 80)
	return PairFit{A: a, B: b, Samples: len(va), Gain: gain, Offset: off, ResidualSigma: sig}
}

// fitPairOffset re-measures only the offset of a pair at a fixed gain (the per-channel offset
// refit: gains are shared across channels, sky pedestals are not).
func fitPairOffset(a, b int, va, vb []float32, gain float64) PairFit {
	sa := sortedF64(va)
	off, sig, _ := bandOffset(va, vb, sa, gain, 10, 80)
	return PairFit{A: a, B: b, Samples: len(va), Gain: gain, Offset: off, ResidualSigma: sig}
}

// quantileGain estimates the gain by quantile matching: the least-squares slope of the
// quantile-quantile curve q_B(p) vs q_A(p) over the P10–P80 band. Matching marginal quantiles
// needs no exact pixel pairing (registration-error tolerant), and the dense probe grid keeps the
// estimator's variance near the paired information limit — the sparse Theil–Sen variant lost a
// factor of a few, which visibly biased small corner overlaps. Curvature from non-affine content
// is caught downstream by fitPair's contamination guard. ok=false when the band has no dynamic
// range (flat sky — gain and offset are degenerate there).
func quantileGain(sortedA, sortedB []float64) (float64, bool) {
	n := float64(len(matchQ))
	var ma, mb float64
	qa := make([]float64, len(matchQ))
	qb := make([]float64, len(matchQ))
	for i, p := range matchQ {
		qa[i] = quantileSorted(sortedA, p)
		qb[i] = quantileSorted(sortedB, p)
		ma += qa[i]
		mb += qb[i]
	}
	ma /= n
	mb /= n
	var varA, cov float64
	for i := range qa {
		da, db := qa[i]-ma, qb[i]-mb
		varA += da * da
		cov += da * db
	}
	if varA <= 0 {
		return 0, false
	}
	return cov / varA, true
}

// bandOffset measures v_B − gain·v_A over the samples whose v_A lies in the [pLo,pHi] percentile
// band of A: robust median offset + MAD-derived sigma.
func bandOffset(va, vb []float32, sortedA []float64, gain, pLo, pHi float64) (offset, sigma float64, n int) {
	lo := quantileSorted(sortedA, pLo)
	hi := quantileSorted(sortedA, pHi)
	diffs := make([]float64, 0, len(va)/2)
	for k, v := range va {
		fv := float64(v)
		if fv < lo || fv > hi {
			continue
		}
		diffs = append(diffs, float64(vb[k])-gain*fv)
	}
	if len(diffs) == 0 {
		return 0, 0, 0
	}
	med := medianF64(diffs)
	return med, madSigmaF64(diffs, med), len(diffs)
}
