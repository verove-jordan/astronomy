package solar

import (
	"sort"
)

// bestframes.go picks a spread of the sharpest individual frames.
//
// It exists because a stack is not always the best picture available. Registration resamples every
// frame and then averages estimates that disagree, and on a capture where the frames are already
// close to what the optics can deliver those two costs can exceed what averaging wins: measured on
// the 12 Aug eclipse clip, individual frames resolve the occulter's edge at sigma 1.09 px while the
// 834-frame master resolves it at 2.30 — the stack is twice as blurry as its own inputs. When that
// is true the honest answer is to hand back the frames.
//
// THE SPREAD IS THE POINT, not just the ranking. Sharpness drifts slowly — over seeing, over focus,
// over the mount settling — so the top of a ranked list is dozens of near-identical frames from the
// same second or two. Twenty of those are one picture with twenty names. A minimum separation in
// time turns the same list into twenty genuinely different views, which for an eclipse also means
// twenty positions of the Moon.

// SelectSpread returns up to n frames, the sharpest available subject to a minimum separation in
// time, in capture order.
//
// Greedy from the top of the ranking: take the best frame, refuse anything within minGapMs of a
// frame already taken, repeat. That is not the optimal packing — a dynamic programme would do
// marginally better — and it is the right shape, because it degrades the way a user expects. Ask for
// more frames than the clip can space out and it returns what it could, rather than quietly
// shrinking the gap and handing back near-duplicates.
func SelectSpread(frames []Frame, n int, minGapMs int64) []Frame {
	if n <= 0 || len(frames) == 0 {
		return nil
	}
	ranked := append([]Frame(nil), frames...)
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].Score > ranked[j].Score })

	var taken []Frame
	for _, f := range ranked {
		if len(taken) >= n {
			break
		}
		if minGapMs > 0 && f.TimeMs > 0 && tooClose(taken, f.TimeMs, minGapMs) {
			continue
		}
		taken = append(taken, f)
	}
	// Back into capture order: they are a sequence through the eclipse, and a viewer flicking through
	// them expects the Moon to move one way.
	sort.SliceStable(taken, func(i, j int) bool {
		if taken[i].TimeMs != taken[j].TimeMs {
			return taken[i].TimeMs < taken[j].TimeMs
		}
		return taken[i].Index < taken[j].Index
	})
	return taken
}

// tooClose reports whether a candidate sits within the minimum gap of anything already chosen.
func tooClose(taken []Frame, at, minGapMs int64) bool {
	for _, f := range taken {
		if f.TimeMs <= 0 {
			continue
		}
		if d := at - f.TimeMs; d < minGapMs && -d < minGapMs {
			return true
		}
	}
	return false
}
