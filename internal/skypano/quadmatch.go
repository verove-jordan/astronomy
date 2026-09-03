package skypano

import "math"

// quadIndex buckets quads by their code so a lookup does not walk every one.
type quadIndex struct {
	tol     float64
	quads   []Quad
	buckets map[[4]int][]int
}

func newQuadIndex(quads []Quad, tol float64) *quadIndex {
	ix := &quadIndex{tol: tol, quads: quads, buckets: make(map[[4]int][]int, len(quads))}
	for i, q := range quads {
		ix.buckets[ix.key(q.Code)] = append(ix.buckets[ix.key(q.Code)], i)
	}
	return ix
}

func (ix *quadIndex) key(c [4]float64) [4]int {
	return [4]int{
		int(math.Floor(c[0] / ix.tol)),
		int(math.Floor(c[1] / ix.tol)),
		int(math.Floor(c[2] / ix.tol)),
		int(math.Floor(c[3] / ix.tol)),
	}
}

// near returns quads whose code is within tol of c, scanning the 81 surrounding buckets.
func (ix *quadIndex) near(c [4]float64) []int {
	base := ix.key(c)
	var out []int
	var off [4]int
	for off[0] = -1; off[0] <= 1; off[0]++ {
		for off[1] = -1; off[1] <= 1; off[1]++ {
			for off[2] = -1; off[2] <= 1; off[2]++ {
				for off[3] = -1; off[3] <= 1; off[3]++ {
					k := [4]int{base[0] + off[0], base[1] + off[1], base[2] + off[2], base[3] + off[3]}
					for _, qi := range ix.buckets[k] {
						if codeDist(ix.quads[qi].Code, c) <= ix.tol {
							out = append(out, qi)
						}
					}
				}
			}
		}
	}
	return out
}

func codeDist(a, b [4]float64) float64 {
	s := 0.0
	for i := range a {
		d := a[i] - b[i]
		s += d * d
	}
	return math.Sqrt(s)
}

// QuadSolveOptions tune the asterism solve.
type QuadSolveOptions struct {
	Quads QuadOptions
	// CodeTol is how close two hash codes must be to be considered the same shape. Roughly the
	// centroid error divided by the quad diameter.
	CodeTol float64
	// VerifyRadiusPx is the loose radius used to decide whether a candidate is worth refitting.
	VerifyRadiusPx float64
	// ConfirmRadiusPx is the TIGHT radius a refitted solution is finally judged on. It has to be
	// close to the real centroid error: a loose radius accepts random pairings, and enough of them
	// to look convincing. Measured on a real panel, 2000 detections and a 12-pixel radius produce
	// about 180 chance matches with an RMS of 8 — which is simply the mean distance inside a
	// 12-pixel disc, the signature of matching noise. At 4 pixels chance falls to about 20.
	ConfirmRadiusPx float64
	// MinInliers is the fewest CONFIRMED stars a solution may claim.
	MinInliers int
	// MaxCandidates bounds how many shape matches are verified before giving up.
	MaxCandidates int
	// CellsX, CellsY and PerCell equalise star density before quads are built — see uniform.go.
	// PerCell times the cell count is roughly how many stars each side contributes.
	CellsX, CellsY, PerCell int
}

func DefaultQuadSolveOptions() QuadSolveOptions {
	return QuadSolveOptions{
		Quads:           DefaultQuadOptions(),
		CodeTol:         0.03,
		VerifyRadiusPx:  12,
		ConfirmRadiusPx: 4,
		MinInliers:      60,
		MaxCandidates:   20000,
		CellsX:          8,
		CellsY:          6,
		PerCell:         12,
	}
}

// SolveQuads finds the camera by matching star SHAPES rather than star brightnesses.
//
// The catalogue is first projected through the prior camera, so both sets live in the same pixel
// plane and a quad's shape means the same thing on each side. The prior only has to be good enough
// that a quad's local shape is not distorted between the two — a few degrees of pointing error
// changes it by a second-order amount, which is why this works where nearest-neighbour matching
// could not.
//
// Each shape match hands over four star correspondences. Those are enough to fit a camera, and the
// fit is then judged on every OTHER star: a real match aligns the whole field, a coincidental one
// aligns four stars and nothing else.
//
// THE POPULATIONS MUST OVERLAP, and this is the whole game. A quad matches only if all four of its
// stars are present on BOTH sides, so cat and det have to reach comparable DEPTH. Measured on a real
// panel: the 400 brightest detections against the 800 brightest catalogue stars (to magnitude 5.9)
// found nothing but chance — 17 inliers — because the detections are drawn from thousands of stars
// by a ranking that saturation has scrambled, so the two sets barely intersect. Going deeper on both
// sides, 2000 detections against 4000 catalogue stars (to magnitude 7.3), the same code returns 219.
// Do not try to fix a failure here by loosening CodeTol; go deeper instead.
func SolveQuads(prior Camera, cat [][3]float64, det []Detection, o QuadSolveOptions) (Camera, Solution, bool) {
	if len(cat) < 4 || len(det) < 4 || prior.F <= 0 {
		return prior, Solution{}, false
	}
	// Catalogue into the prior's pixel plane, keeping the mapping back to sky vectors.
	var refPts []Point
	var refVec [][3]float64
	for _, v := range cat {
		x, y, ok := prior.Project(v)
		if !ok {
			continue
		}
		refPts = append(refPts, Point{x, y})
		refVec = append(refVec, v)
	}
	imgPts := make([]Point, len(det))
	for i, d := range det {
		imgPts[i] = Point{d.X, d.Y}
	}
	if len(refPts) < 4 {
		return prior, Solution{}, false
	}

	// Even out the density on both sides before building quads, so the two lists agree locally about
	// which stars are present. Without this the neighbourhoods never correspond.
	w, h := prior.Cx*2, prior.Cy*2
	refSel := selectUniform(refPts, w, h, o.CellsX, o.CellsY, o.PerCell)
	imgSel := selectUniform(imgPts, w, h, o.CellsX, o.CellsY, o.PerCell)
	refQuads := BuildQuads(subset(refPts, refSel), o.Quads)
	imgQuads := BuildQuads(subset(imgPts, imgSel), o.Quads)
	Diag.RefStars, Diag.ImgStars = len(refSel), len(imgSel)
	if len(refQuads) == 0 || len(imgQuads) == 0 {
		return prior, Solution{}, false
	}
	ix := newQuadIndex(refQuads, o.CodeTol)

	best, bestSol, found := prior, Solution{}, false
	tried := 0
	Diag.RefQuads, Diag.ImgQuads, Diag.Candidates, Diag.BestInliers = len(refQuads), len(imgQuads), 0, 0
	for _, iq := range imgQuads {
		for _, ri := range ix.near(iq.Code) {
			if tried >= o.MaxCandidates {
				return best, bestSol, found
			}
			tried++
			Diag.Candidates++
			rq := refQuads[ri]
			// The code's symmetry breaking means A,B,C,D correspond position by position.
			seed := make([]Match, 4)
			for i := 0; i < 4; i++ {
				// Quad indices are into the selected subsets; map them back.
				rp, ip := refSel[rq.Idx[i]], imgSel[iq.Idx[i]]
				seed[i] = Match{Vec: refVec[rp], X: imgPts[ip].X, Y: imgPts[ip].Y}
			}
			// Rotation only: four clustered stars cannot pin the focal length.
			cam := fit(prior, seed, 3)
			loose := collectMatches(cam, cat, det, o.VerifyRadiusPx)
			if len(loose) < 12 {
				continue // not even worth refitting
			}
			// Re-fit on everything that agreed, which spreads the constraint over the whole field
			// instead of one small quad, then judge on the TIGHT radius.
			cam = fit(cam, loose, 4)
			confirmed := collectMatches(cam, cat, det, o.ConfirmRadiusPx)
			if len(confirmed) >= 8 {
				cam = fit(cam, confirmed, 4)
				confirmed = collectMatches(cam, cat, det, o.ConfirmRadiusPx)
			}
			if len(confirmed) > Diag.BestInliers {
				Diag.BestInliers = len(confirmed)
			}
			if len(confirmed) < o.MinInliers || len(confirmed) <= bestSol.Matches {
				continue
			}
			best, found = cam, true
			bestSol = Solution{
				Matches:           len(confirmed),
				RMSPx:             rms(confirmed),
				ScaleArcsecPerPix: 3600 * 180 / math.Pi / cam.F,
			}
		}
	}
	return best, bestSol, found
}

// Diag reports what the last solve saw: how many quads each side produced, how many code matches
// were verified, and the best confirmed inlier count. Keep it — a failed solve is uninterpretable
// without these, and the difference between "no candidates" and "candidates that never verify"
// points at completely different causes.
var Diag struct {
	RefQuads, ImgQuads, Candidates, BestInliers int
	RefStars, ImgStars                          int
}
