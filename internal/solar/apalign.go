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
	// apMeasureScale is how much the canonical raster is reduced for measurement. A seeing-induced
	// distortion field varies over tens of pixels, so measuring it at a quarter scale loses nothing
	// and is what keeps multi-point alignment affordable — the measurement, not the correction, is
	// where the time goes.
	apMeasureScale = 4
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

// measureAPFieldScaled is measureAPFieldAt with an explicit measurement reduction.
func measureAPFieldScaled(ref, target *fits.Image, l Limb, n, fullSide, reduce int) apField {
	if reduce < 1 {
		reduce = 1
	}
	f := apField{n: n, side: fullSide, dx: make([]float64, n*n), dy: make([]float64, n*n), ok: make([]bool, n*n)}
	refS, _ := boxDownTo(ref, ref.W/reduce)
	tgtS, _ := boxDownTo(target, target.W/reduce)
	// Everything below works in the reduced images; scale converts back to the full raster.
	scale := float64(fullSide) / float64(refS.W)
	nodeScale := float64(ref.W) / float64(refS.W)
	radius := int(float64(refS.W)/float64(n-1)/2) + 4

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
			dx, dy := comet.AlignSeeded(refS, tgtS, comet.Point{X: sx, Y: sy}, radius,
				apMaxShift/nodeScale, apBlur, 0, 0)
			f.dx[k], f.dy[k], f.ok[k] = dx*scale, dy*scale, true
		}
	}
	rejectAPOutliers(&f)
	fillAPGaps(&f)
	return f
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

// fillAPGaps replaces unmeasured nodes with the median of the measured ones and smooths the field,
// so it stays continuous across the veto'd regions rather than stepping at their edges.
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
	for k := range f.ok {
		if !f.ok[k] {
			f.dx[k], f.dy[k] = mx, my
		}
	}
	smoothField(f.dx, f.n)
	smoothField(f.dy, f.n)
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
