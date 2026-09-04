// Panel segmentation and lunar-surface canvas assembly.
//
// The single-reference lucky-imaging stack assumes every frame looks at the SAME patch of the
// subject: it registers each frame onto one sharpest reference over a ±64 px coarse search. That
// holds for a tracked planetary capture and breaks completely for two very common lunar cases —
//
//   - a hand-swept phone or camera at high magnification, where the Moon is far larger than the
//     field and drifts right across it during the clip (a 4-minute sweep can cross the whole disc);
//   - a DSLR re-pointed between bursts to cover a Moon that overflows the frame.
//
// In both, frames minutes apart share no pixels at all, so there is no reference they can all be
// registered onto — the stack either collapses to whatever overlapped the reference or averages
// misaligned surface into mush. What those runs want is not one stack but SEVERAL: cut the sweep
// into panels that each do share a field, stack each panel with the existing lucky-imaging core, and
// merge the panel masters onto one canvas larger than any single frame.
//
// Segmentation is measured, never assumed: consecutive frames are cross-correlated to build the
// capture's own drift trajectory, and a run whose total drift stays inside driftSinglePanelFrac of
// the frame is left ALONE — it takes the historical single-panel path and its master is unchanged.
package planetary

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"

	"github.com/verove-jordan/astronomy/internal/comet"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/siril"
)

const (
	// driftSinglePanelFrac: a capture whose total drift stays within this fraction of the smaller
	// frame axis is one panel. 0.30 keeps a generous common field (≥70% of the frame is seen by
	// every frame) and is well clear of the ±64 px the single-reference aligner already handles.
	driftSinglePanelFrac = 0.30
	// panelDriftFrac is the drift a single panel is allowed to span, as a fraction of the smaller
	// frame axis. Frames within one panel therefore always share ≥(1−panelDriftFrac) of the field.
	panelDriftFrac = 0.30
	// panelStepFrac is how far the panel anchor advances between consecutive panels. Smaller than
	// panelDriftFrac so neighbouring panels OVERLAP, which is what the canvas blend needs.
	panelStepFrac = 0.18
	// minPanelFrames: a panel with fewer frames than this is merged into its neighbour rather than
	// stacked on its own — too few frames to lucky-image and it would only add a noisy seam.
	minPanelFrames = 8
	// maxPanels caps the segmentation so a pathological trajectory cannot spawn hundreds of stacks.
	// A long phone sweep legitimately needs dozens (a 10,000 px drift on a 2,160 px frame is ~26
	// panels), so the cap is high and segmentDrift WIDENS its step to fit rather than truncating —
	// silently dropping the tail of a sweep would lose exactly the surface the user swept to get.
	maxPanels = 80
	// driftTrackDim is the long-axis size the drift tracker works at. Correlating downsampled planes
	// is what makes tracking thousands of frames affordable; the trajectory only needs to be good
	// enough to GROUP frames, since each panel's own stack re-registers to sub-pixel accuracy.
	driftTrackDim = 384
	// driftSearchFrac is the FIRST step's search range as a fraction of the tracking plane's smaller
	// axis — generous, because nothing is known yet about how fast the capture is moving.
	driftSearchFrac = 0.25
	// driftSeededShift is the search radius, in tracking-plane pixels, once a velocity is known. A
	// sweep is continuous: predicting the next step from the last one turns an O(search²) brute-force
	// hunt into a small residual search, which is the difference between tracking a 1,500-frame clip
	// in seconds and not being able to track it at all. A step whose correlation comes out poor is
	// re-measured with the full-width search, so an actual jump is still caught.
	driftSeededShift = 10
	// driftMinCorr is the correlation below which a step is treated as unmeasurable (cloud, a frame
	// that lost the subject) and the trajectory coasts on the previous velocity instead.
	driftMinCorr = 0.25
	// driftGoodCorr is what a step must score to be believed WITHOUT trying harder. A real surface
	// match scores high; a repetitive texture will happily return a spurious peak that still clears
	// driftMinCorr, which is how a large re-point gets silently read as "barely moved". Anything
	// under this triggers the coarse recovery, and the better-correlating of the two answers wins.
	driftGoodCorr = 0.6
	// discRadiusTol is how far a frame's fitted radius may sit from the run's median before the fit
	// is discarded. The Moon's angular size is fixed over a session, so this is a physical
	// consistency check, not a tuning knob.
	discRadiusTol = 0.06
	// anchorTolBase/anchorTolFrac bound how far a limb anchor may sit from what the correlation chain
	// predicts before it is treated as a bad fit rather than a measurement.
	anchorTolBase = 40.0
	anchorTolFrac = 0.20
	// anchorRegimeFrac is the share of frames that must carry an accepted limb anchor before the
	// trajectory is built from anchors alone rather than from correlation.
	anchorRegimeFrac = 0.30
	// discArcMin is the limb arc (degrees) a disc fit must cover before its centre is trusted as an
	// ABSOLUTE position. Well under a full circle — a frame the Moon overflows shows only part of the
	// limb — but enough that the circle is constrained rather than fitted to a nearly-straight edge.
	discArcMin = 60
	// recoverDim is the long-axis size of the COARSE plane used to recover a large discrete jump.
	// A re-pointed DSLR burst series moves the subject by a large fraction of the frame between
	// bursts — far more than the wide search covers — and a jump the tracker misses does not merely
	// lose one step: every later frame inherits the error, so two different pointings get merged
	// into one panel and stacked on top of each other. At this scale a brute-force search over the
	// WHOLE frame costs almost nothing, so the recovery is simply always available.
	recoverDim = 96
	// recoverMinOverlap: half of each axis, for jump recovery.
	recoverMinOverlap = 0.5
)

// driftPoint is one frame's cumulative position of the SUBJECT within the field, in full-resolution
// pixels, relative to the first frame. corr is the correlation that produced the step into it.
type driftPoint struct {
	X, Y float64
	Corr float64
}

// trackDrift measures the capture's own drift trajectory by cross-correlating consecutive frames on
// downsampled planes. Returns one point per input frame (the first is the origin). A frame that
// fails to read, or whose correlation falls below driftMinCorr, coasts on the previous step's
// velocity so one bad frame cannot break the trajectory into two panels.
func trackDrift(ctx context.Context, paths []string, tick func(done, total int)) ([]driftPoint, int, int, error) {
	if len(paths) == 0 {
		return nil, 0, 0, fmt.Errorf("no frames to track")
	}
	first, err := fits.ReadImage(paths[0])
	if err != nil {
		return nil, 0, 0, fmt.Errorf("read %s: %w", paths[0], err)
	}
	fullW, fullH := first.W, first.H
	step := downStepFor(fullW, fullH)
	prev := lumaDown(first, step)
	if prev == nil {
		return nil, 0, 0, fmt.Errorf("frame has no usable plane")
	}
	radius := min(prev.W, prev.H) / 4
	wide := float64(min(prev.W, prev.H)) * driftSearchFrac
	center := comet.Point{X: float64(prev.W) / 2, Y: float64(prev.H) / 2}

	// One pass: per frame, measure BOTH the limb (an absolute anchor) and the correlation step from
	// its predecessor (a relative fallback). Which of the two to believe cannot be decided here —
	// see the radius filter below — so both are kept.
	type meas struct {
		cx, cy, r    float64
		hasFit       bool
		stepX, stepY float64
		stepOK       bool
		stepStrong   bool // measured at driftGoodCorr or better, i.e. not coasted or recovered
	}
	ms := make([]meas, len(paths))
	if cx, cy, r, ok := trackerDisc(first); ok {
		ms[0] = meas{cx: cx, cy: cy, r: r, hasFit: true}
	}
	var vx, vy float64
	haveV := false
	for i := 1; i < len(paths); i++ {
		if err := ctx.Err(); err != nil {
			return nil, 0, 0, err
		}
		im, rerr := fits.ReadImage(paths[i])
		var cur *fits.Image
		if rerr == nil {
			cur = lumaDown(im, step)
			if cx, cy, r, ok := trackerDisc(im); ok {
				ms[i] = meas{cx: cx, cy: cy, r: r, hasFit: true}
			}
		}
		if cur != nil && cur.W == prev.W && cur.H == prev.H {
			sx, sy, corr := 0.0, 0.0, 0.0
			if haveV {
				sx, sy = comet.AlignSeeded(prev, cur, center, radius, driftSeededShift, 1, vx, vy)
				corr = znccAt(prev, cur, center, radius, sx, sy)
			}
			if !haveV || corr < driftGoodCorr {
				sx, sy = comet.AlignSeeded(prev, cur, center, radius, wide, 1, 0, 0)
				corr = znccAt(prev, cur, center, radius, sx, sy)
			}
			// A peak sitting ON the search boundary is not a peak, it is the edge of the window: the
			// real one is further out. Saturation must therefore force the recovery even when the
			// clipped match happens to correlate well, or a large jump is silently reported as
			// exactly the search radius.
			saturated := math.Abs(sx) >= wide-1 || math.Abs(sy) >= wide-1
			if corr < driftGoodCorr || saturated {
				if rx, ry, ok := coarseRecover(prev, cur); ok {
					refine := math.Max(driftSeededShift, float64(step)*2)
					gx, gy := comet.AlignSeeded(prev, cur, center, radius, refine, 1, rx, ry)
					// A saturated match is discarded outright rather than compared: it scores the
					// wrong shift honestly, so a fair comparison can still prefer it.
					if c := znccAt(prev, cur, center, radius, gx, gy); c > corr || saturated {
						sx, sy, corr = gx, gy, c
					}
				}
			}
			if corr >= driftMinCorr {
				// The step is the SUBJECT's motion, the negation of the shift that registers the new
				// frame onto the old one.
				ms[i].stepX, ms[i].stepY, ms[i].stepOK = -sx*float64(step), -sy*float64(step), true
				ms[i].stepStrong = corr >= driftGoodCorr
				vx, vy, haveV = sx, sy, true
			} else if haveV {
				// Unmeasurable (cloud, a frame that lost the subject): COAST at the last velocity
				// rather than stall. A sweep is continuous, so the last step is a better estimate of
				// this one than zero is, and stalling would bend the whole trajectory.
				ms[i].stepX, ms[i].stepY, ms[i].stepOK = -vx*float64(step), -vy*float64(step), true
			}
			prev = cur
		}
		if tick != nil {
			tick(i+1, len(paths))
		}
	}

	// The Moon's radius does not change during a capture, so a fit that disagrees with the run's own
	// median radius is a MIS-fit, not a measurement — and a mis-fit trusted as an absolute anchor is
	// far more damaging than no anchor at all, because it teleports the trajectory. Measured on a real
	// 60-frame burst series: most frames fitted r≈2685 px, a handful returned 3012 and 3408, and those
	// few were enough to turn a 2,750 px trajectory into a 59,000 px one.
	var radii []float64
	for _, m := range ms {
		if m.hasFit {
			radii = append(radii, m.r)
		}
	}
	medR := 0.0
	if len(radii) > 0 {
		sort.Float64s(radii)
		medR = radii[len(radii)/2]
	}
	for i := range ms {
		if ms[i].hasFit && (medR <= 0 || math.Abs(ms[i].r-medR) > discRadiusTol*medR) {
			ms[i].hasFit = false
		}
	}
	// Cross-check every anchor against the correlation chain, and drop the ones that disagree.
	//
	// A circle fitted to a SHORT arc is ill-conditioned: the centre slides along the arc's normal, so
	// as the Moon crosses the field and a different piece of limb comes into view, the fitted centre
	// wanders even when the radius looks right. Those anchors are absolute AND wrong, which is the
	// worst combination — each one teleports the trajectory. Consecutive-frame correlation, by
	// contrast, is reliable over a small step but accumulates. Using each to police the other is what
	// makes the pair trustworthy: an anchor is kept only if it lands where the chain from the last
	// accepted anchor predicts, within a tolerance that grows with how far the chain has run.
	lastOK := -1
	var chainX, chainY float64
	chainTrusted := true
	for i := range ms {
		if ms[i].stepOK {
			chainX += ms[i].stepX
			chainY += ms[i].stepY
			chainTrusted = chainTrusted && ms[i].stepStrong
		} else {
			chainTrusted = false
		}
		if !ms[i].hasFit {
			continue
		}
		if lastOK < 0 {
			lastOK, chainX, chainY, chainTrusted = i, 0, 0, true
			continue
		}
		// Which one is lying? An anchor that disagrees with the chain is only evidence against the
		// ANCHOR while the chain itself is solid — every step since the last accepted anchor measured
		// at full confidence. If any step in between was coasted, recovered or weak, the chain is the
		// unreliable one and the anchor is exactly what is needed to repair it. This is the difference
		// between a smooth sweep (correlation excellent, short-arc fits wander → keep the chain) and a
		// re-pointed burst series (correlation cannot bridge the jump → keep the anchor).
		predX, predY := ms[lastOK].cx+chainX, ms[lastOK].cy+chainY
		tol := anchorTolBase + anchorTolFrac*math.Hypot(chainX, chainY)
		if chainTrusted && math.Hypot(ms[i].cx-predX, ms[i].cy-predY) > tol {
			ms[i].hasFit = false
			continue
		}
		lastOK, chainX, chainY, chainTrusted = i, 0, 0, true
	}

	// Two regimes, and mixing them is what fails. When the limb is visible on most frames the
	// trajectory is built from ANCHORS ALONE — absolute positions, with the few unanchored frames
	// linearly interpolated between their neighbouring anchors. Splicing correlation into an
	// otherwise-anchored run puts an accumulating estimate next to absolute ones, and a single bad
	// step then carries the whole segment away from the anchors around it (measured: gaps filled by
	// correlation landed 4,177 px down a 3,464 px frame). A capture with too few anchors — a
	// deep-terminator close-up that never shows a limb — has nothing to interpolate and falls back to
	// pure correlation accumulation, which is all it can do.
	out := make([]driftPoint, len(paths))
	var anchors []int
	for i, m := range ms {
		if m.hasFit {
			anchors = append(anchors, i)
		}
	}
	if len(anchors) >= 2 && float64(len(anchors)) >= anchorRegimeFrac*float64(len(paths)) {
		ref := anchors[0]
		abs := func(i int) driftPoint {
			return driftPoint{X: ms[i].cx - ms[ref].cx, Y: ms[i].cy - ms[ref].cy, Corr: 1}
		}
		for _, ai := range anchors {
			out[ai] = abs(ai)
		}
		// Between two anchors, correlation supplies the SHAPE and the anchors supply the TRUTH: the
		// chain is walked from the left anchor and its closure error against the right anchor is
		// distributed linearly across the run. Chaining alone lets one bad step run away (measured:
		// 4,177 px down a 3,464 px frame); interpolation alone flattens a burst that genuinely moved
		// between two anchors. Closing the chain on the anchors keeps both ends exact and bounds
		// everything in between.
		fill := func(a, b int) {
			cx, cy := 0.0, 0.0
			chain := make([][2]float64, 0, b-a)
			for i := a + 1; i <= b; i++ {
				if ms[i].stepOK {
					cx += ms[i].stepX
					cy += ms[i].stepY
				}
				chain = append(chain, [2]float64{cx, cy})
			}
			errX, errY := 0.0, 0.0
			if b < len(out) && len(chain) > 0 {
				errX = (out[b].X - out[a].X) - chain[len(chain)-1][0]
				errY = (out[b].Y - out[a].Y) - chain[len(chain)-1][1]
			}
			n := float64(len(chain))
			for k, c := range chain {
				i := a + 1 + k
				if i >= b {
					break
				}
				t := float64(k+1) / n
				out[i] = driftPoint{X: out[a].X + c[0] + t*errX, Y: out[a].Y + c[1] + t*errY, Corr: 0.5}
			}
		}
		for k := 1; k < len(anchors); k++ {
			fill(anchors[k-1], anchors[k])
		}
		// Outside the anchored span there is nothing to close against: chain forward, hold backward.
		last := anchors[len(anchors)-1]
		for i := last + 1; i < len(out); i++ {
			out[i] = out[i-1]
			out[i].Corr = 0.5
			if ms[i].stepOK {
				out[i].X += ms[i].stepX
				out[i].Y += ms[i].stepY
			}
		}
		for i := anchors[0] - 1; i >= 0; i-- {
			out[i] = out[i+1]
			out[i].Corr = 0.5
			if ms[i+1].stepOK {
				out[i].X -= ms[i+1].stepX
				out[i].Y -= ms[i+1].stepY
			}
		}
	} else {
		for i := range out {
			switch {
			case i == 0:
			case ms[i].stepOK:
				out[i] = driftPoint{X: out[i-1].X + ms[i].stepX, Y: out[i-1].Y + ms[i].stepY, Corr: 0.5}
			default:
				out[i] = out[i-1]
			}
		}
	}
	// Re-baseline so the first frame is the origin, which is what the callers assume.
	if base := out[0]; base.X != 0 || base.Y != 0 {
		for i := range out {
			out[i].X -= base.X
			out[i].Y -= base.Y
		}
	}
	return out, fullW, fullH, nil
}

// downStepFor picks the integer decimation that brings the long axis near driftTrackDim.
func downStepFor(w, h int) int {
	long := max(w, h)
	step := long / driftTrackDim
	if step < 1 {
		step = 1
	}
	return step
}

// lumaDown box-decimates the image's luminance by step into a single-plane image.
func lumaDown(im *fits.Image, step int) *fits.Image {
	if im == nil || im.C == 0 || im.W < step || im.H < step {
		return nil
	}
	w, h := im.W/step, im.H/step
	if w <= 0 || h <= 0 {
		return nil
	}
	out := fits.NewImage(w, h, 1)
	inv := float32(1) / float32(step*step*im.C)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var s float32
			for dy := 0; dy < step; dy++ {
				row := (y*step + dy) * im.W
				for dx := 0; dx < step; dx++ {
					for ch := 0; ch < im.C; ch++ {
						s += im.Pix[ch][row+x*step+dx]
					}
				}
			}
			out.Pix[0][y*w+x] = s * inv
		}
	}
	return out
}

// znccAt scores how well target matches ref at shift (dx,dy) over the window — the measured
// confidence of one tracking step.
//
// The sampling convention is comet.zncc's and must stay that way: ref(x,y) is compared with
// target(x−dx, y−dy), because AlignSeeded defines its shift as the translation that registers
// TARGET ONTO REF. Sampling target(x+dx) instead scores the correct shift as if it were wrong, and
// every step would silently fall back to coasting.
func znccAt(ref, target *fits.Image, center comet.Point, radius int, dx, dy float64) float64 {
	if ref == nil || target == nil {
		return 0
	}
	x0, y0 := int(center.X)-radius, int(center.Y)-radius
	x1, y1 := int(center.X)+radius, int(center.Y)+radius
	var sa, sb, saa, sbb, sab float64
	n := 0
	for y := y0; y <= y1; y++ {
		ty := y - int(math.Round(dy))
		if y < 0 || y >= ref.H || ty < 0 || ty >= target.H {
			continue
		}
		for x := x0; x <= x1; x++ {
			tx := x - int(math.Round(dx))
			if x < 0 || x >= ref.W || tx < 0 || tx >= target.W {
				continue
			}
			a := float64(ref.Pix[0][y*ref.W+x])
			b := float64(target.Pix[0][ty*target.W+tx])
			sa += a
			sb += b
			saa += a * a
			sbb += b * b
			sab += a * b
			n++
		}
	}
	if n < 64 {
		return 0
	}
	fn := float64(n)
	ca, cb := saa-sa*sa/fn, sbb-sb*sb/fn
	if ca <= 0 || cb <= 0 {
		return 0
	}
	return (sab - sa*sb/fn) / math.Sqrt(ca*cb)
}

// panelSpan is one segmented panel: the indices of the frames that belong to it and the trajectory
// position of its anchor (the median of its members), in full-resolution pixels.
type panelSpan struct {
	Idx    []int
	AnchoX float64
	AnchoY float64
}

// segmentDrift cuts a trajectory into panels. Frames are walked in capture order and appended to the
// current panel while they stay within panelDriftFrac of the panel's first frame; the next panel
// re-anchors panelStepFrac along, so consecutive panels overlap. A trailing panel shorter than
// minPanelFrames is folded back into its predecessor.
func segmentDrift(drift []driftPoint, w, h int) []panelSpan {
	if len(drift) == 0 {
		return nil
	}
	axis := float64(min(w, h))
	limit := axis * panelDriftFrac
	stepDist := axis * panelStepFrac
	// A sweep longer than maxPanels×stepDist would run off the end of the cap. Widen the step so the
	// panels still span the WHOLE trajectory (they overlap less), clamped short of `limit` so
	// consecutive panels always keep some overlap for the canvas to register and blend across.
	if span := driftSpan(drift); span > stepDist*float64(maxPanels-1) {
		stepDist = span / float64(maxPanels-1)
		if stepDist > limit*0.9 {
			stepDist = limit * 0.9
		}
	}
	var panels []panelSpan
	i := 0
	for i < len(drift) && len(panels) < maxPanels {
		ax, ay := drift[i].X, drift[i].Y
		var idx []int
		nextStart := -1
		for j := i; j < len(drift); j++ {
			d := math.Hypot(drift[j].X-ax, drift[j].Y-ay)
			if d > limit && len(idx) > 0 {
				break
			}
			idx = append(idx, j)
			if nextStart < 0 && d >= stepDist {
				nextStart = j
			}
		}
		panels = append(panels, panelSpan{Idx: idx, AnchoX: medianAxis(drift, idx, true), AnchoY: medianAxis(drift, idx, false)})
		if nextStart <= i {
			if idx[len(idx)-1]+1 <= i {
				break
			}
			nextStart = idx[len(idx)-1] + 1
		}
		i = nextStart
	}
	// A runt tail — a final panel with too few frames to lucky-image — is RE-ANCHORED on the last
	// frame rather than merged into its predecessor. Merging looks simpler but silently breaks the
	// invariant the whole segmentation exists to keep: the predecessor would then span more than
	// panelDriftFrac, so its own frames no longer share a field and its stack is the very smear the
	// panels were meant to prevent. Re-anchoring slides the last panel back until it is full, which
	// costs only extra overlap with its neighbour.
	if len(panels) > 1 && len(panels[len(panels)-1].Idx) < minPanelFrames {
		end := len(drift) - 1
		k := end
		for k > 0 && math.Hypot(drift[k-1].X-drift[end].X, drift[k-1].Y-drift[end].Y) <= limit {
			k--
		}
		if end-k+1 >= minPanelFrames {
			idx := make([]int, 0, end-k+1)
			for j := k; j <= end; j++ {
				idx = append(idx, j)
			}
			panels[len(panels)-1] = panelSpan{Idx: idx, AnchoX: medianAxis(drift, idx, true), AnchoY: medianAxis(drift, idx, false)}
		} else {
			// Frames are so sparse near the end that even a full-width window cannot fill a panel.
			// Covering them beats the budget here: merge, and accept the wider span.
			last := panels[len(panels)-1]
			panels = panels[:len(panels)-1]
			prev := &panels[len(panels)-1]
			seen := make(map[int]bool, len(prev.Idx))
			for _, v := range prev.Idx {
				seen[v] = true
			}
			for _, v := range last.Idx {
				if !seen[v] {
					prev.Idx = append(prev.Idx, v)
				}
			}
			sort.Ints(prev.Idx)
		}
	}
	// Drop a mid-sequence runt: a panel of one or two frames is a stray the trajectory placed away
	// from its neighbours, and stacking it produces a noisy patch the blend then has to carry. Its
	// surface is covered by the panels around it. (The TAIL runt is handled above by re-anchoring,
	// which keeps the end of the sweep.)
	if len(panels) > 1 {
		kept := panels[:0:0]
		for _, p := range panels {
			if len(p.Idx) >= minPanelFrames {
				kept = append(kept, p)
			}
		}
		if len(kept) >= 2 {
			panels = kept
		}
	}
	return panels
}

func medianAxis(drift []driftPoint, idx []int, useX bool) float64 {
	if len(idx) == 0 {
		return 0
	}
	v := make([]float64, 0, len(idx))
	for _, i := range idx {
		if useX {
			v = append(v, drift[i].X)
		} else {
			v = append(v, drift[i].Y)
		}
	}
	sort.Float64s(v)
	return v[len(v)/2]
}

// driftSpan is the trajectory's total extent (the diagonal of its bounding box), in full pixels.
func driftSpan(drift []driftPoint) float64 {
	if len(drift) == 0 {
		return 0
	}
	minX, maxX := drift[0].X, drift[0].X
	minY, maxY := drift[0].Y, drift[0].Y
	for _, p := range drift[1:] {
		minX, maxX = math.Min(minX, p.X), math.Max(maxX, p.X)
		minY, maxY = math.Min(minY, p.Y), math.Max(maxY, p.Y)
	}
	return math.Hypot(maxX-minX, maxY-minY)
}

// stackChannel stacks one channel. It first MEASURES the capture's drift: a run whose subject stays
// put takes the historical single-panel path (byte-identical master), while a swept or re-pointed
// run is segmented into panels, each stacked by the same lucky-imaging core, and the panel masters
// are merged onto one canvas larger than any single frame.
//
// Every failure past segmentation soft-falls to the single-panel stack: a mosaic that cannot be
// assembled must not cost the user the ordinary result.
func stackChannel(ctx context.Context, runner *siril.Runner, frames []string, filter, workAbs, object string,
	opts Options, prog *runProgress, chSpan float64, onProgress func(siril.Progress)) (string, []FrameScore, int, []string, error) {
	if len(frames) == 0 {
		return "", nil, 0, nil, fmt.Errorf("no frames")
	}
	single := func() (string, []FrameScore, int, []string, error) {
		return stackPanelFrames(ctx, runner, frames, filter, "", 0, workAbs, object, opts, prog, chSpan, nil, onProgress)
	}
	if !opts.Mosaic || len(frames) < 2*minPanelFrames {
		return single()
	}
	// Drift tracking gets a small slice of the channel budget; it reads every frame once, decimated.
	trackSpan := chSpan * phaseDrift
	prog.phase(trackSpan)
	report(onProgress, "measuring drift "+channelLabel(filter))
	drift, w, h, err := trackDrift(ctx, frames, prog.tick)
	if err != nil {
		m, rep, kept, snotes, serr := single()
		return m, rep, kept, append([]string{"drift tracking failed, stacking as one panel: " + err.Error()}, snotes...), serr
	}
	span := driftSpan(drift)
	anchored := 0
	for _, d := range drift {
		if d.Corr >= 1 {
			anchored++
		}
	}
	axis := float64(min(w, h))
	report(onProgress, fmt.Sprintf("drift %.0f px, %d/%d frames limb-anchored", span, anchored, len(drift)))
	if span <= axis*driftSinglePanelFrac {
		master, rep, kept, notes, serr := stackPanelFrames(ctx, runner, frames, filter, "", 0, workAbs, object,
			opts, prog, chSpan-trackSpan, drift, onProgress)
		notes = append(notes, fmt.Sprintf("%s: drift %.0f px over the run (%.0f%% of the frame) — one panel",
			channelLabel(filter), span, 100*span/axis))
		return master, rep, kept, notes, serr
	}
	spans := segmentDrift(drift, w, h)
	if len(spans) < 2 {
		return single()
	}
	notes := []string{fmt.Sprintf("%s: subject drifts %.0f px (%.0f%% of the frame), %d/%d frames limb-anchored — segmented into %d panels",
		channelLabel(filter), span, 100*span/axis, anchored, len(drift), len(spans))}

	scale := SnapDrizzle(opts.DrizzleScale)
	panelSpanEach := (chSpan - trackSpan) * 0.92 / float64(len(spans))
	var masters, labels []string
	var seeds []struct{ X, Y float64 }
	var panelReport []FrameScore
	stackedTotal := 0
	for pi, ps := range spans {
		sub := make([]string, 0, len(ps.Idx))
		subTraj := make([]driftPoint, 0, len(ps.Idx))
		for _, i := range ps.Idx {
			sub = append(sub, frames[i])
			subTraj = append(subTraj, drift[i])
		}
		tag := fmt.Sprintf("p%02d", pi+1)
		m, rep, kept, pnotes, perr := stackPanelFrames(ctx, runner, sub, filter, tag, ps.Idx[0], workAbs, object,
			opts, prog, panelSpanEach, subTraj, onProgress)
		if perr != nil {
			notes = append(notes, fmt.Sprintf("panel %s failed (%v) — skipped", tag, perr))
			continue
		}
		masters = append(masters, m+".fits")
		labels = append(labels, tag)
		// Drop this panel's aligned frames as soon as its master exists. A swept 4K clip segments
		// into dozens of panels and each aligned frame is ~50 MB of FITS; keeping them all until the
		// end of the channel would need hundreds of gigabytes of scratch for a run that only ever
		// needs one panel's worth at a time.
		_ = os.RemoveAll(filepath.Join(filepath.Dir(m), "aligned"))
		// The panel masters live on the drizzle grid, so the trajectory (measured in native pixels)
		// scales with it. Sign: the trajectory tracks where the SUBJECT sits in the field, so a panel
		// whose subject has moved +d shows surface that belongs at −d on the canvas.
		seeds = append(seeds, struct{ X, Y float64 }{X: -ps.AnchoX * scale, Y: -ps.AnchoY * scale})
		panelReport = append(panelReport, rep...)
		stackedTotal += kept
		notes = append(notes, pnotes...)
	}
	if len(masters) < 2 {
		notes = append(notes, "fewer than two panels survived — stacking the run as one panel instead")
		m, rep, kept, snotes, serr := single()
		return m, rep, kept, append(notes, snotes...), serr
	}
	prog.phase((chSpan - trackSpan) * 0.08)
	report(onProgress, "assembling lunar canvas")
	canvas, cnotes, aerr := assemblePanels(ctx, masters, labels, seeds)
	if aerr != nil {
		notes = append(notes, "canvas assembly failed, stacking as one panel: "+aerr.Error())
		m, rep, kept, snotes, serr := single()
		return m, rep, kept, append(notes, snotes...), serr
	}
	notes = append(notes, cnotes...)
	out := filepath.Join(workAbs, "planetary_"+object, "ch_"+channelLabel(filter)+"_canvas", "master_"+channelLabel(filter))
	if err := fsutil.EnsureDir(filepath.Dir(out)); err != nil {
		return "", panelReport, 0, notes, err
	}
	normalize(canvas, stackNormPct)
	if err := canvas.WriteFITS(out + ".fits"); err != nil {
		return "", panelReport, 0, notes, err
	}
	return out, panelReport, stackedTotal, notes, nil
}

// coarseRecover brute-force searches the WHOLE frame for the displacement between two tracking-scale
// planes, on a further-decimated copy. It exists for the discrete jump a re-pointed capture makes
// between bursts, which is far larger than any seeded search covers. The returned shift is in
// tracking-plane pixels (ok=false when the planes are too small to decimate usefully).
func coarseRecover(prev, cur *fits.Image) (dx, dy float64, ok bool) {
	step := max(prev.W, prev.H) / recoverDim
	if step < 2 {
		return 0, 0, false
	}
	a, b := lumaDown(prev, step), lumaDown(cur, step)
	if a == nil || b == nil || a.W != b.W || a.H != b.H {
		return 0, 0, false
	}
	// Reach: +/-0.75·min(W,H) in FULL pixels (the decimation cancels). Sized from real data, not
	// intuition: a re-pointed lunar burst series moved the subject 2,540 px on a 3,464 px short axis
	// — past a half-axis reach, yet the two pointings still shared a third of their height and were
	// perfectly recoverable. shiftByOverlap's own quarter-frame overlap guard is what rejects a
	// shift that really is too far, so the reach can afford to be generous.
	reach := min(a.W, a.H) * 3 / 4
	cx, cy, corr := shiftByOverlap(a, b, 0, 0, reach, recoverMinOverlap)
	if corr < driftGoodCorr {
		return 0, 0, false
	}
	return float64(cx) * float64(step), float64(cy) * float64(step), true
}

// shiftByOverlap finds the integer shift maximizing ZNCC computed over the two planes' ACTUAL
// OVERLAP, scanning +/-reach on both axes.
//
// A fixed window centred on the frame — which is what the per-frame aligner uses, correctly, for
// small residual shifts — cannot localize a large displacement: at a shift of half the frame most of
// that window has fallen outside the image, so the few pixels left score noise and the peak lands
// near zero shift. Scoring the overlap instead keeps every candidate shift on equal, fully-populated
// footing, which is what makes a large re-point findable at all.
//
// It takes a SEED and compares the two images directly rather than accepting a pre-shifted plane:
// pre-shifting pads with zeros, and since this scores the whole overlap rather than a window, that
// padding is most of what it would be scoring.
//
// minOverlap is the share of each axis that must still overlap for a candidate shift to be scored,
// and it is a caller's decision rather than a constant. Recovering a JUMP wants it high (a far-out
// shift matching on a sliver of repetitive surface is worse than finding nothing), while placing a
// mosaic PANEL wants it low — neighbouring panels are meant to share only their feathered margin.
func shiftByOverlap(a, b *fits.Image, seedX, seedY, reach int, minOverlap float64) (bx, by int, best float64) {
	best = -2
	bx, by = seedX, seedY
	for dy := seedY - reach; dy <= seedY+reach; dy++ {
		for dx := seedX - reach; dx <= seedX+reach; dx++ {
			// ref(x,y) vs target(x-dx, y-dy) — comet's convention.
			x0, x1 := max(0, dx), min(a.W, b.W+dx)
			y0, y1 := max(0, dy), min(a.H, b.H+dy)
			if float64(x1-x0) < minOverlap*float64(a.W) || float64(y1-y0) < minOverlap*float64(a.H) {
				continue
			}
			var sa, sb, saa, sbb, sab float64
			n := 0
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					va := float64(a.Pix[0][y*a.W+x])
					vb := float64(b.Pix[0][(y-dy)*b.W+(x-dx)])
					sa += va
					sb += vb
					saa += va * va
					sbb += vb * vb
					sab += va * vb
					n++
				}
			}
			if n < 64 {
				continue
			}
			fn := float64(n)
			ca, cb := saa-sa*sa/fn, sbb-sb*sb/fn
			if ca <= 0 || cb <= 0 {
				continue
			}
			if c := (sab - sa*sb/fn) / math.Sqrt(ca*cb); c > best {
				best, bx, by = c, dx, dy
			}
		}
	}
	return bx, by, best
}

// confidentDisc fits the lunar limb and accepts it only when enough arc voted for the circle. A fit
// from a short arc is a circle through nearly-collinear points: its centre can be wrong by more than
// the frame, which is worse than having no answer at all.
func confidentDisc(im *fits.Image) (DiscFit, bool) {
	cx, cy, r, ok := trackerDisc(im)
	if !ok || r <= 0 {
		return DiscFit{}, false
	}
	return DiscFit{CX: cx, CY: cy, R: r}, true
}

// Tracker-local limb fit, for a Moon LARGER than the frame.
//
// disc.go's fitLunarDisc is not reused here, and the reason is measured rather than stylistic. It is
// tuned for a disc that fits inside the frame, where the sunlit limb is most of the lit region's
// boundary and wins the RANSAC vote outright. When the Moon overflows the sensor — the case this
// whole file exists for — only a short arc of limb is in frame while the TERMINATOR runs the full
// height of it, so the terminator carries most of the boundary points and the vote goes to a circle
// fitted along it: on a real 5202x3464 Canon frame that produced r=5912 px against a true 2670, and
// the fit was then (correctly) rejected, leaving the tracker with nothing.
//
// So the selection changes, not the machinery: disc.go's own component, boundary, sharpness, Kasa
// and arc helpers are reused, and only two things differ — boundary points lying along the FRAME's
// own edge are dropped (a cut-off disc's border is not its limb), and the consensus is sought over
// randomized triples rather than a fixed seed lattice, which is what finds a minority arc.
const (
	trackDiscTolPx    = 2.0  // inlier tolerance, small px
	trackDiscIters    = 6000 // randomized triples
	trackDiscMinArc   = 60.0 // degrees of limb that must vote
	trackDiscEdgeSkip = 3    // ignore boundary within this many px of the frame edge
)

// trackerDisc fits the lunar limb for trajectory anchoring. Returns full-resolution coordinates.
func trackerDisc(im *fits.Image) (cx, cy, r float64, ok bool) {
	small := downPlane(im, discDown)
	scale := 1.0
	if small != im {
		scale = discDown
	}
	comp, count, ccx, ccy := litComponent(small)
	if count == 0 {
		return 0, 0, 0, false
	}
	pts := keepSharpest(dropFrameEdge(boundaryPoints(small, comp), small.W, small.H))
	if len(pts) < discMinInliers {
		return 0, 0, 0, false
	}
	sortByAngle(pts, ccx, ccy)
	rng := rand.New(rand.NewSource(1))
	var best circle
	bestInl := 0
	for it := 0; it < trackDiscIters; it++ {
		a, b, c := pts[rng.Intn(len(pts))], pts[rng.Intn(len(pts))], pts[rng.Intn(len(pts))]
		cir, okc := circumcircle(a, b, c)
		if !okc || !circleBounds(cir, small.W, small.H) {
			continue
		}
		n := 0
		for _, p := range pts {
			if math.Abs(math.Hypot(p.x-cir.cx, p.y-cir.cy)-cir.r) < trackDiscTolPx {
				n++
			}
		}
		if n > bestInl {
			best, bestInl = cir, n
		}
	}
	if bestInl < discMinInliers {
		return 0, 0, 0, false
	}
	refined, inliers, _, okr := refineCircle(pts, best)
	if !okr || len(inliers) < discMinInliers {
		return 0, 0, 0, false
	}
	if arc := float64(arcBinsCovered(inliers, refined)) * 360 / discArcBins; arc < trackDiscMinArc {
		return 0, 0, 0, false
	}
	return (refined.cx+0.5)*scale - 0.5, (refined.cy+0.5)*scale - 0.5, refined.r * scale, true
}

// dropFrameEdge removes boundary points running along the image border. Where the disc is cut off by
// the sensor, that border is the CROP, not the limb, and it is a long straight line — exactly the
// shape a circle fit is most eager to accommodate and most wrong to.
func dropFrameEdge(pts []edgePoint, w, h int) []edgePoint {
	out := pts[:0:0]
	for _, p := range pts {
		if p.x < trackDiscEdgeSkip || p.y < trackDiscEdgeSkip ||
			p.x >= float64(w-trackDiscEdgeSkip) || p.y >= float64(h-trackDiscEdgeSkip) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// frameSeed is a per-frame prior on the shift that registers that frame onto the alignment
// reference. Known=false means "no prior" and the aligner falls back to its brightness centroid.
type frameSeed struct {
	X, Y  float64
	Known bool
}

// seedAt is a bounds-safe lookup into an optional seed slice.
func seedAt(seeds []frameSeed, i int) frameSeed {
	if i < 0 || i >= len(seeds) {
		return frameSeed{}
	}
	return seeds[i]
}

// alignMinCorr is the correlation a frame must reach against the reference to be stacked at all.
// A frame below it did not lock onto the surface, and warping it by whatever the search returned
// deposits a displaced copy of the subject into the master — the "second Moon" a lucky-imaging stack
// must never contain. Dropping it costs a little SNR; keeping it costs the whole image.
const alignMinCorr = 0.55

// rejectMislocked blanks the measured field of every frame whose alignment correlation is below
// alignMinCorr so sweep 2 skips it, and returns how many were dropped. The reference is never
// dropped. All-frames-below is treated as "the threshold is wrong for this data, not every frame" —
// the best-correlating half is kept rather than returning an empty stack.
func rejectMislocked(dx, dy [][]float64, corr []float64, refIdx int) int {
	ok := 0
	for i := range corr {
		if i == refIdx || (dx[i] != nil && corr[i] >= alignMinCorr) {
			ok++
		}
	}
	if ok < 2 && len(corr) > 2 {
		med := append([]float64(nil), corr...)
		sort.Float64s(med)
		floor := med[len(med)/2]
		dropped := 0
		for i := range corr {
			if i != refIdx && dx[i] != nil && corr[i] < floor {
				dx[i], dy[i] = nil, nil
				dropped++
			}
		}
		return dropped
	}
	dropped := 0
	for i := range corr {
		if i == refIdx || dx[i] == nil {
			continue
		}
		if corr[i] < alignMinCorr {
			dx[i], dy[i] = nil, nil
			dropped++
		}
	}
	return dropped
}

// seedsFromTrajectory converts measured subject positions into per-frame alignment priors, expressed
// against the frame the aligner will pick as its reference (the sharpest one, argmax of scores).
//
// Sign: the trajectory records where the SUBJECT sits in the field, so a frame whose subject has
// moved +d relative to the reference holds the reference's content at +d — and comet.AlignSeeded
// defines its shift s by ref(x) == target(x−s), which makes s = −d.
func seedsFromTrajectory(traj []driftPoint, scores []float64) []frameSeed {
	if len(traj) != len(scores) || len(traj) == 0 {
		return nil
	}
	ref := argmax(scores)
	if ref < 0 || ref >= len(traj) {
		return nil
	}
	out := make([]frameSeed, len(traj))
	for i := range traj {
		out[i] = frameSeed{X: -(traj[i].X - traj[ref].X), Y: -(traj[i].Y - traj[ref].Y), Known: true}
	}
	return out
}
