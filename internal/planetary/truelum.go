package planetary

import (
	"fmt"
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// True-luminance re-imposition. Siril's `rgbcomp -lum=L` does NOT take the output lightness purely
// from L — the soft, un-deconvolved, chroma-blurred R/G/B lightness leaks in and visibly dilutes
// the sharp L master that carries the detail (the acceptance gate can't see it: it measures L
// BEFORE the compose). Fix: rescale every pixel of the linear composite by (L+ε)/(lum+ε) so its
// mean luminance equals L exactly while per-channel ratios (chromaticity) are preserved.
const (
	trueLumEps      = 1e-4 // stabilises the ratio where both sides are near-zero sky
	trueLumMaxRatio = 4.0  // bounds the correction where the composite is locally near-black
)

// reimposeLuminance rewrites outBase.fits (the rgbcomp composite) with L re-imposed as its exact
// luminance. Soft-fail: any problem returns a note and leaves the composite as Siril wrote it.
func reimposeLuminance(outBase, lBase string) string {
	comp, order, err := readFinish(outBase)
	if err != nil {
		return "true-lum skipped: " + err.Error()
	}
	if comp.C != 3 {
		return fmt.Sprintf("true-lum skipped: composite has %d channel(s)", comp.C)
	}
	lum, err := readAligned(lBase, comp, order)
	if err != nil {
		return "true-lum skipped: " + err.Error()
	}
	applyTrueLum(comp, lum.Pix[0])
	if werr := comp.OverwriteData(outBase + ".fits"); werr != nil {
		return "true-lum skipped: write composite: " + werr.Error()
	}
	return ""
}

// applyTrueLum rescales the composite in place so mean(R,G,B) == L per pixel, ratios preserved.
func applyTrueLum(comp *fits.Image, l []float32) {
	r, g, b := comp.Pix[0], comp.Pix[1], comp.Pix[2]
	for i := range l {
		lum := (float64(r[i]) + float64(g[i]) + float64(b[i])) / 3
		f := (float64(l[i]) + trueLumEps) / (lum + trueLumEps)
		if f > trueLumMaxRatio {
			f = trueLumMaxRatio
		}
		if f < 0 {
			f = 0
		}
		r[i] = float32(math.Min(1, float64(r[i])*f))
		g[i] = float32(math.Min(1, float64(g[i])*f))
		b[i] = float32(math.Min(1, float64(b[i])*f))
	}
}
