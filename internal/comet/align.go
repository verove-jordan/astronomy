package comet

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// AlignToReference returns the sub-pixel shift (dx,dy) to apply to target with Translate so its content
// best matches ref over a square window of half-size `radius` centred on `center`, searching integer
// shifts up to ±maxShift then refining to ¼-pixel. It maximizes zero-mean normalized cross-correlation
// (ZNCC) — robust to per-channel brightness/contrast — i.e. spatial-domain phase correlation focused on
// the coma. Used to cross-align the per-channel comet stacks so the colour comet has no channel offset.
func AlignToReference(ref, target *fits.Image, center Point, radius int, maxShift float64) (dx, dy float64) {
	// Blur at coma scale: point-like stars (a few px, co-located across channels so they pull the
	// correlation toward zero shift) fade away, while the extended coma survives — so the correlation
	// locks onto the comet, not the star field.
	return AlignToReferenceBlur(ref, target, center, radius, maxShift, 10)
}

// AlignToReferenceBlur is AlignToReference with an explicit pre-blur radius. Surface imaging
// (Moon/planets) passes a SMALL blur so the correlation locks onto sharp craters/limb detail; comet
// coma uses ~10 to fade stars. blur <= 0 correlates the raw planes (finest, noisiest). The integer ZNCC
// peak is refined by a 1-D parabola per axis (see AlignSeeded), not the old ¼-pixel grid.
func AlignToReferenceBlur(ref, target *fits.Image, center Point, radius int, maxShift float64, blur int) (dx, dy float64) {
	return AlignSeeded(ref, target, center, radius, maxShift, blur, 0, 0)
}

// AlignSeeded searches integer offsets over round(seed) ± maxShift, then refines the correlation peak by
// a 1-D parabola per axis on the peak's four integer neighbours, and returns the ABSOLUTE sub-pixel shift
// registering target onto ref. Fitting the parabola at INTEGER offsets is deliberate: there zncc samples
// exact pixels (no bilinear smoothing), so the correlation surface is interpolation-bias-free and the
// vertex localizes the peak to ~0.05–0.1 px with no FFT. seed=(0,0) reproduces the un-seeded aligner; a
// caller with a known global drift passes it as the seed so the small ±maxShift search measures only the
// local residual — letting the total shift be measured against the ORIGINAL frame, with no pre-translate.
func AlignSeeded(ref, target *fits.Image, center Point, radius int, maxShift float64, blur int, seedX, seedY float64) (dx, dy float64) {
	return AlignSeededMasked(ref, target, center, radius, maxShift, blur, seedX, seedY, nil)
}

// AlignSeededMasked is AlignSeeded over part of the window only: keep, when non-nil, is indexed in
// the REFERENCE's coordinates and false wherever a pixel must not vote.
//
// It exists for content that moves independently of what is being registered. Correlating an
// eclipsed Sun, the occulter is the highest-contrast feature in the frame AND the only one that
// travels between frames, so the shift that best matches the two images is partly the Moon's motion
// applied to the Sun. Excluding it is the difference between registering the subject and registering
// the thing in front of it.
//
// The mask is stated in reference coordinates and must therefore already cover wherever the moving
// content reaches in the TARGET across the whole search, because the target is sampled at an offset:
// a mask that only covered the reference's copy would let the target's slide under it.
func AlignSeededMasked(ref, target *fits.Image, center Point, radius int, maxShift float64, blur int, seedX, seedY float64, keep []bool) (dx, dy float64) {
	if ref == nil || target == nil || ref.W != target.W || ref.H != target.H {
		return seedX, seedY
	}
	if blur > 0 {
		ref = &fits.Image{W: ref.W, H: ref.H, C: 1, Pix: [][]float32{boxBlur(ref.Pix[0], ref.W, ref.H, blur)}}
		target = &fits.Image{W: target.W, H: target.H, C: 1, Pix: [][]float32{boxBlur(target.Pix[0], target.W, target.H, blur)}}
	}
	bx, by, c0 := integerPeak(ref, target, center, radius, maxShift, seedX, seedY, keep)
	sx := bx + parabola(zncc(ref, target, center, radius, bx-1, by, keep), c0, zncc(ref, target, center, radius, bx+1, by, keep))
	sy := by + parabola(zncc(ref, target, center, radius, bx, by-1, keep), c0, zncc(ref, target, center, radius, bx, by+1, keep))
	return sx, sy
}

// integerPeak returns the integer offset (bx,by) maximizing ZNCC over round(seed) ± maxShift, plus that
// peak's correlation value (for the parabolic refine).
func integerPeak(ref, target *fits.Image, center Point, radius int, maxShift, seedX, seedY float64, keep []bool) (bx, by, best float64) {
	best = -2
	baseX, baseY := math.Round(seedX), math.Round(seedY)
	maxI := int(maxShift)
	for dy := -maxI; dy <= maxI; dy++ {
		for dx := -maxI; dx <= maxI; dx++ {
			ox, oy := baseX+float64(dx), baseY+float64(dy)
			if c := zncc(ref, target, center, radius, ox, oy, keep); c > best {
				best, bx, by = c, ox, oy
			}
		}
	}
	return bx, by, best
}

// parabola returns the sub-integer peak offset from a concave 3-point fit (correlation values at -1,0,+1
// relative to the integer peak), clamped to ±0.5. It returns 0 when the samples are not a concave
// interior maximum (a flat or degenerate window), leaving the integer peak unshifted.
func parabola(cMinus, c0, cPlus float64) float64 {
	den := cMinus - 2*c0 + cPlus
	if den >= 0 {
		return 0
	}
	d := 0.5 * (cMinus - cPlus) / den
	if d < -0.5 {
		return -0.5
	}
	if d > 0.5 {
		return 0.5
	}
	return d
}

// zncc is the zero-mean normalized cross-correlation in [-1,1] between ref (at center) and target
// (sampled at center shifted by (sx,sy), bilinear) over the window. A shift (sx,sy) that scores highest
// is the one Translate(target, sx, sy) would apply to register target onto ref.
func zncc(ref, target *fits.Image, center Point, radius int, sx, sy float64, keep []bool) float64 {
	w, h := ref.W, ref.H
	rsrc, tsrc := ref.Pix[0], target.Pix[0]
	cx, cy := int(math.Round(center.X)), int(math.Round(center.Y))
	var n, sr, st, srr, stt, srt float64
	for y := cy - radius; y <= cy+radius; y++ {
		if y < 0 || y >= h {
			continue
		}
		for x := cx - radius; x <= cx+radius; x++ {
			if x < 0 || x >= w {
				continue
			}
			if keep != nil && !keep[y*w+x] {
				continue
			}
			rv := float64(rsrc[y*w+x])
			tv := float64(bilinear(tsrc, w, h, float64(x)-sx, float64(y)-sy))
			n++
			sr += rv
			st += tv
			srr += rv * rv
			stt += tv * tv
			srt += rv * tv
		}
	}
	if n == 0 {
		return -2
	}
	den := (n*srr - sr*sr) * (n*stt - st*st)
	if den <= 0 {
		return -2
	}
	return (n*srt - sr*st) / math.Sqrt(den)
}
