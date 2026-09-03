package eclipsegeom

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Side says which half of the eclipse a panel belongs to.
type Side int

const (
	Ingress Side = -1
	Peak    Side = 0
	Egress  Side = 1
)

func (s Side) String() string {
	switch s {
	case Ingress:
		return "ingress"
	case Egress:
		return "egress"
	}
	return "maximum"
}

// Ladder tuning. These are the numbers that decide what a sequence looks like, so each carries why.
const (
	// magFloor is the shallowest bite worth a panel: below six percent of the diameter the Moon's
	// edge is a dent the eye reads as a soft limb, not as an eclipse.
	magFloor = 0.06
	// magMatchTol is how far apart the two sides of one rung may be. Twelve points of DIAMETER is
	// about as much as a viewer can put side by side and still call the pair symmetric.
	magMatchTol = 0.12
	// minDepthRatio and minMagGap are the two separations a rung must clear against its neighbours.
	// Two are needed because neither coordinate works at both ends: near maximum the picture is
	// governed by the crescent's remaining thickness, so a RATIO is what separates 0.15 R from
	// 0.11 R; out at the shallow end the depth barely changes and it is the absolute bite that
	// distinguishes a panel from its neighbour.
	minDepthRatio = 1.28
	minMagGap     = 0.035
	// candidateStep is the grid the greedy insertion searches on, in magnitude.
	candidateStep = 0.002
)

// Span is a stretch of wall-clock time for which frames exist.
type Span struct {
	From time.Time
	To   time.Time
}

// Panel is one rendered phase in a sequence, ordered shallow ingress → maximum → shallow egress.
type Panel struct {
	Side        Side      `json:"side"`
	At          time.Time `json:"at"`
	Magnitude   float64   `json:"magnitude"`
	Obscuration float64   `json:"obscuration"`
	// TargetMag is the magnitude the ladder asked for; Magnitude is what the coverage could supply.
	TargetMag float64 `json:"target_mag"`
	// MissMag is how far this rung's two sides ended up apart, in magnitude. It is the sequence's
	// own honesty measure: 0 means the two panels really are the same phase.
	MissMag float64 `json:"miss_mag"`
}

// PlanLadder chooses which phases a sequence should show, given the instants that were actually
// recorded.
//
// It never invents a phase. Every panel it returns is an instant inside a supplied span, so a gap in
// the recording shows up as a rung the ladder declined to place, not as a mirrored or interpolated
// image. Symmetry is bought by pairing: a rung is only placed where BOTH sides can supply a phase
// within magMatchTol of each other, which is what makes the two halves of the finished picture
// mirror each other even when the clips do not.
//
// The rungs are placed by farthest-point insertion in log(crescent thickness) rather than by an even
// ladder that is then snapped. An even ladder assumes the coverage is even; this session's is not —
// twenty-two minutes of the ingress are simply missing — and a snapped ladder answers that by
// putting two rungs on the same crescent. Choosing each new rung where it is furthest from the ones
// already placed spends the panels where the picture still changes, and stops when it cannot place
// another honestly.
func PlanLadder(spans []Span, s Site, panels int) ([]Panel, []string, error) {
	if panels < 3 {
		return nil, nil, fmt.Errorf("plan ladder: %d panels is too few for a sequence", panels)
	}
	if len(spans) == 0 {
		return nil, nil, fmt.Errorf("plan ladder: no covered time")
	}
	from, to := outerBounds(spans)
	max, peak := Maximum(from.Add(-2*time.Hour), to.Add(2*time.Hour), s)
	if peak.Obscuration <= 0 {
		return nil, nil, fmt.Errorf("plan ladder: no eclipse at this site between %s and %s",
			from.Format(time.RFC3339), to.Format(time.RFC3339))
	}
	mPeak := peak.Magnitude()

	in, eg := sides(spans, s, max)
	if len(in) == 0 || len(eg) == 0 {
		return nil, nil, fmt.Errorf("plan ladder: one side of maximum has no frames (%d ingress, %d egress bands)",
			len(in), len(eg))
	}

	rungs, notes := placeRungs(in, eg, s, max, mPeak, (panels-1)/2)
	if len(rungs) == 0 {
		return nil, notes, fmt.Errorf("plan ladder: no phase could be matched on both sides")
	}
	if got := 2*len(rungs) + 1; got != panels {
		notes = append(notes, fmt.Sprintf(
			"the recording supports %d panels, not the %d asked for: only %d phases appear on both sides of maximum",
			got, panels, len(rungs)))
	}
	return assemble(rungs, max, peak, mPeak), notes, nil
}

// rung is one matched pair of phases either side of maximum.
type rung struct {
	target   float64
	inMag    float64
	egMag    float64
	inAt     time.Time
	egAt     time.Time
	inObsc   float64
	egObsc   float64
	depthKey float64 // 1-magnitude of the target, the coordinate spacing is judged in
}

// placeRungs greedily inserts the rung that is furthest from everything already placed.
func placeRungs(in, eg []band, s Site, max time.Time, mPeak float64, want int) ([]rung, []string) {
	var out []rung
	var notes []string
	for len(out) < want {
		best, ok := farthestCandidate(out, in, eg, s, max, mPeak)
		if !ok {
			break
		}
		out = append(out, best)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].inMag < out[j].inMag })
	for _, r := range out {
		if r.miss() > magMatchTol/2 {
			notes = append(notes, fmt.Sprintf(
				"the %.0f%%-magnitude rung is %.0f points apart between the two sides (%.0f%% in, %.0f%% out) — the nearest phases the recording holds",
				r.target*100, r.miss()*100, r.inMag*100, r.egMag*100))
		}
	}
	return out, notes
}

func (r rung) miss() float64 { return math.Abs(r.inMag - r.egMag) }

// farthestCandidate scans the magnitude grid for the placeable rung whose separation from the
// already-placed rungs (and from maximum) is largest.
func farthestCandidate(placed []rung, in, eg []band, s Site, max time.Time, mPeak float64) (rung, bool) {
	var best rung
	bestScore := -1.0
	for m := magFloor; m < mPeak; m += candidateStep {
		cand, ok := makeRung(m, in, eg, s, max)
		if !ok || !separated(cand, placed, mPeak) {
			continue
		}
		if score := separation(cand, placed, mPeak); score > bestScore {
			best, bestScore = cand, score
		}
	}
	return best, bestScore > 0
}

// makeRung snaps a target magnitude onto both sides and rejects the pair if the two ends are too far
// apart to be called the same phase.
func makeRung(m float64, in, eg []band, s Site, max time.Time) (rung, bool) {
	inMag, inAt, ok := nearest(in, m, true, s, max)
	if !ok {
		return rung{}, false
	}
	egMag, egAt, ok := nearest(eg, m, false, s, max)
	if !ok {
		return rung{}, false
	}
	if math.Abs(inMag-egMag) > magMatchTol {
		return rung{}, false
	}
	return rung{
		target: m, inMag: inMag, egMag: egMag, inAt: inAt, egAt: egAt,
		inObsc: At(inAt, s).Obscuration, egObsc: At(egAt, s).Obscuration,
		depthKey: 1 - m,
	}, true
}

// separated reports whether a candidate clears both separations against every placed rung and
// against maximum itself, on BOTH sides.
func separated(c rung, placed []rung, mPeak float64) bool {
	if !apart(c.inMag, mPeak) || !apart(c.egMag, mPeak) {
		return false
	}
	for _, p := range placed {
		if !apart(c.inMag, p.inMag) || !apart(c.egMag, p.egMag) {
			return false
		}
	}
	return true
}

// apart applies the two separations: a ratio of remaining crescent thickness, and an absolute bite.
func apart(a, b float64) bool {
	if math.Abs(a-b) < minMagGap {
		return false
	}
	da, db := math.Max(1e-6, 1-a), math.Max(1e-6, 1-b)
	ratio := math.Max(da, db) / math.Min(da, db)
	return ratio >= minDepthRatio
}

// separation scores a candidate by how far it sits from its nearest neighbour, in log depth.
func separation(c rung, placed []rung, mPeak float64) float64 {
	best := logDepthGap(c.target, mPeak)
	for _, p := range placed {
		if g := logDepthGap(c.target, p.target); g < best {
			best = g
		}
	}
	return best
}

func logDepthGap(a, b float64) float64 {
	da, db := math.Max(1e-6, 1-a), math.Max(1e-6, 1-b)
	return math.Abs(math.Log(da / db))
}

// assemble lays the rungs out as the sequence is read: shallow ingress first, maximum in the middle,
// shallow egress last.
func assemble(rungs []rung, max time.Time, peak Circumstance, mPeak float64) []Panel {
	out := make([]Panel, 0, 2*len(rungs)+1)
	for _, r := range rungs {
		out = append(out, Panel{
			Side: Ingress, At: r.inAt, Magnitude: r.inMag, Obscuration: r.inObsc,
			TargetMag: r.target, MissMag: r.miss(),
		})
	}
	out = append(out, Panel{
		Side: Peak, At: max, Magnitude: mPeak, Obscuration: peak.Obscuration, TargetMag: mPeak,
	})
	for i := len(rungs) - 1; i >= 0; i-- {
		r := rungs[i]
		out = append(out, Panel{
			Side: Egress, At: r.egAt, Magnitude: r.egMag, Obscuration: r.egObsc,
			TargetMag: r.target, MissMag: r.miss(),
		})
	}
	return out
}

func outerBounds(spans []Span) (from, to time.Time) {
	from, to = spans[0].From, spans[0].To
	for _, s := range spans[1:] {
		if s.From.Before(from) {
			from = s.From
		}
		if s.To.After(to) {
			to = s.To
		}
	}
	return from, to
}
