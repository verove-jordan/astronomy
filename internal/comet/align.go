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
	if ref == nil || target == nil || ref.W != target.W || ref.H != target.H {
		return 0, 0
	}
	// Blur both planes at coma scale first: point-like stars (a few px, and co-located across channels so
	// they pull the correlation toward zero shift) fade away, while the extended coma survives — so the
	// correlation locks onto the comet, not the star field.
	const comaBlur = 10
	ref = &fits.Image{W: ref.W, H: ref.H, C: 1, Pix: [][]float32{boxBlur(ref.Pix[0], ref.W, ref.H, comaBlur)}}
	target = &fits.Image{W: target.W, H: target.H, C: 1, Pix: [][]float32{boxBlur(target.Pix[0], target.W, target.H, comaBlur)}}
	best, bx, by := -2.0, 0.0, 0.0
	maxI := int(maxShift)
	for sy := -maxI; sy <= maxI; sy++ {
		for sx := -maxI; sx <= maxI; sx++ {
			if c := zncc(ref, target, center, radius, float64(sx), float64(sy)); c > best {
				best, bx, by = c, float64(sx), float64(sy)
			}
		}
	}
	// Refine to ¼-pixel around the integer peak.
	cx, cy := bx, by
	for sy := cy - 0.75; sy <= cy+0.75+1e-9; sy += 0.25 {
		for sx := cx - 0.75; sx <= cx+0.75+1e-9; sx += 0.25 {
			if c := zncc(ref, target, center, radius, sx, sy); c > best {
				best, bx, by = c, sx, sy
			}
		}
	}
	return bx, by
}

// zncc is the zero-mean normalized cross-correlation in [-1,1] between ref (at center) and target
// (sampled at center shifted by (sx,sy), bilinear) over the window. A shift (sx,sy) that scores highest
// is the one Translate(target, sx, sy) would apply to register target onto ref.
func zncc(ref, target *fits.Image, center Point, radius int, sx, sy float64) float64 {
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
