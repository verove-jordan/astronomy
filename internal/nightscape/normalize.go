package nightscape

import "github.com/verove-jordan/astronomy/internal/fits"

// Per-frame normalization. An untracked phone shot is taken in the camera app's auto mode, so ISO and
// white balance drift frame to frame: each registered frame has a slightly different sky brightness and
// colour cast. Averaging them raw (as a plain per-pixel mean does) muddies the colour. Before stacking we
// therefore measure every frame's per-channel sky background and gain and map them onto a common
// reference (the first frame) — the same idea as Siril's `-norm=addscale`, which the in-memory stack
// bypassed.
const (
	normBgPct    = 40.0 // percentile taken as a frame's per-channel sky-background level
	normHiPct    = 95.0 // percentile used (minus background) as the frame's per-channel gain
	normMinScale = 0.5
	normMaxScale = 2.0
)

// frameNorm holds a frame's per-channel background and gain.
type frameNorm struct {
	bg   [3]float64
	gain [3]float64
}

// measureFrame estimates each channel's background and gain, sub-sampling for speed.
func measureFrame(im *fits.Image) frameNorm {
	var fn frameNorm
	for ch := 0; ch < im.C && ch < 3; ch++ {
		s := subsample(im.Pix[ch], 200000)
		bg := percentile(s, normBgPct)
		gain := percentile(s, normHiPct) - bg
		if gain < 1e-6 {
			gain = 1e-6
		}
		fn.bg[ch] = bg
		fn.gain[ch] = gain
	}
	return fn
}

// normalizeToRef maps each channel onto the reference background and gain:
// out = (in − bg)·(refGain/gain) + refBg. The scale is clamped so a single off-exposure frame cannot
// blow up the stack.
func normalizeToRef(im *fits.Image, fn, ref frameNorm) {
	for ch := 0; ch < im.C && ch < 3; ch++ {
		scale := ref.gain[ch] / fn.gain[ch]
		if scale < normMinScale {
			scale = normMinScale
		} else if scale > normMaxScale {
			scale = normMaxScale
		}
		bg, refBg, s := float32(fn.bg[ch]), float32(ref.bg[ch]), float32(scale)
		p := im.Pix[ch]
		for i := range p {
			p[i] = (p[i]-bg)*s + refBg
		}
	}
}

// subsample returns at most n evenly-spaced samples of p (for fast percentile estimates).
func subsample(p []float32, n int) []float32 {
	if len(p) <= n {
		return p
	}
	step := len(p) / n
	out := make([]float32, 0, n+1)
	for i := 0; i < len(p); i += step {
		out = append(out, p[i])
	}
	return out
}
