// Package stacknative combines registered frames into a master in Go, for the algorithms Siril's own
// `stack` does not implement (trimmed mean, Robust Chauvenet rejection, DSS's auto-adaptive and
// entropy-weighted averages, local normalization) and, for the ones it does, as a second opinion.
//
// It runs over the frames Siril has ALREADY registered, so registration, drizzle and interpolation
// stay Siril's job and only the pixel combination changes hands. Memory is bounded by streaming
// row bands (internal/fits ReadPlaneBand) rather than holding N full frames: 60 ASI1600 frames at a
// 64-row band is ~70 MB in flight, against 3.9 GB for the whole sequence.
package stacknative

import (
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/stackalg"
)

const (
	// maxClipIters bounds every iterative rejection so a pathological pixel can never spin.
	maxClipIters = 12
	// winsorTolerance is the convergence threshold of the winsorized sigma iteration.
	winsorTolerance = 5e-4
	// adaptiveIters is how many reweighting passes the auto-adaptive average runs.
	adaptiveIters = 8
)

// combinePixel reduces one pixel's samples to a single value: it applies the rejection test, then
// the combination method, honouring the per-frame weights.
//
// v holds the pixel's value in each contributing frame IN SEQUENCE ORDER (linear-fit clipping reads
// that order as its x axis). w holds each frame's weight, or nil for an unweighted stack. detail,
// when non-nil, holds each frame's local detail energy at this pixel — the entropy-weighted
// average's input.
func combinePixel(o stackalg.Options, v, w, detail []float64, s *scratch) float64 {
	out, _ := combinePixelCounted(o, v, w, detail, s)
	return out
}

// combinePixelCounted is combinePixel plus the number of samples that survived rejection, so the
// caller can report the rejected fraction without paying for a second rejection pass. kept is -1 for
// the weighted averages, which reject nothing by design.
func combinePixelCounted(o stackalg.Options, v, w, detail []float64, s *scratch) (value float64, kept int) {
	if len(v) == 0 {
		return 0, 0
	}
	if len(v) == 1 {
		return v[0], 1
	}
	switch o.Reject {
	case stackalg.RejectAdaptiveWeighted:
		return adaptiveWeightedMean(v, w, s), -1
	case stackalg.RejectEntropyWeighted:
		return entropyWeightedMean(v, w, detail, s), -1
	}
	keep := rejectionMask(o, v, s)
	kept = 0
	for _, k := range keep {
		if k {
			kept++
		}
	}
	return reduce(o, v, w, keep, s), kept
}

// rejectionMask applies the chosen outlier test. A combination method that takes no rejection
// (sum/min/max/median) keeps everything, exactly as Siril does.
//
// The returned mask is the scratch's shared buffer, valid only until the next call — see resetKeep.
func rejectionMask(o stackalg.Options, v []float64, s *scratch) []bool {
	if info, ok := stackalg.CombineOf(o.Combine); ok && !info.Rejects {
		return s.resetKeep(len(v))
	}
	switch o.Reject {
	case stackalg.RejectNone:
		return rejectNone(v, s)
	case stackalg.RejectPercentile:
		return rejectPercentile(v, o.Low, o.High, s)
	case stackalg.RejectSigma:
		return rejectSigma(v, o.Low, o.High, s)
	case stackalg.RejectMedianSigma:
		return rejectMedianSigma(v, o.Low, o.High, s)
	case stackalg.RejectLinearFit:
		return rejectLinearFit(v, o.Low, o.High, s)
	case stackalg.RejectGESD:
		return rejectGESD(v, o.Low, o.High, s)
	case stackalg.RejectMAD:
		return rejectMAD(v, o.Low, o.High, s)
	case stackalg.RejectRCR:
		return rejectRCR(v, o.Low, s)
	default: // RejectWinsorized and anything unresolved
		return rejectWinsorized(v, o.Low, o.High, s)
	}
}

// reduce applies the combination method to the surviving samples.
func reduce(o stackalg.Options, v, w []float64, keep []bool, s *scratch) float64 {
	switch o.Combine {
	case stackalg.CombineMedian:
		return keptMedian(v, keep, s)
	case stackalg.CombineSum:
		return keptSum(v, keep)
	case stackalg.CombineMin:
		return keptExtreme(v, keep, true)
	case stackalg.CombineMax:
		return keptExtreme(v, keep, false)
	case stackalg.CombineTrimmedMean:
		return trimmedMean(v, keep, o.TrimFrac, s)
	default: // CombineMean (and the auto/empty value)
		return keptWeightedMean(v, w, keep)
	}
}

// keptWeightedMean averages the survivors, weighted per frame. With no survivors it falls back to
// the plain mean of every sample rather than emitting a hole.
func keptWeightedMean(v, w []float64, keep []bool) float64 {
	var sum, wsum float64
	for i, x := range v {
		if !keep[i] {
			continue
		}
		wi := 1.0
		if w != nil {
			wi = w[i]
		}
		sum += wi * x
		wsum += wi
	}
	if wsum > 0 {
		return sum / wsum
	}
	return plainMean(v)
}

func plainMean(v []float64) float64 {
	var sum float64
	for _, x := range v {
		sum += x
	}
	return sum / float64(len(v))
}

func keptMedian(v []float64, keep []bool, s *scratch) float64 {
	s.work = s.work[:0]
	for i, x := range v {
		if keep[i] {
			s.work = append(s.work, x)
		}
	}
	if len(s.work) == 0 {
		return plainMean(v)
	}
	kept := append([]float64(nil), s.work...)
	return median(kept, s)
}

func keptSum(v []float64, keep []bool) float64 {
	var sum float64
	for i, x := range v {
		if keep[i] {
			sum += x
		}
	}
	return sum
}

// keptExtreme returns the darkest (min) or brightest (max) survivor.
func keptExtreme(v []float64, keep []bool, wantMin bool) float64 {
	out, seen := 0.0, false
	for i, x := range v {
		if !keep[i] {
			continue
		}
		if !seen || (wantMin && x < out) || (!wantMin && x > out) {
			out, seen = x, true
		}
	}
	if !seen {
		return plainMean(v)
	}
	return out
}

// trimmedMean sorts the survivors, discards frac of them at EACH end, and averages the rest — a
// blunt but completely predictable robust average with no distributional assumption at all.
func trimmedMean(v []float64, keep []bool, frac float64, s *scratch) float64 {
	s.work = s.work[:0]
	for i, x := range v {
		if keep[i] {
			s.work = append(s.work, x)
		}
	}
	n := len(s.work)
	if n == 0 {
		return plainMean(v)
	}
	kept := append([]float64(nil), s.work...)
	sortFloats(kept)
	cut := int(frac * float64(n))
	if 2*cut >= n {
		cut = (n - 1) / 2
	}
	kept = kept[cut : n-cut]
	var sum float64
	for _, x := range kept {
		sum += x
	}
	return sum / float64(len(kept))
}

// adaptiveWeightedMean is DeepSkyStacker's auto-adaptive weighted average: rather than a hard
// keep-or-drop decision it iterates a weight per sample until the weights converge, so a marginal
// sample fades out instead of disappearing at a threshold. No threshold means no threshold artefacts.
func adaptiveWeightedMean(v, w []float64, s *scratch) float64 {
	m := median(append(s.work[:0], v...), s)
	sigma := mad(v, m, s)
	if sigma <= 0 {
		return keptWeightedMean(v, w, s.resetKeep(len(v)))
	}
	centre := m
	for iter := 0; iter < adaptiveIters; iter++ {
		var sum, wsum float64
		for i, x := range v {
			r := (x - centre) / sigma
			wi := 1 / (1 + r*r) // Cauchy weight: falls off smoothly, never reaches zero
			if w != nil {
				wi *= w[i]
			}
			sum += wi * x
			wsum += wi
		}
		if wsum <= 0 {
			return centre
		}
		next := sum / wsum
		if math.Abs(next-centre) < 1e-9*math.Max(1, math.Abs(centre)) {
			return next
		}
		centre = next
	}
	return centre
}

// entropyWeightedMean weights each frame's contribution by how much local information it carries at
// this pixel, favouring the frames that actually resolve detail there. detail is the per-frame local
// detail energy measured on the band; with none available it degrades to a plain weighted mean.
func entropyWeightedMean(v, w, detail []float64, s *scratch) float64 {
	if detail == nil {
		return keptWeightedMean(v, w, s.resetKeep(len(v)))
	}
	var maxD float64
	for _, d := range detail {
		if d > maxD {
			maxD = d
		}
	}
	if maxD <= 0 {
		return keptWeightedMean(v, w, s.resetKeep(len(v)))
	}
	var sum, wsum float64
	for i, x := range v {
		// A floor keeps a featureless frame contributing to the noise average rather than vanishing.
		wi := math.Max(detail[i]/maxD, entropyWeightFloor)
		if w != nil {
			wi *= w[i]
		}
		sum += wi * x
		wsum += wi
	}
	if wsum <= 0 {
		return plainMean(v)
	}
	return sum / wsum
}

// entropyWeightFloor keeps the least-detailed frame contributing rather than being dropped outright.
const entropyWeightFloor = 0.15

// sortFloats is sort.Float64s, kept local so the hot loop needs no interface dispatch.
func sortFloats(v []float64) { sort.Float64s(v) }
