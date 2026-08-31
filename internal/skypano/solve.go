package skypano

import (
	"math"
	"sort"
)

// Match is one catalogue star paired with the detection it landed on.
type Match struct {
	Vec      [3]float64 // catalogue direction
	X, Y     float64    // detected pixel
	Residual float64    // pixels, after the fit
}

// Solution reports how a panel was solved.
type Solution struct {
	Matches int
	// RMSPx is the root-mean-square reprojection residual over the matched stars.
	RMSPx float64
	// ScaleArcsecPerPix at the frame centre.
	ScaleArcsecPerPix float64
	// CoarseShiftDeg is how far the search had to move from the prior — a direct read of how much
	// the phone's own pointing was off.
	CoarseShiftDeg float64
}

// SolveOptions tune the search.
type SolveOptions struct {
	// SearchDeg is the half-width of the coarse search about the prior, in degrees.
	SearchDeg float64
	// CoarseStepDeg is the coarse grid step.
	CoarseStepDeg float64
	// MatchRadiusPx accepts a catalogue star as matched when a detection lies within it.
	MatchRadiusPx float64
	// FitRadiusPx is the tighter radius used once refinement is under way.
	FitRadiusPx float64
	// MinMatches is the fewest matches a solution may claim.
	MinMatches int
}

// DefaultSolveOptions suit a phone frame whose prior is good to a degree or two.
func DefaultSolveOptions() SolveOptions {
	return SolveOptions{SearchDeg: 4, CoarseStepDeg: 0.25, MatchRadiusPx: 40, FitRadiusPx: 8, MinMatches: 25}
}

// Detection is the minimum a solver needs from a detected star.
type Detection struct{ X, Y float64 }

// SolvePanel refines a prior camera until catalogue stars land on detected ones.
//
// This is deliberately NOT blind plate solving. The phone's pointing is good to about a degree, so
// the job is to search a few degrees around it — which a coarse grid does in milliseconds — and then
// least-squares the rotation and focal length onto the stars that matched. Blind solving over a
// 72-degree field is what Siril cannot do; refinement over a known degree is easy.
//
// cat must be catalogue directions (unit vectors), brightest first. det are detected pixels.
func SolvePanel(prior Camera, cat [][3]float64, det []Detection, o SolveOptions) (Camera, Solution, bool) {
	if len(cat) == 0 || len(det) == 0 || prior.F <= 0 {
		return prior, Solution{}, false
	}
	idx := newPixelIndex(det, o.MatchRadiusPx)

	// Coarse: walk a grid of small rotations about the prior and keep whichever lands the most
	// catalogue stars near a detection. Counting matches rather than summing distances is what makes
	// this robust — a handful of spurious detections cannot drag a count.
	best, bestScore, bestShift := prior, -1, 0.0
	step := o.CoarseStepDeg * math.Pi / 180
	n := int(math.Round(o.SearchDeg / o.CoarseStepDeg))
	for i := -n; i <= n; i++ {
		for j := -n; j <= n; j++ {
			for k := -n; k <= n; k++ {
				c := prior
				c.R = Rotate(prior.R, float64(i)*step, float64(j)*step, float64(k)*step)
				if s := countMatches(c, cat, idx, o.MatchRadiusPx); s > bestScore {
					best, bestScore = c, s
					bestShift = math.Hypot(math.Hypot(float64(i), float64(j)), float64(k)) * o.CoarseStepDeg
				}
			}
		}
	}
	if bestScore < o.MinMatches {
		return prior, Solution{Matches: bestScore}, false
	}

	// Refine: tighten the match radius in stages, re-fitting rotation and focal length each time, so
	// early loose pairs pull the camera close enough for the strict ones to be right.
	cam := best
	radius := o.MatchRadiusPx
	var matches []Match
	for radius >= o.FitRadiusPx {
		matches = collectMatches(cam, cat, det, radius)
		if len(matches) < o.MinMatches {
			break
		}
		cam = fit(cam, matches, 4)
		radius /= 2
	}
	matches = collectMatches(cam, cat, det, o.FitRadiusPx)
	if len(matches) < o.MinMatches {
		return prior, Solution{Matches: len(matches)}, false
	}
	cam = fit(cam, matches, 4)
	matches = collectMatches(cam, cat, det, o.FitRadiusPx)

	return cam, Solution{
		Matches:           len(matches),
		RMSPx:             rms(matches),
		ScaleArcsecPerPix: 3600 * 180 / math.Pi / cam.F,
		CoarseShiftDeg:    bestShift,
	}, true
}

// maxFitParams is the widest system fit solves: three rotation angles, the focal length and three
// radial distortion coefficients.
const maxFitParams = 7

// fit runs Levenberg-damped Gauss-Newton over the first nParams of: three rotation angles, the focal
// length, and the three radial distortion coefficients. The principal point is left alone
// deliberately — over a few degrees it is almost perfectly degenerate with rotation, so fitting it
// only makes the solution wander.
//
// nParams matters more than it looks. Given only the four stars of one quad — all close together —
// focal length is barely distinguishable from a rotation, and letting it float lets the fit satisfy
// those four points by driving F through zero and mirroring the whole sky. Seed fits therefore hold
// F at the value the lens says (good to a couple of percent) and solve rotation alone; F is only
// released once the fit is being judged on stars spread across the frame, and the distortion terms
// only once the stars being judged REACH the field edge — they are unidentifiable from the inner
// field, where by construction the distortion is nearly zero.
func fit(cam Camera, m []Match, nParams int) Camera {
	params := nParams
	if params < 3 {
		params = 3
	}
	if params > maxFitParams {
		params = maxFitParams
	}
	lambda := 1e-3
	cur := cam
	curCost := cost(cur, m)
	for iter := 0; iter < 20; iter++ {
		// Numerical Jacobian: the model is cheap and the parameter count tiny, so a difference
		// quotient is both simpler and less error-prone than hand-differentiating the projection.
		var jtj [maxFitParams][maxFitParams]float64
		var jtr [maxFitParams]float64
		steps := [maxFitParams]float64{1e-5, 1e-5, 1e-5, cur.F * 1e-5, 1e-5, 1e-5, 1e-5}
		for _, mm := range m {
			x0, y0, ok := cur.Project(mm.Vec)
			if !ok {
				continue
			}
			rx, ry := mm.X-x0, mm.Y-y0
			var jx, jy [maxFitParams]float64
			for p := 0; p < params; p++ {
				c := perturb(cur, p, steps[p])
				x1, y1, ok := c.Project(mm.Vec)
				if !ok {
					continue
				}
				jx[p] = (x1 - x0) / steps[p]
				jy[p] = (y1 - y0) / steps[p]
			}
			for a := 0; a < params; a++ {
				jtr[a] += jx[a]*rx + jy[a]*ry
				for b := 0; b < params; b++ {
					jtj[a][b] += jx[a]*jx[b] + jy[a]*jy[b]
				}
			}
		}
		for a := 0; a < params; a++ {
			jtj[a][a] *= 1 + lambda
		}
		delta, ok := solveN(jtj, jtr, params)
		if !ok {
			break
		}
		cand := cur
		for p := 0; p < params; p++ {
			cand = perturb(cand, p, delta[p])
		}
		if c := cost(cand, m); c < curCost {
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

// perturb nudges one parameter: 0..2 are the rotation angles, 3 is the focal length, 4..6 are the
// radial distortion coefficients.
func perturb(c Camera, p int, d float64) Camera {
	switch p {
	case 0:
		c.R = Rotate(c.R, d, 0, 0)
	case 1:
		c.R = Rotate(c.R, 0, d, 0)
	case 2:
		c.R = Rotate(c.R, 0, 0, d)
	case 3:
		c.F += d
	case 4:
		c.K1 += d
	case 5:
		c.K2 += d
	case 6:
		c.K3 += d
	}
	return c
}

func cost(c Camera, m []Match) float64 {
	total := 0.0
	for _, mm := range m {
		x, y, ok := c.Project(mm.Vec)
		if !ok {
			total += 1e6
			continue
		}
		total += (x-mm.X)*(x-mm.X) + (y-mm.Y)*(y-mm.Y)
	}
	return total
}

func rms(m []Match) float64 {
	if len(m) == 0 {
		return 0
	}
	total := 0.0
	for _, mm := range m {
		total += mm.Residual * mm.Residual
	}
	return math.Sqrt(total / float64(len(m)))
}

// collectMatches pairs each catalogue star with its nearest detection inside radius, keeping only
// mutually-nearest pairs so one bright detection cannot claim a crowd of catalogue entries.
func collectMatches(c Camera, cat [][3]float64, det []Detection, radius float64) []Match {
	type cand struct {
		star int
		d2   float64
		x, y float64
	}
	claim := make(map[int]cand, len(det))
	r2 := radius * radius
	for si, v := range cat {
		x, y, ok := c.Project(v)
		if !ok {
			continue
		}
		bi, bd := -1, r2
		for di, d := range det {
			dd := (d.X-x)*(d.X-x) + (d.Y-y)*(d.Y-y)
			if dd < bd {
				bi, bd = di, dd
			}
		}
		if bi < 0 {
			continue
		}
		if prev, ok := claim[bi]; !ok || bd < prev.d2 {
			claim[bi] = cand{star: si, d2: bd, x: det[bi].X, y: det[bi].Y}
		}
	}
	out := make([]Match, 0, len(claim))
	for _, cc := range claim {
		out = append(out, Match{Vec: cat[cc.star], X: cc.x, Y: cc.y, Residual: math.Sqrt(cc.d2)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Residual < out[j].Residual })
	return out
}
