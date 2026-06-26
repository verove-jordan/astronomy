package channeldetect

import "math"

// segmentRuns splits time-ordered samples into contiguous same-filter runs. A boundary is placed
// after frame i when any of these hold (a wheel move or a session/exposure change):
//   - the exposure time changes,
//   - the time gap to the next frame is far larger than the typical intra-run cadence,
//   - the signal level jumps far more than the typical frame-to-frame variation.
//
// This targets block-captured sequences (N frames dwelt per filter), which is the common deep-sky
// case; the gap and exposure cues are reliable even when two filters share a similar level.
func segmentRuns(s []Sample, opts Options) [][]int {
	n := len(s)
	if n == 0 {
		return nil
	}
	if n == 1 {
		return [][]int{{0}}
	}

	gaps := make([]float64, n-1)
	jumps := make([]float64, n-1)
	for i := 0; i < n-1; i++ {
		gaps[i] = float64(s[i+1].Order - s[i].Order)
		jumps[i] = math.Abs(level(s[i+1].FP) - level(s[i].FP))
	}
	medGap := medianOf(gaps)
	medJump, madJump := medianMAD(jumps)

	var runs [][]int
	cur := []int{0}
	for i := 0; i < n-1; i++ {
		if breaksHere(s, i, gaps, jumps, medGap, medJump, madJump, opts) {
			runs = append(runs, cur)
			cur = []int{i + 1}
			continue
		}
		cur = append(cur, i+1)
	}
	return append(runs, cur)
}

func breaksHere(s []Sample, i int, gaps, jumps []float64, medGap, medJump, madJump float64, opts Options) bool {
	if s[i].FP.ExposureMs != s[i+1].FP.ExposureMs {
		return true
	}
	if medGap > 0 && gaps[i] > opts.TimeGapFactor*medGap {
		return true
	}
	if madJump > 0 && jumps[i] > medJump+opts.FPBreakSigma*madJump {
		return true
	}
	return false
}
