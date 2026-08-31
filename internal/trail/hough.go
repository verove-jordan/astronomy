package trail

import (
	"math"
	"sort"
)

// houghPeak accumulates a 180-bin Hough line transform over the seed pixels (pooled indices) and
// returns the dominant line as unit normal (cosT,sinT) with offset rhoVal (so cosT·x + sinT·y = rhoVal)
// plus its vote count. ok is true only when the peak both spans ≥ trailVoteFrac·min(gw,gh) and
// dominates the 99th percentile of populated bins by ≥ trailPeakRatio — the same test as
// grade.DetectTrail. The ρ binning (round(x·cos+y·sin)+diag, diag = ceil(hypot(gw,gh))) is identical.
func houghPeak(seeds []int, gw, gh int, voteFrac float64) (cosT, sinT, rhoVal float64, votes int, ok bool) {
	sin := make([]float64, trailThetaN)
	cos := make([]float64, trailThetaN)
	for t := 0; t < trailThetaN; t++ {
		a := math.Pi * float64(t) / float64(trailThetaN)
		sin[t], cos[t] = math.Sin(a), math.Cos(a)
	}
	diag := int(math.Ceil(math.Hypot(float64(gw), float64(gh))))
	rhoBins := 2*diag + 1
	acc := make([]int, trailThetaN*rhoBins)
	bestVotes, bestT, bestRho := 0, 0, 0
	for _, idx := range seeds {
		fx, fy := float64(idx%gw), float64(idx/gw)
		for t := 0; t < trailThetaN; t++ {
			rho := int(math.Round(fx*cos[t]+fy*sin[t])) + diag
			a := t*rhoBins + rho
			acc[a]++
			if acc[a] > bestVotes {
				bestVotes, bestT, bestRho = acc[a], t, rho
			}
		}
	}
	minDim := gw
	if gh < gw {
		minDim = gh
	}
	spans := float64(bestVotes) >= voteFrac*float64(minDim)
	dominant := float64(bestVotes) >= trailPeakRatio*percentile99(acc)
	if !spans || !dominant {
		return 0, 0, 0, bestVotes, false
	}
	return cos[bestT], sin[bestT], float64(bestRho - diag), bestVotes, true
}

// percentile99 returns the 99th-percentile value among positive accumulator bins — the typical "busy"
// line level, ported verbatim from grade.percentile99.
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
