package skypano

// bundle.go solves the lens ONCE, from every panel's stars at the same time.
//
// It exists because of a measurement. Each panel, solved on its own, matched its catalogue stars at
// about 2 px RMS and looked solved — but two panels shown the same star placed it up to 13 px apart,
// and the mosaic drew every star as a dash because the blend averaged those disagreeing copies. The
// residual, plotted against distance from the frame centre, was almost purely radial: −1 px on axis,
// +18 px at mid-field, falling back by the corners. That is uncorrected lens distortion, and Apple's
// ProRAW "lens corrected" frames evidently still carry it.
//
// Two things follow, and both are the point of this file:
//
// The per-panel RMS was never evidence the lens was right. The refinement matched inside 8 px, so
// every star whose distortion exceeded 8 px was silently excluded from the fit, and the fit then
// reported a small residual over the inner field it had kept. A tight match radius does not measure
// distortion, it hides it. So the staging here starts wide enough to admit the outer field.
//
// The distortion belongs to the LENS, not the panel. Fitting it per panel would let seven independent
// answers drift apart and leave the panels disagreeing again in a subtler way. One shared set of
// coefficients, fitted from every panel's stars at once, is both better constrained and the thing that
// makes two panels place a star in the same spot — which is what the mosaic actually needs.

import (
	"math"
	"sort"
)

// BundleLens alternately refines each panel's rotation and the lens the panels share, tightening the
// match radius as it goes.
//
// cams, cats and dets are parallel: one entry per panel. It returns the refined cameras and a
// per-panel solution report.
func BundleLens(cams []Camera, cats [][][3]float64, dets [][]Detection, o SolveOptions, rounds int) ([]Camera, []Solution, bool) {
	if len(cams) == 0 || len(cams) != len(cats) || len(cams) != len(dets) {
		return cams, nil, false
	}
	cur := append([]Camera(nil), cams...)
	// Every panel starts from the same lens, since there is only one; take the median focal length so
	// one bad panel cannot set it.
	f := medianFocal(cur)
	for i := range cur {
		cur[i].F, cur[i].K1, cur[i].K2, cur[i].K3 = f, 0, 0, 0
	}

	radius := math.Max(o.MatchRadiusPx, 40)
	for r := 0; r < rounds; r++ {
		ms := make([][]Match, len(cur))
		for i := range cur {
			ms[i] = collectMatches(cur[i], cats[i], dets[i], radius)
			if len(ms[i]) >= o.MinMatches {
				// Rotation only: the lens is the set's business, not this panel's.
				cur[i] = fit(cur[i], ms[i], 3)
				ms[i] = collectMatches(cur[i], cats[i], dets[i], radius)
			}
		}
		cur = fitSharedLens(cur, ms)
		if radius > o.FitRadiusPx {
			radius = math.Max(radius/2, o.FitRadiusPx)
		}
	}

	// The polynomial has gone as far as it can. Whatever radial error is left — and on a phone that
	// is the corners — now comes off empirically: refit each rotation, measure the residual against
	// radius over EVERY panel at once, and store it as a correction curve they all share.
	//
	// Start wide again. The radius above has tightened to the final few pixels, and the error being
	// measured here is the one the polynomial could not reach — up to a dozen pixels at the corners.
	// Matching inside three would drop exactly the stars this pass exists for.
	rr := math.Max(o.MatchRadiusPx, 40)
	for r := 0; r < panoRadialRounds; r++ {
		ms := make([][]Match, len(cur))
		for i := range cur {
			ms[i] = collectMatches(cur[i], cats[i], dets[i], rr)
			if len(ms[i]) >= o.MinMatches {
				cur[i] = fit(cur[i], ms[i], 3)
				ms[i] = collectMatches(cur[i], cats[i], dets[i], rr)
			}
		}
		table, maxR := fitRadialTable(cur, ms, radialTableBins)
		if len(table) == 0 {
			break
		}
		for i := range cur {
			cur[i].RadialCorr, cur[i].RadialCorrMaxR = table, maxR
		}
		rr = math.Max(rr/2, o.FitRadiusPx)
	}

	sols := make([]Solution, len(cur))
	ok := false
	for i := range cur {
		m := collectMatches(cur[i], cats[i], dets[i], o.FitRadiusPx)
		sols[i] = Solution{
			Matches:           len(m),
			RMSPx:             rms(m),
			ScaleArcsecPerPix: 3600 * 180 / math.Pi / cur[i].F,
		}
		if len(m) >= o.MinMatches {
			ok = true
		}
	}
	return cur, sols, ok
}

// panoRadialRounds and radialTableBins tune the empirical pass. Few bins and few rounds on purpose:
// the table takes up the slack the polynomial leaves at the edge, it does not chase every star.
const (
	panoRadialRounds = 3
	radialTableBins  = 20
)

// fitRadialTable measures the leftover RADIAL residual against field radius, pooled over every panel,
// and returns it as a correction to the distortion factor plus the radius the table spans.
//
// Pooled because it belongs to the lens: one curve from tens of thousands of stars is far better
// determined than one curve per panel, and per-panel curves would let the panels drift apart again —
// the very failure this file exists to prevent.
func fitRadialTable(cams []Camera, ms [][]Match, bins int) ([]float64, float64) {
	if bins < 3 || len(cams) == 0 {
		return nil, 0
	}
	type sample struct{ rn, dd float64 }
	var pts []sample
	maxRn := 0.0
	for i := range cams {
		c := cams[i]
		if c.F <= 0 {
			continue
		}
		for _, m := range ms[i] {
			x, y, ok := c.Project(m.Vec)
			if !ok {
				continue
			}
			dx, dy := x-c.Cx, y-c.Cy
			rpx := math.Hypot(dx, dy)
			if rpx < 1 {
				continue // a radial correction at the centre has no direction to act along
			}
			// The residual's component ALONG the radius, expressed as a change of the factor: a
			// displacement of e pixels at radius rpx is a factor change of e/rpx.
			e := ((m.X-x)*dx + (m.Y-y)*dy) / rpx
			pts = append(pts, sample{rpx / c.F, e / rpx})
			if rn := rpx / c.F; rn > maxRn {
				maxRn = rn
			}
		}
	}
	if len(pts) < bins*8 || maxRn <= 0 {
		return nil, 0
	}

	table := make([]float64, bins)
	step := maxRn / float64(bins-1)
	for k := 1; k < bins; k++ { // index 0 stays zero: there is no radial direction at the centre
		lo, hi := (float64(k)-0.5)*step, (float64(k)+0.5)*step
		var v []float64
		for _, p := range pts {
			if p.rn >= lo && p.rn < hi {
				v = append(v, p.dd)
			}
		}
		if len(v) < 8 {
			table[k] = table[k-1] // too few stars out here to claim anything new; hold the last value
			continue
		}
		sort.Float64s(v)
		// Each bin's correction stands on its own — it is the factor error measured AT that radius,
		// not an increment on the bin before it.
		table[k] = v[len(v)/2]
	}
	// What was just measured is the residual AFTER the correction already in place, so it accumulates
	// onto it rather than replacing it.
	if len(cams[0].RadialCorr) > 0 {
		for k := range table {
			table[k] += cams[0].corrAt(float64(k) * step)
		}
	}
	return table, maxRn
}

// fitSharedLens runs Levenberg-damped Gauss-Newton over the four parameters every camera shares — the
// focal length and the three radial coefficients — accumulating the normal equations across all the
// panels at once. Rotations are held: BundleLens has just refined them.
func fitSharedLens(cams []Camera, ms [][]Match) []Camera {
	const n = 4 // F, K1, K2, K3
	total := 0
	for _, m := range ms {
		total += len(m)
	}
	if total < 4*n {
		return cams
	}
	cur := append([]Camera(nil), cams...)
	curCost := sharedCost(cur, ms)
	lambda := 1e-3

	for iter := 0; iter < 30; iter++ {
		var jtj [maxFitParams][maxFitParams]float64
		var jtr [maxFitParams]float64
		steps := [n]float64{cur[0].F * 1e-5, 1e-6, 1e-6, 1e-6}
		for i := range cur {
			for _, mm := range ms[i] {
				x0, y0, ok := cur[i].Project(mm.Vec)
				if !ok {
					continue
				}
				rx, ry := mm.X-x0, mm.Y-y0
				var jx, jy [n]float64
				for p := 0; p < n; p++ {
					c := perturb(cur[i], p+3, steps[p])
					x1, y1, ok := c.Project(mm.Vec)
					if !ok {
						continue
					}
					jx[p] = (x1 - x0) / steps[p]
					jy[p] = (y1 - y0) / steps[p]
				}
				for a := 0; a < n; a++ {
					jtr[a] += jx[a]*rx + jy[a]*ry
					for b := 0; b < n; b++ {
						jtj[a][b] += jx[a]*jx[b] + jy[a]*jy[b]
					}
				}
			}
		}
		for a := 0; a < n; a++ {
			jtj[a][a] *= 1 + lambda
		}
		delta, ok := solveN(jtj, jtr, n)
		if !ok {
			break
		}
		cand := append([]Camera(nil), cur...)
		for i := range cand {
			for p := 0; p < n; p++ {
				cand[i] = perturb(cand[i], p+3, delta[p])
			}
		}
		if c := sharedCost(cand, ms); c < curCost {
			cur, curCost, lambda = cand, c, math.Max(lambda/10, 1e-9)
			continue
		}
		lambda *= 10
		if lambda > 1e6 {
			break
		}
	}
	return cur
}

func sharedCost(cams []Camera, ms [][]Match) float64 {
	var total float64
	for i := range cams {
		total += cost(cams[i], ms[i])
	}
	return total
}

func medianFocal(cams []Camera) float64 {
	f := make([]float64, 0, len(cams))
	for _, c := range cams {
		if c.F > 0 {
			f = append(f, c.F)
		}
	}
	if len(f) == 0 {
		return 0
	}
	return quantileOf(f, 0.5)
}

// quantileOf returns the q-quantile of v, leaving the caller's slice alone.
func quantileOf(v []float64, q float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if len(s) == 0 {
		return 0
	}
	return s[int(math.Min(math.Max(q, 0), 1)*float64(len(s)-1))]
}
