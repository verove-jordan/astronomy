package transient

import (
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/trail"
)

// The recurring-corridor pass catches what BOTH per-frame passes miss: a sky track reused by several
// satellites (the geostationary belt crosses Leo/ecliptic fields) leaves a marching segment in many
// frames. Per frame each segment can sit below the detector's seed; in the cross-frame MEDIAN it
// vanishes (no pixel has a majority of frames lit, so the geostationary pass never fires); but in the
// cross-frame MEAN of the residuals the segments sum coherently — exactly as they will in the final
// stack. Detecting on that mean finds the corridor once, and each frame then repairs only the windows
// it actually lights.

// meanAccum builds the coverage-aware mean residual plane: zero-fill pixels of a rotated frame read
// −median(sky) and would drag the mean negative under its borders, so a frame contributes to a pixel
// only where it has data. Sums stay float32 (≤ a few hundred residuals of ~1e-2 each).
type meanAccum struct {
	sum []float32
	cnt []uint16
}

func newMeanAccum(w, h int) *meanAccum {
	return &meanAccum{sum: make([]float32, w*h), cnt: make([]uint16, w*h)}
}

// add accumulates one frame's residual over the frame's data footprint.
func (a *meanAccum) add(resid []float32, f *fits.Image) {
	isZero := frameZeroFn(f)
	for i := range resid {
		if isZero(i) {
			continue
		}
		a.sum[i] += resid[i]
		a.cnt[i]++
	}
}

// plane finalizes the mean residual (0 where no frame had data).
func (a *meanAccum) plane() []float32 {
	out := make([]float32, len(a.sum))
	for i, c := range a.cnt {
		if c > 0 {
			out[i] = a.sum[i] / float32(c)
		}
	}
	return out
}

// recurringCorridor is one mean-plane trail with its windows and, per window, the fraction of basis
// frames lit there — the repair-mode switch: a window most frames light has a contaminated cross-frame
// median, so it is repaired from local background (like the geostationary pass); a lightly-shared
// window keeps the star-safe median paint.
type recurringCorridor struct {
	seg    trail.Segment
	wins   []corridorWindow
	shared []float64
}

// buildRecurring detects trails on the mean residual plane and precomputes each corridor's windows and
// per-window shared fractions. Corridors already found on the median plane (the geostationary pass
// repairs those in every frame) are dropped so a swath is never painted twice.
func buildRecurring(meanResid []float32, medSegs []trail.Segment,
	basisResid [][]float32, basisSky []float64, k float64, w, h int) []recurringCorridor {
	segs := trail.DetectSegments(meanResid, w, h, trail.DefaultParams(k))
	var recs []recurringCorridor
	for _, s := range segs {
		if coveredByMedian(s, medSegs) {
			continue
		}
		wins := corridorWindows(s, w, h)
		shared := make([]float64, len(wins))
		for b, win := range wins {
			if len(win.idx) == 0 {
				continue
			}
			// selfIdx −1: the shared fraction is a corridor-level statistic over the whole basis.
			if f, ok := witnessWindowFrac(win, basisResid, basisSky, -1); ok {
				shared[b] = f
			}
		}
		recs = append(recs, recurringCorridor{seg: s, wins: wins, shared: shared})
	}
	return recs
}

// coveredByMedian reports whether segment s runs along (nearly) the same line as a median-plane
// segment: normals within ~2° and center offsets within the swath widths. Median-plane normals may
// come back flipped (n, C) → (−n, −C), so both signs are compared.
func coveredByMedian(s trail.Segment, medSegs []trail.Segment) bool {
	const cosTol = 0.9994 // cos(2°)
	for _, m := range medSegs {
		dot := s.Nx*m.Nx + s.Ny*m.Ny
		c := m.C
		if dot < 0 {
			dot, c = -dot, -c
		}
		if dot < cosTol {
			continue
		}
		if math.Abs(s.C-c) <= 2*math.Max(s.Width, m.Width) {
			return true
		}
	}
	return false
}

// maskFrameRecurring repairs one frame along every recurring corridor: only the windows this frame
// actually lights (±1 window margin), median paint normally, local-background paint where the window
// is shared by ≥ lineSharedRepairFrac of the basis (contaminated median). Consecutive same-mode
// windows are painted as one range and interior boundaries are nudged open, so no pixel is painted
// (or counted in MaskedPx) twice.
func maskFrameRecurring(f *fits.Image, own []float32, ownSky float64, med [][]float32,
	recs []recurringCorridor, w, h, c int, fr *FrameReport) {
	isZero := frameZeroFn(f)
	for ri, rec := range recs {
		lit, litPx := litWindows(rec.wins, own, ownSky, isZero)
		if len(lit) == 0 || litPx < minCorridorPx {
			continue
		}
		for _, g := range modeGroups(expandLit(lit, len(rec.wins)), rec.shared) {
			sub := rec.seg
			sub.T0, sub.T1 = rec.wins[g.first].t0, rec.wins[g.last].t1
			if g.openEnd {
				sub.T1 -= 1e-6 // the next group starts at this t; keep the paints disjoint
			}
			for ch := 0; ch < c; ch++ {
				if g.localBG {
					fr.MaskedPx += trail.ApplySwathLocalBG(f.Pix[ch], w, h, sub, seedFor(1000+ri, ch))
				} else {
					fr.MaskedPx += trail.ApplySwathMedian(f.Pix[ch], med[ch], w, h, sub)
				}
			}
		}
	}
}

// paintGroup is a maximal run of consecutive windows sharing one repair mode. openEnd marks a group
// that abuts the next one (same run, mode switch), whose end boundary must be excluded.
type paintGroup struct {
	first, last int
	localBG     bool
	openEnd     bool
}

// modeGroups splits a sorted window list into paint groups: runs break on index gaps, groups within a
// run break on repair-mode changes.
func modeGroups(wins []int, shared []float64) []paintGroup {
	var out []paintGroup
	for i := 0; i < len(wins); {
		g := paintGroup{first: wins[i], last: wins[i], localBG: shared[wins[i]] >= lineSharedRepairFrac}
		j := i + 1
		for ; j < len(wins) && wins[j] == wins[j-1]+1 && (shared[wins[j]] >= lineSharedRepairFrac) == g.localBG; j++ {
			g.last = wins[j]
		}
		g.openEnd = j < len(wins) && wins[j] == g.last+1 // same run continues with the other mode
		out = append(out, g)
		i = j
	}
	return out
}

// expandLit widens the lit window set by ±1 (wing margin) and returns it sorted and deduplicated.
func expandLit(lit []int, nWin int) []int {
	seen := map[int]struct{}{}
	var out []int
	for _, b := range lit {
		for _, e := range [3]int{b - 1, b, b + 1} {
			if e >= 0 && e < nWin {
				if _, ok := seen[e]; !ok {
					seen[e] = struct{}{}
					out = append(out, e)
				}
			}
		}
	}
	sort.Ints(out)
	return out
}
