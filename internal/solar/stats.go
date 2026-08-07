package solar

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// stats.go holds the cheap disc measurements triage ranks and gates frames on. They are all either
// ratios or fractions, so they stay valid on a downsampled, developed probe image.

// discSummary is what one frame's disc looks like, measured on the probe plane.
type discSummary struct {
	clipped   float64 // fraction of on-disc pixels at or above saturation
	median    float64 // on-disc median level
	detail    float64 // mean |Laplacian| inside 0.85R, normalised by the median
	limbRatio float64 // median at 0.5R over median at 0.9R — the limb-darkening shape
}

// annulus radii, as fractions of the fitted radius.
const (
	innerLo, innerHi = 0.45, 0.55
	outerLo, outerHi = 0.85, 0.95
	medianRadius     = 0.90 // the disc interior the median and clipping fraction are taken over
	detailRadius     = 0.85 // stay clear of the limb: its step edge would swamp any gradient metric
	onDiscRadius     = 0.98
)

// discStats measures a fitted disc. cx, cy and r are in the coordinates of im.
func discStats(im *fits.Image, cx, cy, r float64) discSummary {
	if r <= 0 {
		return discSummary{}
	}
	p := im.Pix[0]
	var inner, outer, interior []float32
	var onDisc, clipped int
	rMed2 := (medianRadius * r) * (medianRadius * r)
	rOn2 := (onDiscRadius * r) * (onDiscRadius * r)
	for y := 0; y < im.H; y++ {
		dy := float64(y) - cy
		for x := 0; x < im.W; x++ {
			dx := float64(x) - cx
			d2 := dx*dx + dy*dy
			if d2 > rOn2 {
				continue
			}
			v := p[y*im.W+x]
			onDisc++
			if float64(v) >= satLevel {
				clipped++
			}
			if d2 <= rMed2 {
				interior = append(interior, v)
			}
			switch d := math.Sqrt(d2) / r; {
			case d >= innerLo && d <= innerHi:
				inner = append(inner, v)
			case d >= outerLo && d <= outerHi:
				outer = append(outer, v)
			}
		}
	}
	if onDisc == 0 {
		return discSummary{}
	}
	s := discSummary{clipped: float64(clipped) / float64(onDisc)}
	s.median = imgops.Percentile(imgops.Subsample(interior, 200000), 50)
	if hi := imgops.Percentile(imgops.Subsample(outer, 50000), 50); hi > 1e-9 {
		s.limbRatio = imgops.Percentile(imgops.Subsample(inner, 50000), 50) / hi
	}
	s.detail = canonicalDetail(im, cx, cy, r, s.median)
	return s
}

// canonicalDetailRadius is the disc radius, in pixels, every sharpness measurement is taken at.
const canonicalDetailRadius = 500.0

// canonicalDetail measures sharpness after resampling the disc to a fixed radius.
//
// This is what makes the figure comparable ACROSS groups, and hero selection depends on that. A
// raw Laplacian samples a fixed band in *pixels*, so a group shot at a finer plate scale scores
// higher for no other reason than its scale — it would win the hero pick against a group with four
// times the frames and genuinely more resolved detail. Normalising the disc to one radius turns the
// metric into detail per arcsec, which is the thing actually worth ranking. Resampling also
// correctly penalises an undersampled group: stretching it to the canonical radius spreads what
// detail it has over more pixels.
func canonicalDetail(im *fits.Image, cx, cy, r, median float64) float64 {
	if r <= 0 || median <= 1e-9 {
		return 0
	}
	k := canonicalDetailRadius / r
	if math.Abs(k-1) < 0.02 { // already there; skip the resample and its interpolation loss
		return discDetail(im, cx, cy, detailRadius*r, median)
	}
	side := int(2.2*canonicalDetailRadius) | 1
	out := fits.NewImage(side, side, 1)
	half := float64(side-1) / 2
	for y := 0; y < side; y++ {
		sy := cy + (float64(y)-half)/k
		for x := 0; x < side; x++ {
			sx := cx + (float64(x)-half)/k
			out.Pix[0][y*side+x] = imgops.SampleCubic(im.Pix[0], im.W, im.H, sx, sy)
		}
	}
	return discDetail(out, half, half, detailRadius*canonicalDetailRadius, median)
}

// discDetail is the mean absolute Laplacian inside a radius, divided by the disc median. Dividing
// by the median is what makes it comparable between frames shot at different exposures — which, in
// a session where the exposure ranged over 300×, is the whole point. It is a triage-grade ranking
// only; the stack itself ranks frames with planetary's noise-corrected band-pass detail metric.
func discDetail(im *fits.Image, cx, cy, r, median float64) float64 {
	if r < 2 || median <= 1e-9 {
		return 0
	}
	p := im.Pix[0]
	x0, x1 := clampInt(int(cx-r), 1, im.W-2), clampInt(int(cx+r), 1, im.W-2)
	y0, y1 := clampInt(int(cy-r), 1, im.H-2), clampInt(int(cy+r), 1, im.H-2)
	r2 := r * r
	var sum float64
	var n int
	for y := y0; y <= y1; y++ {
		dy := float64(y) - cy
		for x := x0; x <= x1; x++ {
			dx := float64(x) - cx
			if dx*dx+dy*dy > r2 {
				continue
			}
			i := y*im.W + x
			lap := 4*float64(p[i]) - float64(p[i-1]) - float64(p[i+1]) - float64(p[i-im.W]) - float64(p[i+im.W])
			sum += math.Abs(lap)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n) / median
}

// boxDownTo box-averages im down so its long edge is at most maxEdge, returning the reduced plane
// and the factor that converts its coordinates back to the original's.
func boxDownTo(im *fits.Image, maxEdge int) (*fits.Image, float64) {
	long := im.W
	if im.H > long {
		long = im.H
	}
	if long <= maxEdge || maxEdge <= 0 {
		return firstPlane(im), 1
	}
	f := (long + maxEdge - 1) / maxEdge
	w, h := im.W/f, im.H/f
	if w < 8 || h < 8 {
		return firstPlane(im), 1
	}
	out := fits.NewImage(w, h, 1)
	src, dst := im.Pix[0], out.Pix[0]
	inv := 1 / float64(f*f)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var s float64
			for dy := 0; dy < f; dy++ {
				row := (y*f + dy) * im.W
				for dx := 0; dx < f; dx++ {
					s += float64(src[row+x*f+dx])
				}
			}
			dst[y*w+x] = float32(s * inv)
		}
	}
	return out, float64(f)
}

// firstPlane returns im when it is already single-plane, else a mono view of its first plane.
func firstPlane(im *fits.Image) *fits.Image {
	if im.C == 1 {
		return im
	}
	return &fits.Image{W: im.W, H: im.H, C: 1, Pix: [][]float32{im.Pix[0]}}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
