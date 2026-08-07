package solar

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/comet"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// apalign.go corrects atmospheric distortion across the disc.
//
// A global similarity — scale, rotation, translation — assumes the whole frame moved as one rigid
// thing. Seeing does not work that way: the atmosphere is a field of cells, each bending the light
// from its own patch of sky by its own amount, so at any instant one side of the disc is displaced
// several pixels differently from the other. Registering globally and stacking then averages
// features against slightly shifted copies of themselves, and the result is a stack that is cleaner
// than any single frame but visibly softer — detail lost everywhere, which is the classic symptom.
//
// The fix, and what the Moon path has always done, is multi-point alignment: lay a grid of
// alignment points over the subject, measure each one's local displacement, and warp by that field
// instead of by one rigid transform.
//
// Two details make it work rather than make things worse. The measured field is filtered — a point
// that locks onto noise would drag its whole neighbourhood — and the field is composed with the
// similarity into a SINGLE resample, because correcting distortion by interpolating twice would
// give back in blur what it won in alignment.

const (
	// apCellPx is the target spacing between alignment points. It is a compromise: wide enough that
	// each patch holds real structure to correlate on, narrow enough to follow a seeing cell.
	apCellPx             = 110
	apGridMin, apGridMax = 8, 28
	// apMaxShift bounds the search, in canonical pixels. Seeing distortion at this aperture is a few
	// pixels; anything larger is a mislock, not a measurement.
	apMaxShift = 7.0
	// apBlur smooths each patch before correlating, so the match follows structure rather than noise.
	apBlur = 1
	// apMinStructure is the local CONTRAST — standard deviation over mean — below which a point has
	// nothing to lock onto. Stating it as a ratio rather than an absolute variance is what keeps it
	// meaningful across exposures, and across a session that bracketed over two orders of magnitude.
	// Quiet Sun in white light through a small aperture genuinely has almost no resolvable texture,
	// so those points must be dropped rather than allowed to return a noise measurement.
	apMinStructure = 0.004
	// apOutlierPx is how far a point may disagree with its neighbours before it is discarded.
	apOutlierPx = 2.5
	// apMeasureScale is how much the canonical raster is reduced before the field is MEASURED.
	//
	// It was 4, on the reasoning that a seeing-induced distortion field varies over tens of pixels so
	// a quarter-scale measurement loses nothing. The reasoning confused the field's SCALE with its
	// AMPLITUDE. What has to be resolved is not how far apart the wrinkles are, it is how far each
	// one moved — and after a limb fit has already removed the rigid part, that is a few pixels at
	// full scale and therefore under one pixel at quarter scale. The correlator was being asked for a
	// sub-pixel answer from a raster that could not represent one, so it returned noise, and every
	// frame was then warped by that noise.
	//
	// The cost of being wrong was not subtle, and it fell almost entirely on the OUTER disc, where a
	// field error is largest and where there is least structure to constrain it. Measured on a real
	// 1.06"/px capture, fine contrast at 0.9 R as a fraction of disc centre:
	//
	//	reduction 4 (old)  43%     reduction 2  57%     reduction 1  85%     no field at all  101%
	//
	// A single frame shows no fall-off at all, so the whole of that was manufactured — and it is
	// exactly the "sharp in the middle, out of focus at the edges" that sends people looking for a
	// collimation or field-curvature problem they do not have.
	//
	// Measuring at full scale is several times slower and buys back nearly all of it, while still
	// resolving more centre detail than a rigid stack. `ap_scale` re-opens the trade for anyone who
	// wants the speed back — and the coarse-to-fine ladder below makes the full-scale answer cost far
	// less than a full-scale search would.
	apMeasureScale = 1
	// apCoarseReduce is the reduction the first pass of the ladder measures at, and apRefineShift is
	// how far, in full-raster pixels, the second pass then searches around what the first found.
	//
	// The cost of a correlation is the patch area times the number of offsets tried, and reducing the
	// raster shrinks both — which is why a coarse pass is nearly free and a full-scale search is not.
	// The ladder keeps the cheap part cheap and spends full resolution only where it is needed: on
	// resolving the last couple of pixels, which is exactly the part a reduced raster cannot do.
	//
	// A coarse pass at half scale locates each node to about a pixel, so a two-pixel refinement window
	// covers it with room to spare. That is 25 offsets at full scale instead of 225.
	apCoarseReduce = 2
	apRefineShift  = 2.0
)

// apField is a measured distortion field on a regular grid over the canonical raster.
type apField struct {
	n      int       // grid is n×n
	side   int       // canonical raster side the grid spans
	dx, dy []float64 // displacement at each node, in canonical pixels
	ok     []bool
	valid  int
}

// apGridN picks the grid density for a disc.
func apGridN(side int) int {
	n := side / apCellPx
	if n < apGridMin {
		n = apGridMin
	}
	if n > apGridMax {
		n = apGridMax
	}
	return n
}

// nodeAt returns the centre of grid node (i, j) on a raster of the given side.
func nodeAt(i, j, n, side int) (x, y float64) {
	step := float64(side-1) / float64(n-1)
	return float64(i) * step, float64(j) * step
}

// at samples the field at a canonical-space point, bilinearly.
func (f apField) at(x, y float64) (dx, dy float64) {
	if f.n < 2 || f.valid == 0 {
		return 0, 0
	}
	step := float64(f.side-1) / float64(f.n-1)
	gx, gy := x/step, y/step
	i, j := clampInt(int(gx), 0, f.n-2), clampInt(int(gy), 0, f.n-2)
	tx, ty := clampF(gx-float64(i), 0, 1), clampF(gy-float64(j), 0, 1)
	for _, c := range [][3]float64{{0, 0, (1 - tx) * (1 - ty)}, {1, 0, tx * (1 - ty)},
		{0, 1, (1 - tx) * ty}, {1, 1, tx * ty}} {
		k := (j+int(c[1]))*f.n + i + int(c[0])
		dx += f.dx[k] * c[2]
		dy += f.dy[k] * c[2]
	}
	return dx, dy
}

// measureAPField measures the distortion of target relative to ref, both already in canonical space.
// The two images are the canonical-space reference and frame; they may be reduced copies, in which
// case fullSide states the raster the returned field must apply to.
func measureAPField(ref, target *fits.Image, l Limb, n int) apField {
	return measureAPFieldAt(ref, target, l, n, ref.W)
}

// measureAPFieldAt measures on the given (possibly reduced) images and returns a field expressed on
// a raster of fullSide pixels.
func measureAPFieldAt(ref, target *fits.Image, l Limb, n, fullSide int) apField {
	return measureAPFieldScaled(ref, target, l, n, fullSide, apMeasureScale)
}

// apPass is one rung of the measurement ladder: the reduction it measures at, and how far it
// searches — in full-raster pixels — around whatever seed it is handed.
type apPass struct {
	reduce int
	search float64
}

// apLadder is the schedule of passes for a requested measurement reduction.
//
// Asking for a reduced measurement is taken literally: one pass, at that reduction, searching the
// full range. That is the speed setting, and it should do exactly what it says.
//
// Asking for full resolution runs COARSE-TO-FINE instead of one expensive full-scale search, because
// the two are not the same price for the same answer. A full-scale search spends most of its work
// discovering roughly where the match is — a question a reduced raster answers perfectly well — and
// only the last pixel or two genuinely needs full resolution. Splitting it that way measured the same
// field for a fraction of the time.
func apLadder(reduce int) []apPass {
	if reduce > 1 {
		return []apPass{{reduce: reduce, search: apMaxShift}}
	}
	return []apPass{{reduce: apCoarseReduce, search: apMaxShift}, {reduce: 1, search: apRefineShift}}
}

// measureAPFieldScaled is measureAPFieldAt with an explicit measurement reduction.
func measureAPFieldScaled(ref, target *fits.Image, l Limb, n, fullSide, reduce int) apField {
	if reduce < 1 {
		reduce = 1
	}
	f := apField{n: n, side: fullSide, dx: make([]float64, n*n), dy: make([]float64, n*n), ok: make([]bool, n*n)}
	for _, pass := range apLadder(reduce) {
		measureAPPass(&f, ref, target, l, n, fullSide, pass)
	}
	rejectAPOutliers(&f)
	fillAPGaps(&f)
	return f
}

// measureAPPass measures every node at one rung, seeded by what the field already holds.
//
// Seeds are carried in FULL-raster pixels and converted per pass, so a rung neither knows nor cares
// what reduction produced them. A node the previous rung could not measure is seeded from the median
// of the ones it could: an unmeasured node is a node with no local structure, not a node with no
// displacement, and starting it at zero would send the refinement searching two pixels around
// somewhere the field never was.
func measureAPPass(f *apField, ref, target *fits.Image, l Limb, n, fullSide int, pass apPass) {
	refS, _ := boxDownTo(ref, ref.W/pass.reduce)
	tgtS, _ := boxDownTo(target, target.W/pass.reduce)
	// Blur ONCE per pass, here, rather than letting the correlator do it per alignment point.
	//
	// AlignSeeded blurs whatever it is handed, and it is handed the whole raster — so asking it to
	// blur meant blurring several megapixels again for every one of a few hundred nodes, to correlate
	// a patch a hundred pixels across. That single redundancy dwarfed the correlation it was there to
	// support, and it is why measuring at full scale looked inherently expensive: the cost that grew
	// was never the search, it was blurring the frame four hundred times.
	refS = blurPlane(refS, apBlur)
	tgtS = blurPlane(tgtS, apBlur)
	// Everything below works in the reduced images; scale converts back to the full raster.
	scale := float64(fullSide) / float64(refS.W)
	nodeScale := float64(ref.W) / float64(refS.W)
	radius := int(float64(refS.W)/float64(n-1)/2) + 4
	seedX, seedY := seedFor(f)

	prevDx, prevDy, prevOK := f.dx, f.dy, f.ok
	f.dx = make([]float64, n*n)
	f.dy = make([]float64, n*n)
	f.ok = make([]bool, n*n)

	for j := 0; j < n; j++ {
		for i := 0; i < n; i++ {
			k := j*n + i
			x, y := nodeAt(i, j, n, ref.W)
			// Only points whose whole PATCH is on the disc. Testing the node's own distance is not
			// enough: a patch centred just inside the limb still reaches across it, and the limb is a
			// step edge that any correlator locks onto perfectly — such a point reports near-zero
			// distortion with high confidence and quietly flattens the field around the whole rim.
			if math.Hypot(x-l.CX, y-l.CY)+float64(radius)*nodeScale > 0.92*l.R {
				continue
			}
			sx, sy := x/nodeScale, y/nodeScale
			if !hasStructure(refS, sx, sy, radius) {
				continue
			}
			hx, hy := seedX, seedY
			if prevOK[k] {
				hx, hy = prevDx[k], prevDy[k]
			}
			dx, dy := comet.AlignSeeded(refS, tgtS, comet.Point{X: sx, Y: sy}, radius,
				pass.search/nodeScale, 0, hx/scale, hy/scale)
			f.dx[k], f.dy[k], f.ok[k] = dx*scale, dy*scale, true
		}
	}
}

// blurPlane returns a blurred copy, or the original when no blur is asked for. It uses the same box
// blur the correlator would have applied, so hoisting it changes what it costs and not what it finds.
func blurPlane(im *fits.Image, r int) *fits.Image {
	if r <= 0 {
		return im
	}
	return &fits.Image{W: im.W, H: im.H, C: 1, Pix: [][]float32{comet.BoxBlur(im.Pix[0], im.W, im.H, r)}}
}

// seedFor is the displacement an unmeasured node starts from: the median of whatever the previous
// rung did measure, or nothing at all on the first rung.
func seedFor(f *apField) (x, y float64) {
	var xs, ys []float64
	for k := range f.ok {
		if f.ok[k] {
			xs, ys = append(xs, f.dx[k]), append(ys, f.dy[k])
		}
	}
	if len(xs) == 0 {
		return 0, 0
	}
	return median(xs), median(ys)
}

// hasStructure reports whether a patch holds enough contrast to correlate on.
func hasStructure(im *fits.Image, cx, cy float64, r int) bool {
	x0, x1 := clampInt(int(cx)-r, 0, im.W-1), clampInt(int(cx)+r, 0, im.W-1)
	y0, y1 := clampInt(int(cy)-r, 0, im.H-1), clampInt(int(cy)+r, 0, im.H-1)
	if x1-x0 < 4 || y1-y0 < 4 {
		return false
	}
	var sum, sumSq float64
	var n int
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			v := float64(im.Pix[0][y*im.W+x])
			sum += v
			sumSq += v * v
			n++
		}
	}
	mean := sum / float64(n)
	if mean <= 1e-9 {
		return false
	}
	variance := sumSq/float64(n) - mean*mean
	if variance <= 0 {
		return false
	}
	return math.Sqrt(variance)/mean > apMinStructure
}

// rejectAPOutliers discards points that disagree with the median of their measured neighbours. One
// point locked onto noise would otherwise bend the whole surrounding field.
func rejectAPOutliers(f *apField) {
	var xs, ys []float64
	for k := range f.ok {
		if f.ok[k] {
			xs, ys = append(xs, f.dx[k]), append(ys, f.dy[k])
		}
	}
	if len(xs) < 4 {
		for k := range f.ok {
			f.ok[k] = false
		}
		return
	}
	mx, my := median(xs), median(ys)
	for k := range f.ok {
		if f.ok[k] && math.Hypot(f.dx[k]-mx, f.dy[k]-my) > apOutlierPx+apMaxShift/2 {
			f.ok[k] = false
		}
	}
	for k := range f.ok {
		if f.ok[k] {
			f.valid++
		}
	}
}

// fillAPGaps replaces unmeasured nodes with an affine EXTRAPOLATION of the measured ones and smooths
// the field, so it stays continuous across the veto'd regions rather than stepping at their edges.
//
// An affine, not the median, and the difference falls almost entirely on the limb. Every node whose
// patch would reach across the limb is vetoed, so the whole outer rim of the grid is unmeasured — and
// filling it with one constant says the distortion out there is whatever it averaged to on the disc.
// It is not. A hand-held phone shakes, and a rolling shutter turns that into a SHEAR: the top of the
// disc is read out several milliseconds before the bottom, so a frame captured while the phone was
// moving is not a rigid copy of one that was still, it is skewed. A shear is exactly zero on average
// over the disc and largest at the rim, so a constant fill gets the rim as wrong as it is possible to
// get it while looking perfectly reasonable everywhere the field was measured.
//
// Fitting six parameters to a few hundred nodes is heavily over-determined, and an affine is the one
// model safe to extrapolate: it cannot bend, so a fit that is right across the disc cannot be wildly
// wrong just outside it. The free-form field emphatically can, which is why it is not extrapolated.
func fillAPGaps(f *apField) {
	if f.valid == 0 {
		for k := range f.dx {
			f.dx[k], f.dy[k] = 0, 0
		}
		return
	}
	var xs, ys []float64
	for k := range f.ok {
		if f.ok[k] {
			xs, ys = append(xs, f.dx[k]), append(ys, f.dy[k])
		}
	}
	mx, my := median(xs), median(ys)
	aff, affOK := fitAPAffine(f)
	for k := range f.ok {
		if f.ok[k] {
			continue
		}
		f.dx[k], f.dy[k] = mx, my
		if affOK {
			x, y := nodeAt(k%f.n, k/f.n, f.n, f.side)
			f.dx[k], f.dy[k] = aff.at(x, y)
		}
	}
	smoothField(f.dx, f.n)
	smoothField(f.dy, f.n)
}

// apAffine is a six-parameter affine model of a distortion field: a translation, a scale, a rotation
// and a shear, expressed as two planes over the canonical raster.
type apAffine struct{ dx, dy [3]float64 }

// at evaluates the model at a canonical-space point.
func (a apAffine) at(x, y float64) (dx, dy float64) {
	return a.dx[0] + a.dx[1]*x + a.dx[2]*y, a.dy[0] + a.dy[1]*x + a.dy[2]*y
}

// fitAPAffine least-squares fits a plane to each component of the measured nodes.
//
// The coordinates are centred on the raster before fitting. Left raw they run to a couple of thousand,
// so the normal equations carry entries differing by six orders of magnitude and the solve loses most
// of its precision on a gradient that is only a pixel or two across the whole frame.
func fitAPAffine(f *apField) (apAffine, bool) {
	// Six parameters from at least four times as many constraints, or the fit is describing noise.
	if f.valid < 24 {
		return apAffine{}, false
	}
	half := float64(f.side-1) / 2
	var ata [9]float64
	var atx, aty [3]float64
	for k := range f.ok {
		if !f.ok[k] {
			continue
		}
		x, y := nodeAt(k%f.n, k/f.n, f.n, f.side)
		b := [3]float64{1, x - half, y - half}
		for r := 0; r < 3; r++ {
			atx[r] += b[r] * f.dx[k]
			aty[r] += b[r] * f.dy[k]
			for c := 0; c < 3; c++ {
				ata[r*3+c] += b[r] * b[c]
			}
		}
	}
	sx, okX := solveSmall(ata[:], atx[:], 3)
	sy, okY := solveSmall(ata[:], aty[:], 3)
	if !okX || !okY {
		return apAffine{}, false
	}
	// Back out of centred coordinates so the model can be evaluated at raw canonical positions.
	return apAffine{
		dx: [3]float64{sx[0] - sx[1]*half - sx[2]*half, sx[1], sx[2]},
		dy: [3]float64{sy[0] - sy[1]*half - sy[2]*half, sy[1], sy[2]},
	}, true
}

// smoothField applies one 3×3 mean pass over the grid.
func smoothField(v []float64, n int) {
	out := append([]float64(nil), v...)
	for j := 0; j < n; j++ {
		for i := 0; i < n; i++ {
			var s float64
			var c int
			for dj := -1; dj <= 1; dj++ {
				for di := -1; di <= 1; di++ {
					x, y := i+di, j+dj
					if x < 0 || y < 0 || x >= n || y >= n {
						continue
					}
					s += v[y*n+x]
					c++
				}
			}
			out[j*n+i] = s / float64(c)
		}
	}
	copy(v, out)
}
