package solar

import (
	"fmt"
	"math"
	"sort"
)

// gate.go decides which frames inside a group are worth stacking, and says why.
//
// The split between the two kinds of verdict is deliberate. Where physics gives a threshold — a
// clipped disc carries no recoverable detail, a disc running off the frame edge cannot be
// registered, a frame far too dark contributes noise — the gate is absolute and rejects. Everything
// else is an observation about a frame relative to its siblings, and is recorded as an advisory
// rather than a rejection.
//
// In particular a frame is NOT rejected for being exposed differently from its group. A real test
// session brackets exposure on purpose, and per-frame photometric normalisation exists precisely so
// those frames still stack together — throwing them away would discard most of a session's data to
// fix a problem the pipeline already solves.

const (
	// clippedFracLimit is the on-disc saturated fraction beyond which the exposure was blown. A few
	// isolated hot pixels are fine; half a percent of the disc is not.
	clippedFracLimit = 0.005
	// darkFloorFrac is how far below the group's disc brightness a frame may sit before it is
	// contributing noise rather than signal. An eighth of the group median is roughly three stops.
	darkFloorFrac = 0.125
	// defocusFrac is the sharpness floor, as a fraction of the group's median detail. It only fires
	// alongside the outlier test, so a merely-softer frame survives and lets the stacker's own
	// frame ranking decide.
	defocusFrac = 0.5
	// minPartialDiscArc is how much limb must be in frame for a partial disc to stay registerable.
	// A sixth of the circumference still pins a circle usefully; much less and the radius runs away.
	minPartialDiscArc = 60.0
	// minMembersForOutlierGates is the group size below which sibling comparison means nothing.
	minMembersForOutlierGates = 5
	// outlierZ is the MAD-based modified z-score beyond which a member is an outlier.
	outlierZ = 3.5
)

// gateGroup applies the gates, then summarises the group.
func gateGroup(g *Group, opts Options) {
	for i := range g.Members {
		applyAbsoluteGates(&g.Members[i])
	}
	applyRelativeGates(g)

	var detail []float64
	var frames int
	for _, m := range g.Members {
		if m.Rejected {
			continue
		}
		frames += m.Frames
		detail = append(detail, m.Detail)
	}
	g.Detail = median(detail)
	g.Frames = frames
	g.Stackable = frames >= opts.minFrames()
	if !g.Stackable {
		g.Notes = append(g.Notes, fmt.Sprintf("only %d usable frame(s); needs %d to stack", frames, opts.minFrames()))
	}
}

// applyAbsoluteGates rejects frames on grounds that need no comparison.
func applyAbsoluteGates(m *Member) {
	if m.Err != "" {
		m.reject(ReasonUnreadable, m.Err)
		return
	}
	if !m.DiscOK {
		m.reject(ReasonNoLimb, "no solar limb could be fitted — most often zoomed in past the disc")
		return
	}
	if m.ClippedFrac > clippedFracLimit {
		m.reject(ReasonClipped, fmt.Sprintf("%.1f%% of the disc is saturated — the exposure is blown and that detail is gone for good", 100*m.ClippedFrac))
	}
	// A disc running past the frame edge is NOT a fault: a close-up of the limb and its prominences
	// is one of the things this telescope is for, and the geometry is still pinned as long as enough
	// limb is in shot for the circle fit to be constrained. Only reject when the visible arc is too
	// short to define a circle — then the centre and radius really are guesswork.
	if m.Disc.Partial && m.Disc.ArcDeg < minPartialDiscArc {
		m.reject(ReasonEdgeClipped, fmt.Sprintf("only %.0f° of limb is in frame — not enough to pin the disc's centre and radius", m.Disc.ArcDeg))
	} else if m.Disc.Partial {
		m.note(ReasonEdgeClipped, fmt.Sprintf("partial disc: %.0f° of limb in frame, registered from the limb arc", m.Disc.ArcDeg))
	}
}

// applyRelativeGates compares each member against its siblings: a hard floor on brightness and
// sharpness, and advisories for everything else.
func applyRelativeGates(g *Group) {
	live := liveMembers(g)
	if len(live) < minMembersForOutlierGates {
		return
	}
	exposure := robustStats(fieldOf(g, live, func(m Member) float64 { return m.OnDiscMedian }))
	detail := robustStats(fieldOf(g, live, func(m Member) float64 { return m.Detail }))
	haze := robustStats(fieldOf(g, live, func(m Member) float64 { return m.LimbRatio }))

	for _, i := range live {
		m := &g.Members[i]

		if exposure.med > 0 && m.OnDiscMedian < darkFloorFrac*exposure.med {
			m.reject(ReasonTooDark, fmt.Sprintf("the disc reads %.3g against this group's %.3g — too far under-exposed to add anything but noise", m.OnDiscMedian, exposure.med))
		} else if z, ok := exposure.z(m.OnDiscMedian); ok && math.Abs(z) > outlierZ {
			m.note(ReasonExposure, fmt.Sprintf("exposed differently from its siblings (%.3g against %.3g); photometric normalisation will bring it into line", m.OnDiscMedian, exposure.med))
		}

		z, ok := detail.z(m.Detail)
		if ok && z < -outlierZ && m.Detail < defocusFrac*detail.med {
			m.reject(ReasonDefocused, fmt.Sprintf("sharpness %.3g against this group's %.3g — out of focus or badly shaken", m.Detail, detail.med))
		} else if ok && z < -outlierZ {
			m.note(ReasonDefocused, fmt.Sprintf("softer than its siblings (%.3g against %.3g); frame selection will rank it low", m.Detail, detail.med))
		}

		if z, ok := haze.z(m.LimbRatio); ok && math.Abs(z) > outlierZ {
			m.note(ReasonHaze, "its limb-darkening profile differs from its siblings — thin cloud, haze, or simply a different etalon tuning")
		}
	}
}

// liveMembers returns the indices of members still in play.
func liveMembers(g *Group) []int {
	var out []int
	for i := range g.Members {
		if !g.Members[i].Rejected {
			out = append(out, i)
		}
	}
	return out
}

// fieldOf extracts one measurement across the given members.
func fieldOf(g *Group, idx []int, get func(Member) float64) []float64 {
	out := make([]float64, 0, len(idx))
	for _, i := range idx {
		out = append(out, get(g.Members[i]))
	}
	return out
}

// robust holds a median and MAD for outlier scoring.
type robust struct{ med, mad float64 }

// robustStats computes the median and a robust scale for v.
//
// The scale is the median absolute deviation, falling back to the MEAN absolute deviation when the
// MAD is zero. That happens whenever more than half the values tie — quantised measurements, or a
// burst shot on identical settings — and without the fallback a single glaring outlier among
// identical siblings could never be flagged, because every test would divide by zero and give up.
func robustStats(v []float64) robust {
	if len(v) == 0 {
		return robust{}
	}
	med := median(v)
	dev := make([]float64, len(v))
	var sum float64
	for i, x := range v {
		dev[i] = math.Abs(x - med)
		sum += dev[i]
	}
	scale := median(dev)
	if scale <= 1e-12 {
		scale = sum / float64(len(v))
	}
	return robust{med: med, mad: scale}
}

// z returns the modified z-score of x. ok=false when the MAD is degenerate (every sibling
// identical), in which case no outlier claim can be made.
func (r robust) z(x float64) (float64, bool) {
	if r.mad <= 1e-12 {
		return 0, false
	}
	return 0.6745 * (x - r.med) / r.mad, true
}

// reject records a disqualifying verdict against a member.
func (m *Member) reject(code, text string) {
	m.Rejected = true
	m.Reasons = append(m.Reasons, Reason{Code: code, Text: text, Rejects: true})
}

// note records an observation that is worth showing but does not disqualify the frame.
func (m *Member) note(code, text string) {
	m.Reasons = append(m.Reasons, Reason{Code: code, Text: text})
}

// annotateNeighbours records, for each group, how far the nearest same-kind group sits in scale.
// This is the number that decides whether merging two groups is a cheap resample or a pointless
// one — and it is why triage reports adjacency instead of silently picking a coarser tolerance.
func annotateNeighbours(groups []Group) {
	byKind := map[Kind][]int{}
	for i, g := range groups {
		byKind[g.Kind] = append(byKind[g.Kind], i)
	}
	for _, idx := range byKind {
		sort.Slice(idx, func(a, b int) bool { return groups[idx[a]].DiscRadius < groups[idx[b]].DiscRadius })
		for n, gi := range idx {
			best := math.Inf(1)
			for _, adj := range []int{n - 1, n + 1} {
				if adj < 0 || adj >= len(idx) {
					continue
				}
				other := groups[idx[adj]].DiscRadius
				if groups[gi].DiscRadius <= 0 || other <= 0 {
					continue
				}
				if d := math.Abs(other/groups[gi].DiscRadius - 1); d < best {
					best = d
				}
			}
			if !math.IsInf(best, 1) {
				groups[gi].NearestPct = 100 * best
			}
		}
	}
}
