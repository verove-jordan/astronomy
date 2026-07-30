package annotate

import (
	"fmt"
	"math"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/postprocess"
)

const (
	// matchTolPx is how close a projected catalogue star must land to a detected peak to count as
	// a match (both for flip validation and for snapping star labels onto their real centroid).
	matchTolPx = 4.0
	// flipProbeStars caps how many bright catalogue stars the flip validation projects.
	flipProbeStars = 30
)

// mapping carries the master-grid → final-PNG-pixel geometry for one run: the symmetric finish
// crop plus the two independent y-axis questions — whether the WCS y axis runs with or against
// the FITS file rows (decided empirically), and whether file rows run against the PNG's top-down
// rows (read from the master's ROWORDER card).
type mapping struct {
	W, H     int // master dims (file grid)
	Wf, Hf   int // final image dims
	cx, cy   int // symmetric crop offsets (master px)
	wcsFlip  bool
	fileFlip bool
}

// newMapping derives the crop window from the master vs final dimensions (the finish only ever
// center-crops — no resize, no rotation).
func newMapping(w, h, wf, hf int, fileFlip bool) (mapping, error) {
	dx, dy := w-wf, h-hf
	if dx < 0 || dy < 0 || dx%2 != 0 || dy%2 != 0 {
		return mapping{}, fmt.Errorf("%w: master %dx%d vs final %dx%d is not a symmetric center crop",
			ErrNoFinal, w, h, wf, hf)
	}
	return mapping{W: w, H: h, Wf: wf, Hf: hf, cx: dx / 2, cy: dy / 2, fileFlip: fileFlip}, nil
}

// rowOrderBottomUp reads the master's ROWORDER card (Siril writes BOTTOM-UP; this repo's own
// WriteFITS stamps TOP-DOWN). Absent card → top-down, matching the repo's file convention.
func rowOrderBottomUp(h *fits.Header) bool {
	v, ok := h.String("ROWORDER")
	return ok && strings.EqualFold(strings.TrimSpace(v), "BOTTOM-UP")
}

// wcsToFile maps a WCS-frame y to the file-row frame under the chosen wcsFlip.
func (m mapping) wcsToFile(x, y float64) (float64, float64) {
	if m.wcsFlip {
		return x, float64(m.H-1) - y
	}
	return x, y
}

// toFinal maps file-grid coordinates to final-image pixels; inFrame is false outside the crop.
func (m mapping) toFinal(xFile, yFile float64) (x, y float64, inFrame bool) {
	if m.fileFlip {
		yFile = float64(m.H-1) - yFile
	}
	x, y = xFile-float64(m.cx), yFile-float64(m.cy)
	inFrame = x >= 0 && x < float64(m.Wf) && y >= 0 && y < float64(m.Hf)
	return x, y, inFrame
}

// inWindow reports whether a file-grid peak lies inside the final crop window (flip-invariant —
// the window is centered, so the count never depends on orientation).
func (m mapping) inWindow(x, y int) bool {
	return x >= m.cx && x < m.W-m.cx && y >= m.cy && y < m.H-m.cy
}

// peakGrid hashes detected peaks into 8 px buckets for O(1) nearest-peak probes.
type peakGrid struct {
	cell  int
	byKey map[[2]int][]postprocess.StarPeak
}

func newPeakGrid(peaks []postprocess.StarPeak) *peakGrid {
	g := &peakGrid{cell: 8, byKey: make(map[[2]int][]postprocess.StarPeak, len(peaks))}
	for _, p := range peaks {
		k := [2]int{p.X / g.cell, p.Y / g.cell}
		g.byKey[k] = append(g.byKey[k], p)
	}
	return g
}

// nearest returns the closest peak within tol px of (x,y) in the file grid.
func (g *peakGrid) nearest(x, y, tol float64) (postprocess.StarPeak, bool) {
	cx, cy := int(x)/g.cell, int(y)/g.cell
	best, bestD := postprocess.StarPeak{}, tol*tol
	found := false
	for gy := cy - 1; gy <= cy+1; gy++ {
		for gx := cx - 1; gx <= cx+1; gx++ {
			for _, p := range g.byKey[[2]int{gx, gy}] {
				dx, dy := float64(p.X)-x, float64(p.Y)-y
				if d := dx*dx + dy*dy; d <= bestD {
					best, bestD, found = p, d, true
				}
			}
		}
	}
	return best, found
}

// probeStar is one bright catalogue star's WCS-frame projection used for flip validation.
type probeStar struct{ x, y float64 }

// chooseFlip decides the WCS-y orientation empirically: project the brightest catalogue stars
// under identity and y-flip and score matches against the detected peaks. The winner must clear
// an absolute floor AND double the loser — anything less means the chain (solve, projection,
// crop, detection) does not line up, and labels must not be emitted.
func chooseFlip(m *mapping, grid *peakGrid, probes []probeStar) (matched, tried int, ok bool) {
	tried = len(probes)
	score := func(flip bool) int {
		n := 0
		for _, p := range probes {
			mm := *m
			mm.wcsFlip = flip
			x, y := mm.wcsToFile(p.x, p.y)
			if _, found := grid.nearest(x, y, matchTolPx); found {
				n++
			}
		}
		return n
	}
	identity, flipped := score(false), score(true)
	winner, loser, flip := identity, flipped, false
	if flipped > identity {
		winner, loser, flip = flipped, identity, true
	}
	need := flipProbeThreshold(tried)
	if need == 0 || winner < need || winner < 2*loser {
		return winner, tried, false
	}
	m.wcsFlip = flip
	return winner, tried, true
}

// flipProbeThreshold is the minimum match count for a probe set of n stars (0 = unverifiable).
// The bar scales with the field: a galaxy field near the pole may hold only 6–8 catalogue stars
// bright enough to probe, and demanding min(5, n) there made sparse fields permanently
// `validation_failed` (task: M81/M82 solved with a perfect WCS but 3/7 matches — labels never
// shown). 40% of the probes, floored at 3 and capped at the historical 5, keeps the real
// disambiguation with chooseFlip's 2×-dominance rule while letting sparse fields validate.
func flipProbeThreshold(n int) int {
	if n < 3 {
		return 0
	}
	need := int(math.Ceil(0.4 * float64(n)))
	if need < 3 {
		need = 3
	}
	if need > 5 {
		need = 5
	}
	return need
}
