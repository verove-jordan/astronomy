package transient

import (
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/trail"
)

// corridorWindow is one bucket of a segment's masking swath, covering lineWindowT steps along the
// line direction. Validation compares the candidate and the witnesses WINDOW BY WINDOW: a marching
// satellite lights a few windows per frame (different ones each frame), while fixed-pattern walking
// noise lights the SAME windows in the same large fraction of frames — the whole-corridor mean the
// old test used could not tell those apart (task #316: a geostationary-belt trail elevated >30% of
// witnesses somewhere along the full line and every real candidate was rejected as fixed pattern).
type corridorWindow struct {
	t0, t1 float64 // t-extent along the line (closed; windows share endpoints)
	idx    []int   // unique plane indices inside the swath for this t-range
}

// corridorWindows buckets segment s's masking swath into lineWindowT-long windows, walking ALONG the
// line (t ∈ [T0,T1]) and stepping ⊥ up to Width each side — the same walk as Segment.Contains.
// Windows that fall entirely off-image come back with an empty idx and are skipped by every consumer.
func corridorWindows(s trail.Segment, w, h int) []corridorWindow {
	span := s.T1 - s.T0
	nWin := int(span/lineWindowT) + 1
	wins := make([]corridorWindow, nWin)
	for b := range wins {
		wins[b].t0 = s.T0 + float64(b)*lineWindowT
		wins[b].t1 = math.Min(s.T1, s.T0+float64(b+1)*lineWindowT)
	}
	seen := map[int]struct{}{}
	p0x, p0y := s.Nx*s.C, s.Ny*s.C
	for t := s.T0; t <= s.T1; t++ {
		b := int((t - s.T0) / lineWindowT)
		if b >= nWin {
			b = nWin - 1
		}
		bx, by := p0x-t*s.Ny, p0y+t*s.Nx
		for u := -s.Width; u <= s.Width; u++ {
			x, y := int(math.Round(bx+u*s.Nx)), int(math.Round(by+u*s.Ny))
			if x < 0 || x >= w || y < 0 || y >= h {
				continue
			}
			i := y*w + x
			if _, ok := seen[i]; !ok {
				seen[i] = struct{}{}
				wins[b].idx = append(wins[b].idx, i)
			}
		}
	}
	return wins
}

// ownWindowMean averages the candidate frame's residual over one window, skipping zero-fill pixels
// (registration borders of a rotated frame read −median(sky) and would drag a real trail's window
// below significance). Returns the mean and the number of pixels that actually contributed.
func ownWindowMean(own []float32, win corridorWindow, isZero func(int) bool) (float64, int) {
	var sum float64
	n := 0
	for _, i := range win.idx {
		if isZero(i) {
			continue
		}
		sum += float64(own[i])
		n++
	}
	if n == 0 {
		return 0, 0
	}
	return sum / float64(n), n
}

// litWindows returns the indices of the windows where the candidate frame's residual is significant
// (mean ≥ lineWingK·ownSky over ≥ lineWindowMinPx non-zero pixels) plus the total lit pixel count.
func litWindows(wins []corridorWindow, own []float32, ownSky float64, isZero func(int) bool) (lit []int, litPx int) {
	for b, win := range wins {
		m, n := ownWindowMean(own, win, isZero)
		if n < lineWindowMinPx {
			continue
		}
		if m >= lineWingK*ownSky {
			lit = append(lit, b)
			litPx += n
		}
	}
	return lit, litPx
}

// witnessWindowFrac classifies every witness over ONE window: lit (mean ≥ lineWingK·its sky), dark, or
// unobserved (mean < −3·its sky — a zero-fill border reads −median(sky), far below any noise floor;
// only residual planes survive in the streamed basis, so witness zeros cannot be tested directly).
// Returns lit/observing and ok=false when fewer than lineWindowMinWitness witnesses observed the window.
func witnessWindowFrac(win corridorWindow, basisResid [][]float32, basisSky []float64, selfIdx int) (float64, bool) {
	litW, observing := 0, 0
	for j := range basisResid {
		if j == selfIdx {
			continue
		}
		var sum float64
		for _, i := range win.idx {
			sum += float64(basisResid[j][i])
		}
		m := sum / float64(len(win.idx))
		if m < -3*basisSky[j] {
			continue // unobserved: the witness has no data (zero fill) under this window
		}
		observing++
		if m >= lineWingK*basisSky[j] {
			litW++
		}
	}
	if observing < lineWindowMinWitness {
		return 0, false
	}
	return float64(litW) / float64(observing), true
}

// fixedPatternFrac is the candidate's recurrence score: the MEDIAN, over its lit windows, of the
// fraction of witnesses lit in the same window. Walking noise lights the same windows in the same
// frames every time (high median); a marching/multi-satellite trail lights each window in only a few
// frames (low median). The median absorbs a bright star sitting on the corridor, which legitimately
// lights one window in many witnesses. ok=false when no lit window had enough observing witnesses.
func fixedPatternFrac(wins []corridorWindow, lit []int, basisResid [][]float32, basisSky []float64, selfIdx int) (float64, bool) {
	fracs := make([]float64, 0, len(lit))
	for _, b := range lit {
		if len(wins[b].idx) == 0 {
			continue
		}
		if f, ok := witnessWindowFrac(wins[b], basisResid, basisSky, selfIdx); ok {
			fracs = append(fracs, f)
		}
	}
	if len(fracs) == 0 {
		return 0, false
	}
	sort.Float64s(fracs)
	return fracs[len(fracs)/2], true
}

// paintRuns merges the lit windows (±1 window of margin for the tapering wings) into disjoint runs
// and returns one extent-clipped copy of the segment per run — so the repair paints only where the
// frame actually carries the streak, not the whole border-extended line (painting a clean corridor
// stretch with the cross-frame median would correlate the stack's noise along it for nothing).
func paintRuns(s trail.Segment, wins []corridorWindow, lit []int) []trail.Segment {
	if len(lit) == 0 {
		return nil
	}
	expanded := map[int]struct{}{}
	for _, b := range lit {
		for _, e := range [3]int{b - 1, b, b + 1} {
			if e >= 0 && e < len(wins) {
				expanded[e] = struct{}{}
			}
		}
	}
	order := make([]int, 0, len(expanded))
	for b := range expanded {
		order = append(order, b)
	}
	sort.Ints(order)

	var runs []trail.Segment
	start := order[0]
	prev := order[0]
	flush := func(a, b int) {
		sub := s
		sub.T0, sub.T1 = wins[a].t0, wins[b].t1
		runs = append(runs, sub)
	}
	for _, b := range order[1:] {
		if b != prev+1 {
			flush(start, prev)
			start = b
		}
		prev = b
	}
	flush(start, prev)
	return runs
}

// frameZeroFn reports whether a pixel of f is zero-fill (all channels exactly 0 — Siril's
// registration fill), the footprint test the corridor statistics use for the candidate frame.
func frameZeroFn(f *fits.Image) func(int) bool {
	return func(i int) bool {
		for ch := 0; ch < f.C; ch++ {
			if f.Pix[ch][i] != 0 {
				return false
			}
		}
		return true
	}
}
