package skypano

// fill.go extends a canvas into the part of it that is nothing.
//
// A mosaic canvas is a third to two thirds empty, and that matters the moment anything OUTSIDE this
// package is asked to model its background. Our own Flatten takes samples and knows to skip the
// uncovered ones, but a whole-image tool — GraXpert's AI background model is the case this was
// written for — has no notion of a coverage mask. Handed the canvas as it stands it fits its model
// straight through the black surround, and the model dives toward zero wherever the data stops,
// laying a dark rim just inside every edge of the picture.
//
// So the holes are filled with a smooth continuation of the sky before the tool sees them, and the
// mask is restored after. Nearest-neighbour would also keep the model off zero, but it puts a hard
// edge exactly where the model is most sensitive; a push-pull pyramid relaxes toward the surrounding
// level instead.
//
// Relaxes toward, not extrapolates: at a straight mask edge with a strong gradient across it the
// filled side steps a little away from the last known column (measured at about 15 per cent of the
// level, against a fall of 100 per cent if the hole were left at zero) and then flattens out. That is
// the right shape for this job — the fill only has to stop the model diving, and a fill that
// confidently continued the gradient would invent a slope the tool would then take back off the sky.

import "github.com/verove-jordan/astronomy/internal/fits"

// FillHoles replaces every pixel where mask is 0 with a smooth extrapolation of the pixels where it
// is non-zero, in place, one channel at a time. mask must be one value per pixel. Pixels inside the
// mask are left exactly as they were, so a filled canvas still contains the original data.
func FillHoles(im *fits.Image, mask []float32) {
	if im == nil || len(mask) != im.W*im.H {
		return
	}
	for c := 0; c < im.C; c++ {
		fillPlane(im.Pix[c], mask, im.W, im.H)
	}
}

// fillPlane is the push-pull fill for one plane: average the known pixels down a weighted pyramid,
// then come back up filling each level's holes from the level above it.
func fillPlane(pix, mask []float32, w, h int) {
	type level struct {
		v, m []float32
		w, h int
	}
	l0 := level{v: make([]float32, len(pix)), m: make([]float32, len(pix)), w: w, h: h}
	any := false
	for i := range pix {
		if mask[i] > 0 {
			l0.v[i], l0.m[i] = pix[i], 1
			any = true
		}
	}
	if !any {
		return // nothing to extrapolate FROM; leave the plane alone rather than invent a value
	}

	// Down to 1x1, not to 2x2. The coarsest level has to be a SINGLE cell, because that is the only
	// level guaranteed to carry weight wherever the known pixels are: stop at 2x2 and a canvas whose
	// whole right half is empty still has an empty cell at the top of the pyramid, with nothing above
	// it to fill from, and the pull leaves that half at zero — the exact failure this fill exists to
	// prevent.
	levels := []level{l0}
	for {
		cur := levels[len(levels)-1]
		if cur.w <= 1 && cur.h <= 1 {
			break
		}
		nw, nh := max((cur.w+1)/2, 1), max((cur.h+1)/2, 1)
		nv, nm := make([]float32, nw*nh), make([]float32, nw*nh)
		for y := 0; y < nh; y++ {
			for x := 0; x < nw; x++ {
				var sv, sm, n float32
				for dy := 0; dy < 2; dy++ {
					for dx := 0; dx < 2; dx++ {
						sx, sy := 2*x+dx, 2*y+dy
						if sx >= cur.w || sy >= cur.h {
							continue
						}
						i := sy*cur.w + sx
						sv += cur.v[i] * cur.m[i]
						sm += cur.m[i]
						n++
					}
				}
				if j := y*nw + x; sm > 0 {
					nv[j], nm[j] = sv/sm, sm/n
				}
			}
		}
		levels = append(levels, level{v: nv, m: nm, w: nw, h: nh})
	}

	for l := len(levels) - 2; l >= 0; l-- {
		fine, coarse := levels[l], levels[l+1]
		for y := 0; y < fine.h; y++ {
			for x := 0; x < fine.w; x++ {
				i := y*fine.w + x
				a := float64(fine.m[i])
				if a >= 1 {
					continue
				}
				up := bilinearAt(coarse.v, coarse.w, coarse.h, (float64(x)-0.5)/2, (float64(y)-0.5)/2)
				fine.v[i] = float32(float64(fine.v[i])*a + up*(1-a))
				fine.m[i] = 1
			}
		}
	}
	copy(pix, levels[0].v)
}

func bilinearAt(p []float32, w, h int, fx, fy float64) float64 {
	if fx < 0 {
		fx = 0
	}
	if fy < 0 {
		fy = 0
	}
	if fx > float64(w-1) {
		fx = float64(w - 1)
	}
	if fy > float64(h-1) {
		fy = float64(h - 1)
	}
	x0, y0 := int(fx), int(fy)
	x1, y1 := min(x0+1, w-1), min(y0+1, h-1)
	tx, ty := fx-float64(x0), fy-float64(y0)
	v00, v10 := float64(p[y0*w+x0]), float64(p[y0*w+x1])
	v01, v11 := float64(p[y1*w+x0]), float64(p[y1*w+x1])
	return (v00*(1-tx)+v10*tx)*(1-ty) + (v01*(1-tx)+v11*tx)*ty
}
