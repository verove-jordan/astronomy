package nightscape

// crop.go trims a stack down to the part of the canvas the whole sequence actually covered.
//
// An untracked sequence drifts. Registration puts every frame on the reference frame's canvas, so
// the edges are reached by only some of the frames and the corners by almost none. Those pixels are
// not wrong — they are just thinner, and the recipe fades them into an extrapolated fill — but they
// poison every statistic measured afterwards. autoStretch picks its black point from a low
// percentile of the frame, and with a soft dark rim in shot that percentile lands in the rim rather
// than in sky, so the black point is set below the real background and the whole image comes out
// washed out. Cropping first is what makes the grade measure sky and only sky.

import "github.com/verove-jordan/astronomy/internal/fits"

const (
	// cropEdgeFrac is the share of an edge row or column that must be covered for that edge to be
	// kept. Set high: one thin sliver of rim is enough to drag a percentile.
	cropEdgeFrac = 0.98
	// cropMinSide stops the trim from eating the image if coverage is pathological.
	cropMinSide = 64
)

// box is an inclusive pixel rectangle.
type box struct{ x0, y0, x1, y1 int }

func (b box) width() int  { return b.x1 - b.x0 + 1 }
func (b box) height() int { return b.y1 - b.y0 + 1 }
func (b box) full(w, h int) bool {
	return b.x0 == 0 && b.y0 == 0 && b.x1 == w-1 && b.y1 == h-1
}

// coverageBox finds the rectangle to keep, given how many frames covered each pixel. It shaves whole
// rows and columns off whichever edges are still ragged, which lands close to the largest inscribed
// rectangle of a rotated footprint without needing to solve for it.
//
// minFrac must be the same floor the stack itself used. The point of cropping is to drop what was
// EXTRAPOLATED rather than measured, so the bar is exactly the bar the stack applied — no stricter.
// Demanding 90% of the sequence instead cost a low panel four fifths of its height, because a
// handful of poorly-registered frames is enough to push most of the canvas under so high a bar.
func coverageBox(counts []float64, w, h, frames int, minFrac float64) box {
	need := minFrac * float64(frames)
	b := box{0, 0, w - 1, h - 1}
	if frames <= 0 || len(counts) != w*h {
		return b
	}

	rowOK := func(y int) bool {
		n := 0
		for x := b.x0; x <= b.x1; x++ {
			if counts[y*w+x] >= need {
				n++
			}
		}
		return float64(n) >= cropEdgeFrac*float64(b.width())
	}
	colOK := func(x int) bool {
		n := 0
		for y := b.y0; y <= b.y1; y++ {
			if counts[y*w+x] >= need {
				n++
			}
		}
		return float64(n) >= cropEdgeFrac*float64(b.height())
	}

	for b.width() > cropMinSide && b.height() > cropMinSide {
		trimmed := false
		if !rowOK(b.y0) {
			b.y0++
			trimmed = true
		}
		if !rowOK(b.y1) {
			b.y1--
			trimmed = true
		}
		if !colOK(b.x0) {
			b.x0++
			trimmed = true
		}
		if !colOK(b.x1) {
			b.x1--
			trimmed = true
		}
		if !trimmed {
			break
		}
	}
	return b
}

// cropImage returns the sub-image inside b.
func cropImage(im *fits.Image, b box) *fits.Image {
	if b.full(im.W, im.H) {
		return im
	}
	out := fits.NewImage(b.width(), b.height(), im.C)
	for ch := 0; ch < im.C; ch++ {
		for y := 0; y < out.H; y++ {
			copy(out.Pix[ch][y*out.W:(y+1)*out.W], im.Pix[ch][(b.y0+y)*im.W+b.x0:(b.y0+y)*im.W+b.x0+out.W])
		}
	}
	return out
}

// cropPlane returns the sub-plane inside b.
func cropPlane(p []float32, w, h int, b box) []float32 {
	if b.full(w, h) {
		return p
	}
	out := make([]float32, b.width()*b.height())
	for y := 0; y < b.height(); y++ {
		copy(out[y*b.width():(y+1)*b.width()], p[(b.y0+y)*w+b.x0:(b.y0+y)*w+b.x0+b.width()])
	}
	return out
}
