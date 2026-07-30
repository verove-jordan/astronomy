package noise

import (
	"fmt"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// DenoiseWeighted runs the starlet denoiser in place with a per-pixel threshold weight
// (len == W·H): the effective threshold at pixel i is K[j]·Strength·weight[i]·sigma(i), so weight 1
// behaves like Denoise and larger weights denoise harder — the seam noise equalization ramps the
// weight up where fewer frames were stacked. Pixels with weight 0 stay BYTE-IDENTICAL (the
// reconstruction is copied back only where weight > 0 — reconstruction float dust must not touch
// the full-depth core). A nil weight delegates to Denoise. Errors on a length mismatch; otherwise
// soft-fails per plane exactly like Denoise.
func DenoiseWeighted(im *fits.Image, o Options, weight []float32) error {
	if im == nil || o.Strength <= 0 || im.W <= 0 || im.H <= 0 {
		return nil
	}
	if weight == nil {
		Denoise(im, o)
		return nil
	}
	if len(weight) != im.W*im.H {
		return fmt.Errorf("denoise weight plane has %d samples, image is %dx%d", len(weight), im.W, im.H)
	}
	o = withDefaults(o)
	for ch := 0; ch < im.C && ch < len(im.Pix); ch++ {
		denoisePlaneWeighted(im.Pix[ch], im.W, im.H, o, weight)
	}
	return nil
}

// denoisePlaneWeighted mirrors denoisePlane with the weight folded into every threshold and a
// masked copy-back.
func denoisePlaneWeighted(pix []float32, w, h int, o Options, weight []float32) {
	if !allFinite(pix) {
		return
	}
	cJ, wcoef := Decompose(pix, w, h, o.Scales)
	sigmaMap, mask, ok := buildMaps(pix, cJ, wcoef, w, h, o)
	if !ok {
		return
	}
	for j := range wcoef {
		kj := o.K[j]
		if kj <= 0 {
			continue
		}
		thresholdScaleWeighted(wcoef[j], sigmaMap, mask, weight, kj*o.Strength*scaleSigma(j))
	}
	out := Reconstruct(cJ, wcoef)
	if !allFinite(out) {
		return // soft-fail: keep the original plane
	}
	for i := range pix {
		if weight[i] > 0 {
			pix[i] = out[i]
		}
	}
}

// thresholdScaleWeighted is thresholdScale with the per-pixel threshold weight applied.
func thresholdScaleWeighted(w, sigmaMap, mask, weight []float32, base float64) {
	parallelRows(len(w), func(i0, i1 int) {
		for i := i0; i < i1; i++ {
			wv := float64(w[i])
			thr := softThreshold(wv, base*float64(sigmaMap[i])*float64(weight[i]))
			m := float64(mask[i])
			w[i] = float32(m*wv + (1-m)*thr)
		}
	})
}
