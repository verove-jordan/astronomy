package stacknative

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/stackalg"
)

// clean is a tight population around 1.0 — the "sky" every test plants outliers into.
func clean(n int) []float64 {
	v := make([]float64, n)
	for i := range v {
		// A deterministic, slightly asymmetric spread, so nothing depends on a lucky symmetry.
		v[i] = 1.0 + 0.01*math.Sin(float64(i)*1.7)
	}
	return v
}

func resolved(reject stackalg.Reject, frames int) stackalg.Options {
	o := stackalg.DefaultLights()
	o.Reject = reject
	return stackalg.Resolve(o, frames)
}

// TestRejection_RemovesAPlantedOutlier is the contract every algorithm must meet: a single bright
// sample (a satellite trail crossing one sub) must not survive into the master.
func TestRejection_RemovesAPlantedOutlier(t *testing.T) {
	algorithms := []stackalg.Reject{
		stackalg.RejectPercentile,
		stackalg.RejectSigma,
		stackalg.RejectMedianSigma,
		stackalg.RejectWinsorized,
		stackalg.RejectLinearFit,
		stackalg.RejectGESD,
		stackalg.RejectMAD,
		stackalg.RejectRCR,
	}
	for _, algo := range algorithms {
		t.Run(string(algo), func(t *testing.T) {
			const n = 30
			v := clean(n)
			want := 0.0
			for _, x := range v {
				want += x
			}
			want /= n
			v[7] = 5.0 // one frame, one very bright pixel

			s := newScratch(n)
			got := combinePixel(resolved(algo, n), v, nil, nil, s)
			assert.InDelta(t, want, got, 0.02,
				"the outlier must be rejected, not averaged in (unrejected mean would be ~%.2f)",
				(want*float64(n-1)+5.0)/n)
		})
	}
}

// TestRejection_KeepsCleanData: on a population with no outliers the algorithms must not eat the
// signal — over-rejection costs depth just as surely as under-rejection costs cleanliness.
func TestRejection_KeepsCleanData(t *testing.T) {
	algorithms := []stackalg.Reject{
		stackalg.RejectNone, stackalg.RejectSigma, stackalg.RejectMedianSigma,
		stackalg.RejectWinsorized, stackalg.RejectGESD, stackalg.RejectMAD, stackalg.RejectRCR,
	}
	for _, algo := range algorithms {
		t.Run(string(algo), func(t *testing.T) {
			const n = 40
			v := clean(n)
			s := newScratch(n)
			keep := rejectionMask(resolved(algo, n), v, s)
			kept := 0
			for _, k := range keep {
				if k {
					kept++
				}
			}
			assert.GreaterOrEqual(t, kept, n-2, "%s rejected %d of %d clean samples", algo, n-kept, n)
		})
	}
}

// TestLinearFit_ModelsAMovingSky is the reason linear-fit clipping exists. When the sky level RISES
// through the session (a moon coming up), the trend itself inflates the spread a centre-and-spread
// test measures — so plain sigma clipping MISSES moderate outliers that are obvious once the trend
// is removed. The linear fit models the ramp and judges each sample against the line.
func TestLinearFit_ModelsAMovingSky(t *testing.T) {
	const n = 40
	ramp := make([]float64, n)
	for i := range ramp {
		ramp[i] = 1.0 + 0.02*float64(i) // the sky level nearly doubles across the session
	}
	// A moderate contaminant: well off the trend line, but well inside ±3σ of the whole ramp.
	ramp[13] += 0.35

	// rejectionMask returns the scratch's shared buffer, so each result must be copied before the
	// next call overwrites it.
	mask := func(algo stackalg.Reject) []bool {
		return append([]bool(nil), rejectionMask(resolved(algo, n), ramp, newScratch(n))...)
	}
	lin := mask(stackalg.RejectLinearFit)
	sig := mask(stackalg.RejectSigma)

	assert.False(t, lin[13], "the linear fit must catch an outlier that is off the TREND")
	assert.True(t, sig[13], "plain sigma clipping misses it — the ramp inflated the sigma it is judged against")

	countKept := func(keep []bool) int {
		k := 0
		for _, b := range keep {
			if b {
				k++
			}
		}
		return k
	}
	assert.GreaterOrEqual(t, countKept(lin), n-3, "and it must reject little else")
}

// TestLinearFit_RejectsAHardOutlierToo: modelling a trend must not cost it the obvious catches.
func TestLinearFit_RejectsAHardOutlier(t *testing.T) {
	const n = 40
	ramp := make([]float64, n)
	for i := range ramp {
		ramp[i] = 1.0 + 0.02*float64(i)
	}
	ramp[13] = 9.0
	keep := rejectionMask(resolved(stackalg.RejectLinearFit, n), ramp, newScratch(n))
	assert.False(t, keep[13])
}

func TestCombine_Methods(t *testing.T) {
	v := []float64{1, 2, 3, 4, 100}
	s := newScratch(len(v))
	base := stackalg.DefaultLights()
	base.Reject = stackalg.RejectNone

	tests := []struct {
		name    string
		combine stackalg.Combine
		want    float64
		delta   float64
	}{
		{"mean averages everything", stackalg.CombineMean, 22, 1e-9},
		{"median ignores the outlier", stackalg.CombineMedian, 3, 1e-9},
		{"sum adds", stackalg.CombineSum, 110, 1e-9},
		{"min takes the darkest", stackalg.CombineMin, 1, 1e-9},
		{"max takes the brightest", stackalg.CombineMax, 100, 1e-9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := base
			o.Combine = tt.combine
			assert.InDelta(t, tt.want, combinePixel(o, v, nil, nil, s), tt.delta)
		})
	}
}

func TestTrimmedMean_DropsBothEnds(t *testing.T) {
	// 0 and 100 are the planted extremes; trimming 20% of 10 samples drops one at each end.
	v := []float64{0, 2, 3, 4, 5, 5, 6, 7, 8, 100}
	o := stackalg.DefaultLights()
	o.Combine, o.Reject, o.TrimFrac = stackalg.CombineTrimmedMean, stackalg.RejectNone, 0.1
	got := combinePixel(o, v, nil, nil, newScratch(len(v)))
	assert.InDelta(t, 5.0, got, 1e-9, "both extremes must be gone")

	// A trim so wide it would empty the set collapses to the median rather than dividing by zero.
	o.TrimFrac = 0.45
	assert.InDelta(t, 5.0, combinePixel(o, v, nil, nil, newScratch(len(v))), 0.5)
}

func TestWeights_FavourTheChosenFrames(t *testing.T) {
	v := []float64{1, 1, 1, 3}
	w := []float64{1, 1, 1, 9} // the last frame counts nine times
	o := stackalg.DefaultLights()
	o.Reject = stackalg.RejectNone
	got := combinePixel(o, v, w, nil, newScratch(len(v)))
	assert.InDelta(t, (1+1+1+27)/12.0, got, 1e-9)
}

// TestAdaptiveWeighted_FadesOutliersRatherThanCutting: the value must land near the clean population
// without the step change a hard threshold produces as an outlier crosses it.
func TestAdaptiveWeighted_FadesOutliers(t *testing.T) {
	const n = 30
	s := newScratch(n)
	o := resolved(stackalg.RejectAdaptiveWeighted, n)

	v := clean(n)
	v[4] = 6.0
	assert.InDelta(t, 1.0, combinePixel(o, v, nil, nil, s), 0.05, "a strong outlier must barely move it")

	// Sweeping an outlier's brightness must move the result smoothly, with no cliff.
	var prev float64
	for k := 0; k < 20; k++ {
		u := clean(n)
		u[4] = 1.0 + 0.25*float64(k)
		got := combinePixel(o, u, nil, nil, s)
		if k > 0 {
			assert.Less(t, math.Abs(got-prev), 0.02, "the response must stay continuous at step %d", k)
		}
		prev = got
	}
}

func TestEntropyWeighted_FavoursTheDetailedFrames(t *testing.T) {
	v := []float64{1, 1, 1, 5}
	detail := []float64{0, 0, 0, 1} // only the last frame resolves anything here
	o := resolved(stackalg.RejectEntropyWeighted, len(v))
	got := combinePixel(o, v, nil, detail, newScratch(len(v)))
	assert.Greater(t, got, 2.0, "the detailed frame must dominate")

	// With no detail measured at all it must degrade to a plain mean, not to zero.
	assert.InDelta(t, 2.0, combinePixel(o, v, nil, nil, newScratch(len(v))), 1e-9)
}

// TestRejection_DegenerateInputs: a pixel that is identical in every frame (a saturated core, a
// masked region) has zero spread — every algorithm must return that value, never NaN.
func TestRejection_DegenerateInputs(t *testing.T) {
	for _, algo := range []stackalg.Reject{
		stackalg.RejectNone, stackalg.RejectPercentile, stackalg.RejectSigma,
		stackalg.RejectMedianSigma, stackalg.RejectWinsorized, stackalg.RejectLinearFit,
		stackalg.RejectGESD, stackalg.RejectMAD, stackalg.RejectRCR,
		stackalg.RejectAdaptiveWeighted, stackalg.RejectEntropyWeighted,
	} {
		t.Run(string(algo)+"/flat", func(t *testing.T) {
			v := []float64{0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5}
			got := combinePixel(resolved(algo, len(v)), v, nil, nil, newScratch(len(v)))
			require.False(t, math.IsNaN(got), "a flat pixel must never produce NaN")
			assert.InDelta(t, 0.5, got, 1e-9)
		})
		t.Run(string(algo)+"/zeros", func(t *testing.T) {
			v := make([]float64, 8)
			got := combinePixel(resolved(algo, len(v)), v, nil, nil, newScratch(len(v)))
			require.False(t, math.IsNaN(got))
			assert.InDelta(t, 0, got, 1e-9)
		})
		t.Run(string(algo)+"/single frame", func(t *testing.T) {
			got := combinePixel(resolved(algo, 1), []float64{0.25}, nil, nil, newScratch(1))
			assert.InDelta(t, 0.25, got, 1e-9)
		})
	}
}

// TestGESDCritical_GrowsWithStrictness sanity-checks the statistical machinery: a stricter
// significance must demand a larger deviation before a sample is called an outlier.
func TestGESDCritical_GrowsWithStrictness(t *testing.T) {
	loose := gesdCritical(60, 1, 0.10)
	strict := gesdCritical(60, 1, 0.01)
	assert.Greater(t, strict, loose)
	assert.Greater(t, loose, 2.0, "even a loose test needs a couple of sigma")
	assert.Less(t, strict, 6.0, "and a strict one must stay reachable")
}

func TestNormalQuantile_MatchesKnownValues(t *testing.T) {
	assert.InDelta(t, 0, normalQuantile(0.5), 1e-9)
	assert.InDelta(t, 1.2815515655, normalQuantile(0.9), 1e-6)
	assert.InDelta(t, 1.9599639845, normalQuantile(0.975), 1e-6)
	assert.InDelta(t, -2.3263478740, normalQuantile(0.01), 1e-6)
}
