package noise

import "github.com/verove-jordan/astronomy/internal/fits"

// Options tunes the starlet denoiser. The zero value is not meaningful; obtain a baseline from
// DefaultOptions and adjust. Any zero field (except an explicitly-zero Strength, which is a no-op)
// is filled with its default when Denoise runs.
type Options struct {
	Strength     float64   // overall multiplier (0 = no-op)
	Scales       int       // starlet scales, default 5
	K            []float64 // per-scale threshold multipliers (len==Scales); default {3,2.5,1.8,1,0}
	ProtectSNRLo float64   // SNR where structure protection starts, default 5
	ProtectSNRHi float64   // SNR where structure is fully protected, default 10
}

// DefaultOptions returns the standard deep-sky preset (Strength 1, 5 scales, coarse scale untouched).
func DefaultOptions() Options {
	return Options{
		Strength:     1.0,
		Scales:       5,
		K:            []float64{3.0, 2.5, 1.8, 1.0, 0.0},
		ProtectSNRLo: 5,
		ProtectSNRHi: 10,
	}
}

// withDefaults fills any unset field of o from DefaultOptions and normalizes K to length Scales.
func withDefaults(o Options) Options {
	d := DefaultOptions()
	if o.Scales <= 0 {
		o.Scales = d.Scales
	}
	if len(o.K) == 0 {
		o.K = d.K
	}
	if o.ProtectSNRLo == 0 {
		o.ProtectSNRLo = d.ProtectSNRLo
	}
	if o.ProtectSNRHi == 0 {
		o.ProtectSNRHi = d.ProtectSNRHi
	}
	if o.ProtectSNRHi <= o.ProtectSNRLo {
		o.ProtectSNRHi = o.ProtectSNRLo + 1
	}
	o.K = fitK(o.K, o.Scales)
	return o
}

// fitK returns a length-scales copy of k, zero-padding (no thresholding) or truncating as needed.
func fitK(k []float64, scales int) []float64 {
	out := make([]float64, scales)
	copy(out, k)
	return out
}

// Denoise runs the starlet denoiser in place over every channel of im. Strength<=0 is a no-op. It
// never panics: a plane with non-finite samples, or that cannot be safely processed, is left as-is.
func Denoise(im *fits.Image, o Options) {
	if im == nil || o.Strength <= 0 || im.W <= 0 || im.H <= 0 {
		return
	}
	o = withDefaults(o)
	for ch := 0; ch < im.C && ch < len(im.Pix); ch++ {
		denoisePlane(im.Pix[ch], im.W, im.H, o)
	}
}

// denoisePlane denoises one plane in place, soft-failing (leaving pix untouched) on any non-finite
// input or output, or when the noise floor cannot be estimated.
func denoisePlane(pix []float32, w, h int, o Options) {
	if !allFinite(pix) {
		return
	}
	cJ, wcoef := Decompose(pix, w, h, o.Scales)
	sigmaMap, mask, ok := buildMaps(pix, cJ, wcoef, w, h, o)
	if !ok {
		return
	}
	applyThresholds(wcoef, sigmaMap, mask, o)
	out := Reconstruct(cJ, wcoef)
	if !allFinite(out) {
		return // soft-fail: keep the original plane
	}
	copy(pix, out)
}
