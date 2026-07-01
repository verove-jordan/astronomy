package nightscape

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// Auto-levels. Instead of fixed black/white/intensity constants, derive the stretch from each image's
// own statistics. The black and white points are percentiles of the luminance (adaptive), and the asinh
// intensity is *solved* so the sky background lands on a target level. Asinh (not a midtone-transfer
// function) is used because the linear sky has a huge dynamic range with a tiny background — asinh
// compresses it gracefully where an MTF tuned to the same background would blow the whole Milky Way to
// white. Linked across channels (one transfer for R/G/B) to preserve colour.
const (
	autoBlackPct = 16.0 // starting luminance percentile for the (global) black point. Moderate (v5): now
	//                     that the v5 flatten removes the warm horizon gradient and v4's sigma-clip + chromaBlur
	//                     cleaned the floor, the firm v4 clip (22, looped to 60) only crushed the dark top to
	//                     PURE black while the orange bottom survived — unnatural. Kept gentle so the now-
	//                     homogeneous sky lands as a natural dark grey everywhere; raised by the loop only as
	//                     needed to reach the target (capped lower, 45, to not expose the ragged top drift edge).
	autoWhitePct = 99.99 // global white point: near the max so the bright Milky-Way core keeps its
	//                      dust-lane gradient. A lower 99.9 clipped the large core to a flat 1.0 that then
	//                      read as a "burned"/detail-less blob; compressHighlights does the actual highlight
	//                      rolloff (to the look's ceiling), which now has real gradient to roll off.
)

// autoStretch stretches the sky so its background sits at targetBg, in place, with a GLOBAL (linked)
// black/white and a linked asinh — the smooth, hue-preserving transfer the reference recipe uses (a
// per-channel black *clip* speckles noisy darks, so the colour cast is removed upstream by a gentle
// per-channel neutralise + the mask-aware flatten, not here). The black point is raised until the
// normalized background is below the target, so a darker target clips more of the sky floor; then the
// asinh intensity is solved so the background lands exactly on targetBg. A high white point keeps the
// brightest core from saturating wide; compressHighlights then rolls the very core to a golden glow.
// Statistics use the sky region only (skyMask>0.5) so the dark foreground/fill can't skew the levels.
func autoStretch(im *fits.Image, targetBg float64, skyMask []float32) {
	if targetBg <= 0 || targetBg >= 0.5 {
		targetBg = 0.06
	}
	lum := skyLuminance(im, skyMask, 300000)
	wp := percentile(lum, autoWhitePct)
	med := percentile(lum, 50)

	blackPct := autoBlackPct
	bp := percentile(lum, blackPct)
	bgNorm := (med - bp) / (wp - bp)
	for bgNorm >= targetBg*0.9 && blackPct < 45 {
		blackPct += 3
		bp = percentile(lum, blackPct)
		if wp-bp < 1e-6 {
			wp = bp + 1e-6
		}
		bgNorm = (med - bp) / (wp - bp)
	}
	if bgNorm < 1e-5 {
		bgNorm = 1e-5
	}
	intensity := solveAsinhIntensity(bgNorm, targetBg)

	bpf, invSpan := float32(bp), float32(1.0/(wp-bp))
	denom := math.Asinh(intensity)
	for ch := 0; ch < im.C; ch++ {
		p := im.Pix[ch]
		for i := range p {
			x := float64((p[i] - bpf) * invSpan)
			if x < 0 {
				x = 0
			} else if x > 1 {
				x = 1
			}
			p[i] = float32(math.Asinh(x*intensity) / denom)
		}
	}
}

// skyLuminance returns up to n evenly-spaced luminance samples from the sky region (skyMask>0.5), so
// stretch statistics ignore the dark foreground and low-coverage fill. A nil mask uses the whole image.
func skyLuminance(im *fits.Image, skyMask []float32, n int) []float32 {
	lum := luminance(im)
	if skyMask == nil || len(skyMask) != len(lum) {
		return subsample(lum, n)
	}
	sky := make([]float32, 0, len(lum)/2)
	for i, m := range skyMask {
		if m > 0.5 {
			sky = append(sky, lum[i])
		}
	}
	if len(sky) == 0 {
		return subsample(lum, n)
	}
	return subsample(sky, n)
}

// solveAsinhIntensity finds the asinh intensity β>1 such that asinh(bgNorm·β)/asinh(β) = targetBg.
// The ratio rises monotonically from ~bgNorm (β→1) toward 1 (β→∞), so a bisection on [1, 1e5]
// converges. Falls back to a gentle 5 if the target is unreachable (bgNorm ≥ targetBg).
func solveAsinhIntensity(bgNorm, targetBg float64) float64 {
	ratio := func(beta float64) float64 { return math.Asinh(bgNorm*beta) / math.Asinh(beta) }
	lo, hi := 1.0, 1e5
	if ratio(hi) < targetBg || bgNorm >= targetBg {
		return 5
	}
	for i := 0; i < 60; i++ {
		mid := math.Sqrt(lo * hi) // geometric bisection (β spans orders of magnitude)
		if ratio(mid) < targetBg {
			lo = mid
		} else {
			hi = mid
		}
	}
	return math.Sqrt(lo * hi)
}
