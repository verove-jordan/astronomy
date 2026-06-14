package grade

import (
	"math"
	"sort"
)

// Trail detector tuning.
const (
	trailBrightK   = 5.0  // bright threshold = median + k·(1.4826·MAD)
	trailThetaN    = 180  // angular resolution (1° steps)
	trailMaxBrite  = 0.30 // skip if more than this fraction of cells are "bright" (textured frame)
	trailVoteFrac  = 0.25 // a line must span at least this fraction of min(w,h)
	trailPeakRatio = 4.0  // the peak line must dominate the 99th-percentile of Hough bins
)

// DetectTrail reports whether a (downsampled, max-pooled) image contains a long straight bright
// streak — a satellite or aircraft trail — via a Hough line transform over bright pixels. A real
// streak produces one Hough bin that both spans much of the frame AND dominates all others;
// compact sources (stars) spread their votes and never dominate. Returns the flag and the span
// score (peak votes ÷ min(w,h)).
func DetectTrail(grid []float64, w, h int) (bool, float64) {
	if w < 16 || h < 16 || len(grid) != w*h {
		return false, 0
	}
	med, mad := medianMAD(grid)
	if mad <= 0 {
		return false, 0
	}
	thr := med + trailBrightK*mad
	var xs, ys []int
	for i, v := range grid {
		if v > thr {
			xs = append(xs, i%w)
			ys = append(ys, i/w)
		}
	}
	bright := len(xs)
	if bright < 16 || float64(bright) > trailMaxBrite*float64(w*h) {
		return false, 0
	}

	sin := make([]float64, trailThetaN)
	cos := make([]float64, trailThetaN)
	for t := 0; t < trailThetaN; t++ {
		a := math.Pi * float64(t) / float64(trailThetaN)
		sin[t], cos[t] = math.Sin(a), math.Cos(a)
	}
	diag := int(math.Ceil(math.Hypot(float64(w), float64(h))))
	rhoBins := 2*diag + 1
	acc := make([]int, trailThetaN*rhoBins)
	maxVotes := 0
	for k := 0; k < bright; k++ {
		fx, fy := float64(xs[k]), float64(ys[k])
		for t := 0; t < trailThetaN; t++ {
			rho := int(math.Round(fx*cos[t]+fy*sin[t])) + diag
			idx := t*rhoBins + rho
			acc[idx]++
			if acc[idx] > maxVotes {
				maxVotes = acc[idx]
			}
		}
	}

	minDim := w
	if h < w {
		minDim = h
	}
	span := float64(maxVotes) / float64(minDim)
	dominant := float64(maxVotes) >= trailPeakRatio*percentile99(acc)
	return span >= trailVoteFrac && dominant, span
}

// percentile99 returns the 99th-percentile value among positive accumulator bins (the typical
// "busy" line level), used to judge whether the peak truly stands out.
func percentile99(acc []int) float64 {
	var pos []int
	for _, v := range acc {
		if v > 0 {
			pos = append(pos, v)
		}
	}
	if len(pos) == 0 {
		return 0
	}
	sort.Ints(pos)
	return float64(pos[int(0.99*float64(len(pos)-1))])
}

// medianMAD returns the median and the median absolute deviation scaled to a σ estimate
// (×1.4826), the robust spread used throughout grading.
func medianMAD(vals []float64) (median, mad float64) {
	if len(vals) == 0 {
		return 0, 0
	}
	cp := make([]float64, len(vals))
	copy(cp, vals)
	sort.Float64s(cp)
	median = cp[len(cp)/2]
	dev := make([]float64, len(cp))
	for i, v := range cp {
		dev[i] = math.Abs(v - median)
	}
	sort.Float64s(dev)
	return median, 1.4826 * dev[len(dev)/2]
}
