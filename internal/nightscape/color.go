package nightscape

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// luminance returns the per-pixel Rec.601 luminance of an image (a copy of the single plane for mono).
func luminance(im *fits.Image) []float32 {
	if im.C != 3 {
		out := make([]float32, len(im.Pix[0]))
		copy(out, im.Pix[0])
		return out
	}
	r, g, b := im.Pix[0], im.Pix[1], im.Pix[2]
	out := make([]float32, len(r))
	for i := range out {
		out[i] = 0.299*r[i] + 0.587*g[i] + 0.114*b[i]
	}
	return out
}

// neutralizeBackground aligns the per-channel sky background to a common black: each channel's
// low-percentile level is brought down to the minimum across channels, killing the global colour
// cast while leaving the highlights (star/Milky-Way-core hues) intact. (main.py neutralize_background)
func neutralizeBackground(im *fits.Image, lowPct float64) {
	if im.C != 3 {
		return
	}
	bgs := [3]float64{}
	target := math.Inf(1)
	for c := 0; c < 3; c++ {
		bgs[c] = percentile(im.Pix[c], lowPct)
		if bgs[c] < target {
			target = bgs[c]
		}
	}
	for c := 0; c < 3; c++ {
		off := float32(bgs[c] - target)
		if off <= 0 {
			continue
		}
		p := im.Pix[c]
		for i := range p {
			if v := p[i] - off; v > 0 {
				p[i] = v
			} else {
				p[i] = 0
			}
		}
	}
}

// removeGreenCast pulls the green channel down toward the mean of red and blue wherever it exceeds
// it (an SCNR-style average-neutral). amount 1.0 = full, 0 = none. (main.py remove_green_cast)
func removeGreenCast(im *fits.Image, amount float64) {
	if im.C != 3 || amount <= 0 {
		return
	}
	r, g, b := im.Pix[0], im.Pix[1], im.Pix[2]
	a := float32(amount)
	for i := range g {
		avg := (r[i] + b[i]) * 0.5
		if excess := g[i] - avg; excess > 0 {
			g[i] = g[i] - a*excess
		}
	}
}

// asinhStretch applies arcsinh(data*β)/arcsinh(β) after a black-point clip and a global white-point
// normalisation. blackPct>0 clips the darkest sky to black (deep contrast); perChannelBlack does it
// per channel to neutralise the background cast at the source. The white point is always global so
// the Milky-Way-core RGB ratios (its golden hue) survive. (main.py asinh_stretch)
func asinhStretch(im *fits.Image, intensity, normPct, blackPct float64, perChannelBlack bool) {
	clampZero(im)
	if blackPct > 0 {
		if im.C == 3 && perChannelBlack {
			for c := 0; c < 3; c++ {
				lo := float32(percentile(im.Pix[c], blackPct))
				subtract(im.Pix[c], lo)
			}
		} else {
			lo := float32(percentile(allPixels(im), blackPct))
			for c := 0; c < im.C; c++ {
				subtract(im.Pix[c], lo)
			}
		}
	}
	pHi := percentile(allPixels(im), normPct)
	if pHi > 0 {
		inv := float32(1.0 / pHi)
		for c := 0; c < im.C; c++ {
			p := im.Pix[c]
			for i := range p {
				v := p[i] * inv
				if v < 0 {
					v = 0
				} else if v > 1 {
					v = 1
				}
				p[i] = v
			}
		}
	}
	if intensity <= 1.0 {
		return
	}
	denom := math.Asinh(intensity)
	for c := 0; c < im.C; c++ {
		p := im.Pix[c]
		for i := range p {
			p[i] = float32(math.Asinh(float64(p[i])*intensity) / denom)
		}
	}
}

// boostSaturation scales each pixel's distance from its luminance by factor (1.0 = neutral).
// (main.py boost_saturation)
func boostSaturation(im *fits.Image, factor float64) {
	if im.C != 3 || factor == 1.0 {
		return
	}
	lum := luminance(im)
	f := float32(factor)
	for c := 0; c < 3; c++ {
		p := im.Pix[c]
		for i := range p {
			p[i] = lum[i] + f*(p[i]-lum[i])
		}
	}
}

// splitTone adds cool shadow / warm highlight tints weighted by luminance, with a tanh roll-off above
// the knee so highlights never clip to flat white (Milky-Way-core colour preserved). (main.py split_tone)
func splitTone(im *fits.Image, shadow, highlight [3]float64, strength, knee float64) {
	if im.C != 3 || strength == 0 {
		return
	}
	lum := luminance(im)
	r, g, b := im.Pix[0], im.Pix[1], im.Pix[2]
	chans := [3][]float32{r, g, b}
	k := knee
	for i := range lum {
		l := float64(lum[i])
		if l < 0 {
			l = 0
		} else if l > 1 {
			l = 1
		}
		smoothHi := l * l * (3 - 2*l)
		wHi := smoothHi * (1 - math.Pow(l, 4))
		wLo := (1 - smoothHi) * (1 - math.Pow(1-l, 8))
		for c := 0; c < 3; c++ {
			v := float64(chans[c][i]) + strength*(wLo*shadow[c]+wHi*highlight[c])
			if v > k {
				v = k + (1-k)*math.Tanh((v-k)/(1-k))
			}
			if v < 0 {
				v = 0
			} else if v > 1 {
				v = 1
			}
			chans[c][i] = float32(v)
		}
	}
}

// compressHighlights applies a soft shoulder in the luminance domain above knee (tanh), scaling each
// pixel's RGB by the compressed/original luminance ratio. Bright regions roll off toward — but never
// reach — `ceil` while keeping their channel ratios, so the Milky-Way core stays golden instead of
// clipping to flat white. knee outside (0,1) disables it; ceil ≤ 0 falls back to highlightCeiling.
// highlightCeiling is the DEFAULT ceiling compressHighlights maps the brightest core to when a Look
// doesn't set its own (ceil ≤ 0). Below 1.0 so the Milky-Way core reads as a bright golden/grey glow
// instead of clipping to flat white. Per-look overrides (Look.HighlightCeiling): natural goes lower
// (≈0.62) so the large core is a dim golden glow with visible dust lanes, deepsky stays punchy.
const highlightCeiling = 0.78

func compressHighlights(im *fits.Image, knee, ceil float64) {
	if im.C != 3 { // nightscape's callers are always RGB; keep the mono case out of this path
		return
	}
	CompressHighlights(im, knee, ceil)
}

// CompressHighlights applies a soft shoulder in the luminance domain above knee (tanh), scaling each
// pixel by the compressed/original luminance ratio so bright regions roll off toward — but never reach —
// `ceil` while keeping their channel ratios (hue). Works on mono (C==1) and planar RGB (C==3). knee
// outside (0,1) disables it; ceil ≤ 0 falls back to highlightCeiling. Exported so the deep-sky finish can
// cap bright star cores just below 1.0 BEFORE the MTF autostretch (which fixes 1.0→1.0), keeping their
// colour instead of clipping to white.
func CompressHighlights(im *fits.Image, knee, ceil float64) {
	if im == nil || (im.C != 1 && im.C != 3) || knee <= 0 || knee >= 1 {
		return
	}
	if ceil <= 0 {
		ceil = highlightCeiling
	}
	if knee >= ceil { // keep the shoulder well-defined (asymptote above the knee)
		knee = ceil * 0.6
	}
	lum := im.Pix[0]
	if im.C == 3 {
		lum = luminance(im)
	}
	for i := range lum {
		l := float64(lum[i])
		if l <= knee {
			continue
		}
		// Shoulder that asymptotes to `ceil` (not 1.0): bright cores roll off while keeping their channel
		// ratios (each channel scaled by comp/l → hue preserved).
		comp := knee + (ceil-knee)*math.Tanh((l-knee)/(ceil-knee))
		s := float32(comp / l)
		for c := 0; c < im.C; c++ {
			im.Pix[c][i] *= s
		}
	}
}

// --- small in-place helpers ---

func clampZero(im *fits.Image) {
	for c := 0; c < im.C; c++ {
		p := im.Pix[c]
		for i := range p {
			if p[i] < 0 {
				p[i] = 0
			}
		}
	}
}

func subtract(p []float32, v float32) {
	for i := range p {
		if x := p[i] - v; x > 0 {
			p[i] = x
		} else {
			p[i] = 0
		}
	}
}

// allPixels returns a flat view across all channels for global percentile estimates.
func allPixels(im *fits.Image) []float32 {
	if im.C == 1 {
		return im.Pix[0]
	}
	out := make([]float32, 0, len(im.Pix[0])*im.C)
	for c := 0; c < im.C; c++ {
		out = append(out, im.Pix[c]...)
	}
	return out
}
