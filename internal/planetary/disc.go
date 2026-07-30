package planetary

import (
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// Lunar-disc limb fit. The earthshine composite needs the FULL disc geometry (centre + radius)
// while only the lit part of the Moon is bright. The limb (surface→sky edge) is a sharp arc of the
// disc circle; the terminator (lit→unlit surface) is a soft curve strictly INSIDE that circle. So:
// threshold the lit region, walk its boundary, keep the sharpest edge points, and let a
// deterministic RANSAC vote pick the circle the limb agrees on — terminator points scatter across
// candidate circles (different curvature) and lose the vote by construction.
const (
	discDown        = 4    // fit on a 4× box-downsampled plane (≈1024² for a 16 MP frame)
	discMinRadius   = 32.0 // small px (≈128 full px): smaller blobs are noise/planets, not the Moon
	discMaxRadiusX  = 1.5  // r ≤ 1.5×max(w,h): the disc may exceed the frame, garbage may not
	discCenterSlack = 0.75 // centre may sit up to 0.75×dim outside the frame (partial disc)
	discMinAreaFrac = 0.05 // lit component ≥ 5% of the fitted disc area (a thin crescent is ~6%)
	discOnDiscFrac  = 0.8  // ≥80% of the lit component must lie inside 1.02·r
	discKeepSharp   = 0.6  // keep the sharpest 60% of boundary points (limb sharp, terminator soft)
	discKeepMin     = 30   // but never fewer than this many
	discSeeds       = 24   // evenly spaced seeds → C(24,3) = 2024 deterministic triples
	discMinChord    = 16.0 // degenerate-triple floor (small px)
	discInlierTol   = 1.5  // limb-distance tolerance (small px ≈ 6 full px)
	discMinInliers  = 60
	discArcBins     = 72  // 5° bins
	discMinArcBins  = 18  // ≥90° of limb must vote
	discMaxResidMAD = 1.0 // small px
)

// discFit is a fitted lunar disc in FULL-resolution pixel coordinates.
type discFit struct {
	CX, CY, R float64
	Inliers   int
	ArcDeg    float64
	ResidMAD  float64
}

type edgePoint struct{ x, y, grad float64 }

type circle struct{ cx, cy, r float64 }

// fitLunarDisc fits the Moon's limb circle on the image's first plane (the detail master).
// ok=false means "no confident disc" — the caller skips the earthshine step with a note.
func fitLunarDisc(im *fits.Image) (discFit, bool) {
	small := downPlane(im, discDown)
	scale := 1.0
	if small != im {
		scale = discDown
	}
	comp, count, ccx, ccy := litComponent(small)
	if count == 0 {
		return discFit{}, false
	}
	pts := boundaryPoints(small, comp)
	pts = keepSharpest(pts)
	if len(pts) < discMinInliers {
		return discFit{}, false
	}
	sortByAngle(pts, ccx, ccy)
	c, ok := ransacCircle(pts, small.W, small.H)
	if !ok {
		return discFit{}, false
	}
	c, inliers, residMAD, ok := refineCircle(pts, c)
	if !ok || len(inliers) < discMinInliers || residMAD > discMaxResidMAD {
		return discFit{}, false
	}
	if !circleBounds(c, small.W, small.H) || !litOnDisc(comp, small.W, count, c) {
		return discFit{}, false
	}
	if float64(count) < discMinAreaFrac*math.Pi*c.r*c.r {
		return discFit{}, false
	}
	bins := arcBinsCovered(inliers, c)
	if bins < discMinArcBins {
		return discFit{}, false
	}
	return discFit{
		CX:      (c.cx+0.5)*scale - 0.5,
		CY:      (c.cy+0.5)*scale - 0.5,
		R:       c.r * scale,
		Inliers: len(inliers), ArcDeg: float64(bins) * 360 / discArcBins, ResidMAD: residMAD * scale,
	}, true
}

// litComponent thresholds the lit surface (same recipe as apDiskMask/detailSNR) and keeps the
// largest connected component, returning its mask, pixel count and centroid.
func litComponent(im *fits.Image) (comp []bool, count int, cx, cy float64) {
	p := im.Pix[0]
	bg, pk, ok := litStats(p)
	if !ok {
		return nil, 0, 0, 0
	}
	thr := litThreshold(bg, pk)
	mask := make([]bool, len(p))
	for i, v := range p {
		mask[i] = v > thr
	}
	labels, n := imgops.Label(mask, im.W, im.H)
	if n == 0 {
		return nil, 0, 0, 0
	}
	sizes := make([]int, n+1)
	for _, l := range labels {
		sizes[l]++
	}
	largest := 1
	for l := 2; l <= n; l++ {
		if sizes[l] > sizes[largest] {
			largest = l
		}
	}
	comp = make([]bool, len(p))
	var sx, sy float64
	for i, l := range labels {
		if l != largest {
			continue
		}
		comp[i] = true
		sx += float64(i % im.W)
		sy += float64(i / im.W)
	}
	count = sizes[largest]
	return comp, count, sx / float64(count), sy / float64(count)
}

// boundaryPoints returns the component's edge pixels (a 4-neighbour outside the component) with
// their local gradient magnitude. Image-border pixels are excluded: a frame-clipped disc must not
// contribute the frame edge as "limb".
func boundaryPoints(im *fits.Image, comp []bool) []edgePoint {
	if comp == nil {
		return nil
	}
	p, w, h := im.Pix[0], im.W, im.H
	var pts []edgePoint
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			i := y*w + x
			if !comp[i] || (comp[i-1] && comp[i+1] && comp[i-w] && comp[i+w]) {
				continue
			}
			gx := float64(p[i+1]) - float64(p[i-1])
			gy := float64(p[i+w]) - float64(p[i-w])
			pts = append(pts, edgePoint{x: float64(x), y: float64(y), grad: math.Hypot(gx, gy)})
		}
	}
	return pts
}

// keepSharpest keeps the sharpest discKeepSharp fraction of the boundary (at least discKeepMin):
// the sunlit limb is a hard edge, the terminator a soft ramp, so this biases the vote to the limb.
func keepSharpest(pts []edgePoint) []edgePoint {
	if len(pts) <= discKeepMin {
		return pts
	}
	sort.Slice(pts, func(a, b int) bool {
		if pts[a].grad != pts[b].grad {
			return pts[a].grad > pts[b].grad
		}
		return pts[a].y*1e6+pts[a].x < pts[b].y*1e6+pts[b].x // deterministic tie-break
	})
	keep := int(discKeepSharp * float64(len(pts)))
	if keep < discKeepMin {
		keep = discKeepMin
	}
	return pts[:keep]
}

// sortByAngle orders points by angle about the lit component's centroid so the RANSAC seeds are
// evenly spread along the boundary (deterministic, no randomness).
func sortByAngle(pts []edgePoint, cx, cy float64) {
	sort.Slice(pts, func(a, b int) bool {
		aa := math.Atan2(pts[a].y-cy, pts[a].x-cx)
		ab := math.Atan2(pts[b].y-cy, pts[b].x-cx)
		if aa != ab {
			return aa < ab
		}
		return pts[a].y*1e6+pts[a].x < pts[b].y*1e6+pts[b].x
	})
}

// ransacCircle enumerates circumcircles of evenly-spaced seed triples and keeps the one most of the
// boundary agrees with. Deterministic: same input → same circle.
func ransacCircle(pts []edgePoint, w, h int) (circle, bool) {
	step := len(pts) / discSeeds
	if step < 1 {
		step = 1
	}
	var seeds []edgePoint
	for i := 0; i < len(pts) && len(seeds) < discSeeds; i += step {
		seeds = append(seeds, pts[i])
	}
	var best circle
	bestInl, bestResid := 0, math.Inf(1)
	for i := 0; i < len(seeds); i++ {
		for j := i + 1; j < len(seeds); j++ {
			for k := j + 1; k < len(seeds); k++ {
				c, ok := circumcircle(seeds[i], seeds[j], seeds[k])
				if !ok || !circleBounds(c, w, h) {
					continue
				}
				inl, resid := scoreCircle(pts, c)
				if inl > bestInl || (inl == bestInl && resid < bestResid) {
					best, bestInl, bestResid = c, inl, resid
				}
			}
		}
	}
	return best, bestInl >= discMinInliers
}

// circumcircle returns the circle through three points, rejecting near-collinear or too-close triples.
func circumcircle(a, b, c edgePoint) (circle, bool) {
	if math.Hypot(a.x-b.x, a.y-b.y) < discMinChord ||
		math.Hypot(b.x-c.x, b.y-c.y) < discMinChord ||
		math.Hypot(a.x-c.x, a.y-c.y) < discMinChord {
		return circle{}, false
	}
	d := 2 * (a.x*(b.y-c.y) + b.x*(c.y-a.y) + c.x*(a.y-b.y))
	if math.Abs(d) < 1e-9 {
		return circle{}, false
	}
	a2, b2, c2 := a.x*a.x+a.y*a.y, b.x*b.x+b.y*b.y, c.x*c.x+c.y*c.y
	ux := (a2*(b.y-c.y) + b2*(c.y-a.y) + c2*(a.y-b.y)) / d
	uy := (a2*(c.x-b.x) + b2*(a.x-c.x) + c2*(b.x-a.x)) / d
	return circle{cx: ux, cy: uy, r: math.Hypot(a.x-ux, a.y-uy)}, true
}

// circleBounds bounds a candidate to a plausible Moon: big enough, not absurdly large, centre near
// the frame (a partial disc may centre outside it).
func circleBounds(c circle, w, h int) bool {
	maxDim := float64(w)
	if h > w {
		maxDim = float64(h)
	}
	if c.r < discMinRadius || c.r > discMaxRadiusX*maxDim {
		return false
	}
	return c.cx > -discCenterSlack*float64(w) && c.cx < (1+discCenterSlack)*float64(w) &&
		c.cy > -discCenterSlack*float64(h) && c.cy < (1+discCenterSlack)*float64(h)
}

// scoreCircle counts boundary points within discInlierTol of the circle and sums their residuals.
func scoreCircle(pts []edgePoint, c circle) (inliers int, residSum float64) {
	for _, p := range pts {
		res := math.Abs(math.Hypot(p.x-c.cx, p.y-c.cy) - c.r)
		if res <= discInlierTol {
			inliers++
			residSum += res
		}
	}
	return inliers, residSum
}

// refineCircle least-squares-fits (Kåsa) the RANSAC winner's inliers, trims residual outliers at
// 3×MAD once, refits, and returns the final circle with its inliers and residual MAD.
func refineCircle(pts []edgePoint, c circle) (circle, []edgePoint, float64, bool) {
	inl := circleInliers(pts, c, discInlierTol)
	fit, ok := kasaFit(inl)
	if !ok {
		return circle{}, nil, 0, false
	}
	med, mad := residualStats(inl, fit)
	tol := 3 * mad
	if floor := discInlierTol / 2; tol < floor {
		tol = floor
	}
	trimmed := inl[:0]
	for _, p := range inl {
		if math.Abs(math.Abs(math.Hypot(p.x-fit.cx, p.y-fit.cy)-fit.r)-med) <= tol {
			trimmed = append(trimmed, p)
		}
	}
	if refit, ok2 := kasaFit(trimmed); ok2 {
		fit = refit
	}
	final := circleInliers(pts, fit, discInlierTol)
	_, mad = residualStats(final, fit)
	return fit, final, mad, true
}

func circleInliers(pts []edgePoint, c circle, tol float64) []edgePoint {
	var out []edgePoint
	for _, p := range pts {
		if math.Abs(math.Hypot(p.x-c.cx, p.y-c.cy)-c.r) <= tol {
			out = append(out, p)
		}
	}
	return out
}

// residualStats returns the median and MAD of |dist−r| over pts.
func residualStats(pts []edgePoint, c circle) (med, mad float64) {
	if len(pts) == 0 {
		return 0, 0
	}
	res := make([]float64, len(pts))
	for i, p := range pts {
		res[i] = math.Abs(math.Hypot(p.x-c.cx, p.y-c.cy) - c.r)
	}
	med = medianOf(append([]float64(nil), res...))
	dev := make([]float64, len(res))
	for i, r := range res {
		dev[i] = math.Abs(r - med)
	}
	return med, medianOf(dev)
}

// kasaFit is the algebraic least-squares circle (Kåsa): minimize Σ(x²+y²+Dx+Ey+F)² via the 3×3
// normal equations, solved by Gaussian elimination with partial pivoting.
func kasaFit(pts []edgePoint) (circle, bool) {
	if len(pts) < 3 {
		return circle{}, false
	}
	var sx, sy, sxx, syy, sxy, sz, szx, szy float64
	for _, p := range pts {
		z := p.x*p.x + p.y*p.y
		sx += p.x
		sy += p.y
		sxx += p.x * p.x
		syy += p.y * p.y
		sxy += p.x * p.y
		sz += z
		szx += z * p.x
		szy += z * p.y
	}
	n := float64(len(pts))
	m := [3][4]float64{
		{sxx, sxy, sx, -szx},
		{sxy, syy, sy, -szy},
		{sx, sy, n, -sz},
	}
	for col := 0; col < 3; col++ {
		piv := col
		for r := col + 1; r < 3; r++ {
			if math.Abs(m[r][col]) > math.Abs(m[piv][col]) {
				piv = r
			}
		}
		m[col], m[piv] = m[piv], m[col]
		if math.Abs(m[col][col]) < 1e-12 {
			return circle{}, false
		}
		for r := 0; r < 3; r++ {
			if r == col {
				continue
			}
			f := m[r][col] / m[col][col]
			for cc := col; cc < 4; cc++ {
				m[r][cc] -= f * m[col][cc]
			}
		}
	}
	D, E, F := m[0][3]/m[0][0], m[1][3]/m[1][1], m[2][3]/m[2][2]
	cx, cy := -D/2, -E/2
	r2 := cx*cx + cy*cy - F
	if r2 <= 0 {
		return circle{}, false
	}
	return circle{cx: cx, cy: cy, r: math.Sqrt(r2)}, true
}

// litOnDisc checks that the lit component actually lives on the fitted disc (≥80% of its pixels
// inside 1.02·r) — a fit that "explains" only a corner of the lit area is wrong.
func litOnDisc(comp []bool, w, count int, c circle) bool {
	if count == 0 {
		return false
	}
	limit := 1.02 * c.r
	inside := 0
	for i, on := range comp {
		if !on {
			continue
		}
		if math.Hypot(float64(i%w)-c.cx, float64(i/w)-c.cy) <= limit {
			inside++
		}
	}
	return float64(inside) >= discOnDiscFrac*float64(count)
}

// arcBinsCovered counts the distinct 5° angular bins (about the fitted centre) the inliers occupy —
// a real limb spans a long arc, a lucky cluster of noise does not.
func arcBinsCovered(pts []edgePoint, c circle) int {
	var seen [discArcBins]bool
	for _, p := range pts {
		a := math.Atan2(p.y-c.cy, p.x-c.cx) // [-π, π]
		bin := int((a + math.Pi) / (2 * math.Pi) * discArcBins)
		if bin >= discArcBins {
			bin = discArcBins - 1
		}
		seen[bin] = true
	}
	n := 0
	for _, s := range seen {
		if s {
			n++
		}
	}
	return n
}
