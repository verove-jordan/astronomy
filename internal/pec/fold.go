package pec

import (
	"math"
	"sort"
)

// Folding measurements onto the worm, and the question that decides whether PEC is worth writing at
// all: does the error actually repeat?

// Sample is one measurement of the RA axis error.
//
// Arcsec is an AXIS error, not a sky displacement — the two differ by cos(dec), and it is the axis
// the worm turns. Phase is fractional bins, taken from the mount's own counter.
type Sample struct {
	TimeSec   float64
	PhaseBins float64
	Arcsec    float64
}

// Folded is the measured error averaged onto the worm's bins. It is what the chart draws and what
// the repeatability check works on; the correction itself is computed from a fit, not from this.
type Folded struct {
	Mean    []float64 // axis arcsec, per bin
	Scatter []float64 // standard deviation within the bin
	Count   []int
	// Empty is how many bins no sample landed in. A curve with holes is interpolated across, and the
	// user is told rather than shown a confident line through nothing.
	Empty int
}

// Fold averages samples onto the worm's bins, rejecting outliers.
//
// A plain mean would let one satellite trail or one frame of cloud drag a bin by several arcseconds,
// and that bin is then written into the mount and replayed all night. The sigma clip costs nothing
// and removes exactly that.
func Fold(samples []Sample, g Geometry) *Folded {
	if !g.valid() || len(samples) == 0 {
		return nil
	}
	buckets := make([][]float64, g.Bins)
	for _, s := range samples {
		b := int(wrapPhase(s.PhaseBins, g.Bins))
		if b >= 0 && b < g.Bins {
			buckets[b] = append(buckets[b], s.Arcsec)
		}
	}

	out := &Folded{
		Mean:    make([]float64, g.Bins),
		Scatter: make([]float64, g.Bins),
		Count:   make([]int, g.Bins),
	}
	for b, vals := range buckets {
		if len(vals) == 0 {
			out.Empty++
			continue
		}
		mean, sd := clippedMean(vals)
		out.Mean[b], out.Scatter[b], out.Count[b] = mean, sd, len(vals)
	}
	fillEmptyBins(out)
	return out
}

// clippedMean is a mean with 3-sigma outliers removed, plus the surviving spread.
func clippedMean(vals []float64) (mean, sd float64) {
	if len(vals) < 4 {
		return plainMean(vals)
	}
	mean, sd = plainMean(vals)
	if sd == 0 {
		return mean, 0
	}
	kept := make([]float64, 0, len(vals))
	for _, v := range vals {
		if math.Abs(v-mean) <= 3*sd {
			kept = append(kept, v)
		}
	}
	if len(kept) == 0 {
		return mean, sd
	}
	return plainMean(kept)
}

func plainMean(vals []float64) (mean, sd float64) {
	if len(vals) == 0 {
		return 0, 0
	}
	for _, v := range vals {
		mean += v
	}
	mean /= float64(len(vals))
	if len(vals) < 2 {
		return mean, 0
	}
	for _, v := range vals {
		sd += (v - mean) * (v - mean)
	}
	return mean, math.Sqrt(sd / float64(len(vals)-1))
}

// fillEmptyBins interpolates across bins nothing landed in, so the displayed curve is continuous.
// The Count of 0 is left intact, which is how the caller knows the value was invented.
func fillEmptyBins(f *Folded) {
	n := len(f.Mean)
	if f.Empty == 0 || f.Empty == n {
		return
	}
	for b := range f.Mean {
		if f.Count[b] > 0 {
			continue
		}
		prev, next := b, b
		for f.Count[(prev+n)%n] == 0 {
			prev--
		}
		for f.Count[next%n] == 0 {
			next++
		}
		lo, hi := f.Mean[(prev+n)%n], f.Mean[next%n]
		span := float64(next - prev)
		f.Mean[b] = lo + (hi-lo)*float64(b-prev)/span
	}
}

// Repeatability is how much of the measured error is genuinely periodic in the worm.
type Repeatability struct {
	// Coherent is the fraction of the folded curve's variance that repeats from one set of worm
	// cycles to another, in [0,1]. It is the CEILING on what PEC can remove.
	Coherent float64
	// Cycles is how many worm revolutions the run covered.
	Cycles float64
	// NoiseArcsec is the per-bin scatter that did not repeat.
	NoiseArcsec float64
}

// MinCoherent is the repeatability below which writing a curve is refused.
//
// Below about half, most of what was measured is not the worm — it is seeing, wind, or a mount whose
// error simply does not repeat — and a table fitted to it replays that noise all night, every night,
// until somebody re-records it. Refusing is the honest outcome, and it is why this is measured before
// anything is written rather than discovered afterwards.
const MinCoherent = 0.5

// MeasureRepeatability folds odd and even worm cycles separately and compares them.
//
// Two independent folds of the same signal differ only by noise, so the variance of their difference
// is twice the noise variance — which separates "the worm does this every time" from "this happened
// while we were watching" without any model of either.
func MeasureRepeatability(samples []Sample, g Geometry) Repeatability {
	if !g.valid() || len(samples) < 2*g.Bins {
		return Repeatability{}
	}
	var odd, even []Sample
	for _, s := range samples {
		if int(math.Floor(s.TimeSec/g.WormPeriodSec))%2 == 0 {
			even = append(even, s)
		} else {
			odd = append(odd, s)
		}
	}
	a, b := Fold(even, g), Fold(odd, g)
	if a == nil || b == nil {
		return Repeatability{}
	}

	var span float64
	for _, s := range samples {
		span = math.Max(span, s.TimeSec)
	}
	rep := Repeatability{Cycles: span / g.WormPeriodSec}

	diff := make([]float64, g.Bins)
	mean := make([]float64, g.Bins)
	for i := range diff {
		diff[i] = a.Mean[i] - b.Mean[i]
		mean[i] = (a.Mean[i] + b.Mean[i]) / 2
	}
	_, diffSD := plainMean(diff)
	_, meanSD := plainMean(mean)

	noiseVar := diffSD * diffSD / 2
	rep.NoiseArcsec = math.Sqrt(noiseVar)
	if total := meanSD * meanSD; total > 0 {
		// The averaged fold still carries half the noise, so remove it to get the signal alone.
		rep.Coherent = math.Max(0, math.Min(1, (total-noiseVar/2)/total))
	}
	return rep
}

// PeakToPeak is the span of a curve — the number mounts are specified by.
func PeakToPeak(curve []float64) float64 {
	if len(curve) == 0 {
		return 0
	}
	sorted := append([]float64(nil), curve...)
	sort.Float64s(sorted)
	return sorted[len(sorted)-1] - sorted[0]
}
