package dither

import "math"

// Choosing WHERE to dither next. The diagnostic above rates a finished session; this plans one as
// it is shot, and the two share a package deliberately — the planner exists to produce exactly the
// pattern the diagnostic calls ideal.
//
// The offsets are NOT random. Randomness is the traditional answer, but it clusters: two draws
// landing a pixel apart waste a dither, and with a handful of frames per filter there is no law of
// large numbers to rescue it. Instead each new offset is chosen to maximise how far it is from
// every offset already used — in whole pixels, so a warm pixel lands somewhere genuinely new, and
// in sub-pixel PHASE, so the dithered stack also samples the pixel grid evenly (which is what makes
// drizzle and fine detail work). Deterministic, spread, and better than random on both counts.
//
// The other half of the job is closing the loop: mounts have backlash, so a commanded nudge is not
// an achieved one. Planner.Achieved records what the frames actually show, so the NEXT target is
// computed from where the camera really is rather than where it was asked to go.

// Offset is a dither position in sensor pixels, relative to the session's origin frame.
type Offset struct {
	X float64
	Y float64
}

// Planner picks successive dither offsets inside a square box.
type Planner struct {
	radiusPx float64
	used     []Offset
	current  Offset
	index    int
}

// NewPlanner builds a planner for a box of ±radiusPx. A radius of ~10 px is the usual choice: big
// enough to move a warm pixel well clear of its old neighbours, small enough that the frames still
// overlap almost completely.
func NewPlanner(radiusPx float64) *Planner {
	if radiusPx <= 0 {
		radiusPx = 10
	}
	return &Planner{radiusPx: radiusPx, used: []Offset{{}}}
}

// candidatesPerPick is how many Halton points are scored for each dither. A low-discrepancy
// sequence already spreads well, so a few dozen candidates is enough to find a good one without
// making this a search problem.
const candidatesPerPick = 64

// Next returns the delta to command (relative to the current position) and the absolute offset it
// should reach. The absolute offset is what Achieved corrects against.
func (p *Planner) Next() (delta Offset, target Offset) {
	best := Offset{}
	bestScore := -1.0
	for i := 0; i < candidatesPerPick; i++ {
		p.index++
		cand := Offset{
			X: (halton(p.index, 2)*2 - 1) * p.radiusPx,
			Y: (halton(p.index, 3)*2 - 1) * p.radiusPx,
		}
		if score := p.score(cand); score > bestScore {
			best, bestScore = cand, score
		}
	}
	delta = Offset{X: best.X - p.current.X, Y: best.Y - p.current.Y}
	p.used = append(p.used, best)
	p.current = best
	return delta, best
}

// score rates a candidate: how far it is from the nearest already-used offset, combined with how
// far its sub-pixel phase is from the nearest used phase. Both matter and neither alone is enough —
// a candidate 8 px away but landing on the same pixel phase re-samples the same grid positions.
func (p *Planner) score(c Offset) float64 {
	minDist := math.Inf(1)
	minPhase := math.Inf(1)
	for _, u := range p.used {
		d := math.Hypot(c.X-u.X, c.Y-u.Y)
		if d < minDist {
			minDist = d
		}
		if ph := phaseDistance(c, u); ph < minPhase {
			minPhase = ph
		}
	}
	if math.IsInf(minDist, 1) {
		return 0
	}
	// Whole-pixel separation dominates (it is what decorrelates fixed-pattern noise); phase
	// coverage breaks ties between otherwise equally distant candidates.
	return minDist + 2*minPhase
}

// phaseDistance is the toroidal distance between two offsets' sub-pixel phases: how differently the
// two frames sample the pixel grid.
func phaseDistance(a, b Offset) float64 {
	dx := wrapPhase(a.X - b.X)
	dy := wrapPhase(a.Y - b.Y)
	return math.Hypot(dx, dy)
}

// wrapPhase maps a difference onto [-0.5, 0.5]: 0.99 px apart is the same grid phase as 0.01 px.
func wrapPhase(d float64) float64 {
	d = math.Mod(d, 1)
	if d > 0.5 {
		d -= 1
	}
	if d < -0.5 {
		d += 1
	}
	return d
}

// Achieved records where the camera actually ended up (measured from the frames), replacing the
// commanded target. Backlash means a commanded 8 px move can achieve 3; without this the planner
// would keep believing a fiction and its careful spread would drift out of true.
func (p *Planner) Achieved(actual Offset) {
	p.current = actual
	if n := len(p.used); n > 0 {
		p.used[n-1] = actual
	}
}

// Current is the planner's belief about where the camera sits.
func (p *Planner) Current() Offset { return p.current }

// Used returns the offsets visited so far (the first is the origin).
func (p *Planner) Used() []Offset { return append([]Offset(nil), p.used...) }

// halton is the low-discrepancy sequence used to generate candidates: it fills the box evenly by
// construction, so even the first few dithers are well spread instead of accidentally clustered.
func halton(index, base int) float64 {
	result, f := 0.0, 1.0
	for i := index; i > 0; i /= base {
		f /= float64(base)
		result += f * float64(i%base)
	}
	return result
}
