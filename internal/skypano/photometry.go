package skypano

// photometry.go brings the panels onto one photometric scale before they are blended.
//
// A single gain and offset per panel is not enough here, and the mosaic says so plainly: with only a
// level to play with, the assembled canvas came out in coloured patches, one per panel. The reason is
// physical. Each panel was shot at a different altitude and a different hour, so each carries its own
// airglow and light-pollution gradient ACROSS ITS OWN FIELD — brighter towards the horizon, and
// greener, since airglow is green. A gradient that changes from panel to panel is discontinuous at
// the seams, so no canvas-wide model can remove it: it has to come off in each panel's own frame,
// before the blend.
//
// So the correction is, per panel and per channel, an additive PLANE:
//
//	corrected = value + a + b*u + c*v
//
// which is linear in its three unknowns and therefore one small least-squares solve per panel, per
// channel, per round. Per channel because the drift has a colour — the phone re-white-balanced itself
// between panels, and the airglow it was shooting through is not grey.
//
// A PLANE and not a quadratic, and that too is now measured rather than assumed. The assembled canvas
// carries a smooth colour field — 52% peak-to-peak in (R-G)/G on the galactic strip, panel-scale
// patches of green and magenta — and the physics argues for curvature: these panels are 57 by 72
// DEGREES, airglow follows airmass ∝ sec(z), and over a field that wide sec(z) is nothing like linear,
// so a tilt fits the middle of a panel and misses both ends. Adding u², uv and v² anyway (gated at 400
// paired samples) did NOT collapse that field: measured on the arch canvas before and after,
// (R-G)/G went 0.716 → 0.693 and (B-G)/G went 0.580 → 0.852. Blue got half again worse.
//
// The reason is where the constraint lives. The samples are OVERLAPS, and overlaps are at the panel
// EDGES; a quadratic fitted there is unconstrained across the panel's INTERIOR, so it extrapolates
// curvature into the middle of the frame that nothing ever measured. A plane is rigid enough that it
// cannot do much harm between its constraints. This is the same trap as fitting a trend to a trusted
// middle and evaluating it at the ends (see pipeline/edgecrop.go): a model must not be asked for
// values where no data pinned it. Removing this field needs a correction constrained over the whole
// panel — a per-panel sky model from the panel's own frame — not more freedom fitted at its rim.
//
// Additive, with NO gain, and that is a measured decision rather than a simplification. Airglow and
// light pollution add light, so an offset is the physically right correction for them; and ProRAW is
// gain-normalised, so the panels' true exposure differences were measured at a few per cent. Fitting
// a gain anyway made things worse twice over. Uncentred it traded off against the constant and
// returned offsets larger than the sky. Centred, it fell to 0.53-0.74 — the panels being told to
// halve themselves — which is regression dilution: the regressor is a noisy measurement of the same
// sky as the target, and independent noise in a regressor biases its slope towards zero. A gain that
// cannot be measured reliably is worse than a gain assumed to be one, when it is known to be one.

import (
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/imgops"
)

// minPlaneSamples is the paired-overlap count a panel needs before it is corrected at all.
const minPlaneSamples = 64

// Correction is a panel's photometric correction, in the panel's own normalised coordinates. The zero
// value is the identity, so a Panel that has never been matched renders as itself.
type Correction struct {
	// Plane is per channel: the constant, the coefficient of u, and the coefficient of v.
	Plane [][3]float64
}

// Apply corrects one sample. u and v are the panel's own coordinates in [-1,1].
func (co Correction) Apply(ch int, val, u, v float64) float64 {
	if ch >= len(co.Plane) {
		return val
	}
	p := co.Plane[ch]
	return val + p[0] + p[1]*u + p[2]*v
}

func identityCorrection(nch int) Correction {
	return Correction{Plane: make([][3]float64, nch)}
}

// MatchPhotometry fits every panel's correction from the regions where panels overlap.
//
// It is iterative rather than one global solve: each round measures a panel against the consensus of
// everything it overlaps and moves part of the way, which converges without requiring the overlap
// graph to have any particular shape and degrades gently when a panel touches only one neighbour.
// Panel 0 is held at the identity, so the set has a reference and cannot drift as a whole; the level
// that leaves behind is arbitrary, and the canvas-wide Flatten and the grade's black point both
// remove it afterwards.
func MatchPhotometry(panels []Panel, c Canvas, samples, rounds int) {
	if len(panels) < 2 {
		return
	}
	nch := panels[0].Img.C
	// Start from identity every time. The panels are the caller's, and matching them against a second
	// canvas while they still carry the first canvas's correction reads as a refinement but is not
	// one — the answer would depend on how many times the function had been called.
	for i := range panels {
		panels[i].Corr = identityCorrection(nch)
	}

	type obs struct {
		panel int
		u, v  float64
		val   []float64 // per channel, uncorrected
	}
	var all [][]obs
	for _, pt := range samplePoints(c, samples) {
		sky, ok := c.PixToSky(pt[0], pt[1])
		if !ok {
			continue
		}
		var row []obs
		for pi := range panels {
			im := panels[pi].Img
			px, py, ok := panels[pi].Cam.Project(sky)
			if !ok || edgeWeight(px, py, im.W, im.H, DefaultRenderOptions()) <= 0 {
				continue
			}
			o := obs{panel: pi, val: make([]float64, nch)}
			o.u, o.v = panelUV(px, py, im.W, im.H)
			for ch := 0; ch < nch; ch++ {
				o.val[ch] = float64(imgops.SampleCubic(im.Pix[ch], im.W, im.H, px, py))
			}
			row = append(row, o)
		}
		if len(row) > 1 {
			all = append(all, row)
		}
	}
	if len(all) == 0 {
		return
	}

	for r := 0; r < rounds; r++ {
		for pi := 1; pi < len(panels); pi++ {
			for ch := 0; ch < nch; ch++ {
				var mine, theirs, us, vs []float64
				for _, row := range all {
					var self obs
					var others []float64
					found := false
					for _, ob := range row {
						if ob.panel == pi {
							self, found = ob, true
							continue
						}
						others = append(others, panels[ob.panel].Corr.Apply(ch, ob.val[ch], ob.u, ob.v))
					}
					if !found || len(others) == 0 {
						continue
					}
					sort.Float64s(others)
					mine = append(mine, self.val[ch])
					theirs = append(theirs, others[len(others)/2])
					us, vs = append(us, self.u), append(vs, self.v)
				}
				if len(mine) < minPlaneSamples {
					continue
				}
				plane, ok := fitPlane(mine, us, vs, theirs)
				if !ok {
					continue
				}
				// Damped, so one noisy round cannot throw a panel across the set.
				co := &panels[pi].Corr
				for k := 0; k < 3; k++ {
					co.Plane[ch][k] += 0.5 * (plane[k] - co.Plane[ch][k])
				}
			}
		}
	}
}

// panelUV maps a panel pixel to [-1,1], the frame its correction plane is expressed in.
func panelUV(x, y float64, w, h int) (float64, float64) {
	return (x - float64(w)/2) / (float64(w) / 2), (y - float64(h)/2) / (float64(h) / 2)
}

// fitPlane solves target = val + a + b*u + c*v by least squares — the additive difference between
// what this panel sees and what its neighbours see, as a function of where in this panel it is.
//
// Unlike the quantile matching this replaces, the samples are PAIRED — the same direction on the sky
// seen by two panels — because a gradient can only be measured if it is known where each sample came
// from.
//
// The regressors are centred before solving, which keeps the constant from trading against the tilts
// when an overlap is lopsided, and one robustness pass drops the samples the first fit explains
// worst. Those are the stars, which are in both panels but land on slightly different pixels, and the
// registration edges — a handful of them would otherwise tilt the plane across the whole frame.
func fitPlane(val, us, vs, target []float64) (plane [3]float64, ok bool) {
	resid := make([]float64, len(target))
	for i := range target {
		resid[i] = target[i] - val[i]
	}
	fit := func(keep []bool) ([3]float64, bool) {
		sol, k0, ok := solveCentred([][]float64{us, vs}, resid, keep)
		if !ok {
			return [3]float64{}, false
		}
		return [3]float64{k0, sol[0], sol[1]}, true
	}

	p, ok := fit(nil)
	if !ok {
		return [3]float64{}, false
	}
	keep := trimWorst(val, us, vs, target, p, 0.85)
	if p2, ok2 := fit(keep); ok2 {
		p = p2
	}
	return p, true
}

// solveCentred fits target = sum(coef[k]*cols[k]) + constant over the kept samples, subtracting each
// column's mean first. Centring is not a nicety: without it the constant is collinear with any column
// whose values barely vary, and the solve answers with a large coefficient and a compensating offset
// that cancel. That is exactly how an earlier version of this returned offsets of +0.011 on a sky of
// 0.005 — a level the panel does not have.
func solveCentred(cols [][]float64, target []float64, keep []bool) (coef []float64, constant float64, ok bool) {
	n := 0
	means := make([]float64, len(cols))
	var mt float64
	for i := range target {
		if keep != nil && !keep[i] {
			continue
		}
		for k := range cols {
			means[k] += cols[k][i]
		}
		mt += target[i]
		n++
	}
	if n < 32 {
		return nil, 0, false
	}
	for k := range means {
		means[k] /= float64(n)
	}
	mt /= float64(n)

	m := len(cols)
	a := make([][]float64, m)
	for i := range a {
		a[i] = make([]float64, m)
	}
	b := make([]float64, m)
	for i := range target {
		if keep != nil && !keep[i] {
			continue
		}
		dt := target[i] - mt
		for r := 0; r < m; r++ {
			dr := cols[r][i] - means[r]
			b[r] += dr * dt
			for cc := 0; cc < m; cc++ {
				a[r][cc] += dr * (cols[cc][i] - means[cc])
			}
		}
	}
	sol, sok := solveDense(a, b)
	if !sok {
		return nil, 0, false
	}
	constant = mt
	for k := range sol {
		constant -= sol[k] * means[k]
	}
	return sol, constant, true
}

// trimWorst marks the fraction of samples a fit explains best.
func trimWorst(val, us, vs, target []float64, p [3]float64, frac float64) []bool {
	res := make([]float64, len(val))
	for i := range val {
		res[i] = math.Abs(target[i] - (val[i] + p[0] + p[1]*us[i] + p[2]*vs[i]))
	}
	sorted := append([]float64(nil), res...)
	sort.Float64s(sorted)
	cut := sorted[int(frac*float64(len(sorted)-1))]
	keep := make([]bool, len(val))
	for i := range res {
		keep[i] = res[i] <= cut
	}
	return keep
}
