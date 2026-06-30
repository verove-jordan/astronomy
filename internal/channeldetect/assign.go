package channeldetect

import (
	"math"
	"sort"
)

// FlagTransitions segments already-same-filter samples by acquisition gaps and returns one
// Assignment per frame, with WheelTransition set on the first frame of any run whose brightness
// hasn't settled. Use this when the filter is already known and only transition detection is needed.
func FlagTransitions(samples []Sample, opts Options) []Assignment {
	if len(samples) == 0 {
		return nil
	}
	ordered := append([]Sample(nil), samples...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Order < ordered[j].Order })
	runs := segmentRuns(ordered, opts)
	res := Result{Assignments: make([]Assignment, 0, len(ordered))}
	for r, idxs := range runs {
		for _, i := range idxs {
			res.Assignments = append(res.Assignments, Assignment{Path: ordered[i].Path, RunIndex: r})
		}
	}
	flagTransitions(ordered, runs, &res, opts)
	return res.Assignments
}

// transition-cost weights for the forward cyclic walk between consecutive runs.
const (
	stayPenalty = 0.20 // step 0: same filter as previous run (segmentation over-split)
	skipPenalty = 0.40 // per filter skipped (step ≥ 2)
)

// assignCyclic labels each run with a filter by finding the minimum-cost forward walk through the
// fixed cyclic order. Emission cost ties the brightness extremes to L (brightest) and the narrowband
// filter (faintest); transition cost prefers advancing by exactly one wheel position. R/G/B fall out
// purely by position. Returns the per-run filters, per-run confidence, and an overall confidence.
func assignCyclic(s []Sample, runs [][]int, order []string) (filters []string, runConf []float64, overall float64) {
	R, L := len(runs), len(order)
	if R == 0 {
		return nil, nil, 0
	}
	levels := make([]float64, R)
	rich := make([]float64, R)
	for r, idxs := range runs {
		var sl, sr float64
		for _, i := range idxs {
			sl += level(s[i].FP)
			sr += s[i].FP.StarRichness
		}
		levels[r] = sl / float64(len(idxs))
		rich[r] = sr / float64(len(idxs))
	}
	nl, nr := minMaxNorm(levels), minMaxNorm(rich)
	em := make([][]float64, R)
	for r := 0; r < R; r++ {
		em[r] = make([]float64, L)
		for p := 0; p < L; p++ {
			em[r][p] = emissionFor(order[p], nl[r], nr[r])
		}
	}

	filters, runConf, avgEmission := viterbi(em, order)
	return filters, runConf, overallConfidence(levels, avgEmission)
}

// overallConfidence blends how strongly the brightness anchors separate the cycle ends (raw spread)
// with how cleanly the chosen path fits its filter prototypes. A flat set with no L/Ha anchor (e.g.
// RGB-only) scores low and is surfaced for user override, even though its path may fit "broadband".
func overallConfidence(levels []float64, avgEmission float64) float64 {
	if len(levels) == 0 {
		return 0
	}
	lo, hi := levels[0], levels[0]
	for _, v := range levels {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	spread := 0.0
	if hi > 0 {
		spread = (hi - lo) / hi
	}
	fit := math.Max(0, 1-2*avgEmission)
	return math.Max(0, math.Min(1, spread)) * fit
}

// emissionFor scores how well a run fits a filter position from its normalized brightness (levelScore)
// and star richness (richScore), both in [0,1]. Luminance is the brightest; narrowband is faint AND
// star-poor, so a star-rich run (real broadband) is never cheaply narrowband — this stops a faint tail
// from avalanching to Ha. R/G/B sit in the middle and are resolved by wheel position, not brightness.
func emissionFor(filter string, levelScore, richScore float64) float64 {
	score := 0.5*levelScore + 0.5*richScore
	switch {
	case filter == "L":
		return 1 - score // luminance is the brightest broadband
	case isNarrowband(filter):
		// faint counts as narrowband only when stars are absent too: a run richer than the neutral
		// mid-point pays a penalty, so a star-rich (broadband) tail never collapses to Ha. When richness
		// carries no information (flat → 0.5 for all), the penalty is zero and this reduces to brightness.
		return score + math.Max(0, richScore-0.5)
	default:
		return math.Max(score, 1-score) - 0.5 // broadband R/G/B sit in the middle
	}
}

func isNarrowband(filter string) bool {
	switch filter {
	case "Ha", "OIII", "SII":
		return true
	default:
		return false
	}
}

// viterbi runs the forward cyclic DP over the emission matrix and returns the min-cost filter path,
// a per-run confidence (how cleanly each run matched its filter), and the path's average emission
// cost (lower = a better fit; used by overallConfidence).
func viterbi(em [][]float64, order []string) (filters []string, conf []float64, avgEmission float64) {
	R, L := len(em), len(order)
	dp := make([][]float64, R)
	bk := make([][]int, R)
	for r := range dp {
		dp[r] = make([]float64, L)
		bk[r] = make([]int, L)
	}
	copy(dp[0], em[0])
	for r := 1; r < R; r++ {
		for p := 0; p < L; p++ {
			best, bestPrev := math.Inf(1), 0
			for q := 0; q < L; q++ {
				step := ((p-q)%L + L) % L // forward distance q→p around the cycle, in [0,L)
				if c := dp[r-1][q] + transitionCost(step); c < best {
					best, bestPrev = c, q
				}
			}
			dp[r][p] = best + em[r][p]
			bk[r][p] = bestPrev
		}
	}

	endP, best := 0, math.Inf(1)
	for p := 0; p < L; p++ {
		if dp[R-1][p] < best {
			best, endP = dp[R-1][p], p
		}
	}

	filters = make([]string, R)
	conf = make([]float64, R)
	var emSum float64
	p := endP
	for r := R - 1; r >= 0; r-- {
		filters[r] = order[p]
		conf[r] = math.Max(0, 1-em[r][p])
		emSum += em[r][p]
		if r > 0 {
			p = bk[r][p]
		}
	}
	return filters, conf, emSum / float64(R)
}

// transitionCost penalizes the forward step (in [0,L)) between consecutive runs.
func transitionCost(step int) float64 {
	switch step {
	case 1:
		return 0 // the wheel advanced by exactly one position (incl. wrap) — expected
	case 0:
		return stayPenalty // same filter again — an over-split run
	default:
		return float64(step-1) * skipPenalty // one or more filters skipped
	}
}

// flagTransitions marks the first frame of a run as a filter-wheel transition only when its
// background deviates from the run's settled level (the next few frames) beyond the thresholds.
func flagTransitions(s []Sample, runs [][]int, res *Result, opts Options) {
	byPath := make(map[string]int, len(res.Assignments))
	for i := range res.Assignments {
		byPath[res.Assignments[i].Path] = i
	}
	for _, idxs := range runs {
		if len(idxs) < opts.TransitionLookahead+1 {
			continue
		}
		first := s[idxs[0]]
		ref := make([]float64, 0, opts.TransitionLookahead)
		for k := 1; k <= opts.TransitionLookahead; k++ {
			ref = append(ref, s[idxs[k]].FP.Background)
		}
		med, mad := medianMAD(ref)
		if med <= 0 {
			continue
		}
		delta := math.Abs(first.FP.Background - med)
		if delta <= math.Max(opts.TransitionRelFloor*med, opts.TransitionSigma*mad) {
			continue
		}
		if ai, ok := byPath[first.Path]; ok {
			res.Assignments[ai].WheelTransition = true
			res.Assignments[ai].TransitionDelta = delta / med
		}
	}
}
