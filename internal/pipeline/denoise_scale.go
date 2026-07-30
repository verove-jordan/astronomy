package pipeline

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/graxpert"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// minScaledSide keeps the downscaled copy large enough for the denoiser's tiling to behave.
const minScaledSide = 256

// denoiseAIScaled is the opt-in cheap variant of the joint colour denoise (ASTRO_DENOISE_SCALE ∈
// (0,1)): the AI pass runs on a bilinear-downscaled copy (~scale² of the cost), and only the
// upscaled CHROMA is transferred back — per-pixel mean/luminance preserved EXACTLY (the
// chromaSmoothRGB identity), so bilinear softness never touches detail. Luminance noise is left
// untouched by design: this suits LRGB sessions (L carries detail) more than RGB-only ones.
// ok=false → the caller runs the byte-identical full-resolution pass instead.
func denoiseAIScaled(ctx context.Context, opts Options, path string, onProgress func(siril.Progress)) (note string, ok bool) {
	s := opts.DenoiseScale
	if s <= 0 || s >= 1 {
		return "", false
	}
	im, err := fits.ReadImage(path)
	if err != nil || im.C != 3 {
		return "", false // mono has no chroma to transfer — full-res denoise applies as before
	}
	w := scaledSide(im.W, s)
	h := scaledSide(im.H, s)
	if w >= im.W || h >= im.H {
		return "", false
	}
	smallPath := strings.TrimSuffix(path, ".fits") + "_small.fits"
	smallOut := strings.TrimSuffix(path, ".fits") + "_small_dn.fits"
	if err := im.Resize(w, h).WriteFITS(smallPath); err != nil {
		return "", false
	}
	defer func() { _ = os.Remove(smallPath); _ = os.Remove(smallOut) }()

	fwd := func(p graxpert.Progress) {
		if onProgress != nil {
			onProgress(siril.Progress{Line: p.Line, Percent: p.Percent, Sample: p.Sample})
		}
	}
	if err := opts.Graxpert.Denoise(ctx, smallPath, smallOut, graxpert.DenoiseOptions{}, fwd); err != nil {
		// GraXpert itself failed — a full-resolution retry would fail the same way, only slower.
		return "GraXpert denoise skipped: " + err.Error(), true
	}
	dn, err := fits.ReadImage(smallOut)
	if err != nil {
		return "", false
	}
	transferChroma(im, dn.Resize(im.W, im.H))
	if err := im.OverwriteData(path); err != nil {
		if err := im.WriteFITS(path); err != nil {
			return "GraXpert denoise skipped: " + err.Error(), true
		}
	}
	return fmt.Sprintf("%s — chroma at %.0f%% scale, luminance untouched", denoiseAppliedNote, s*100), true
}

func scaledSide(v int, s float64) int {
	n := int(math.Round(float64(v) * s))
	if n < minScaledSide {
		n = minScaledSide
	}
	return n
}

// transferChroma replaces orig's chroma with up's while keeping orig's per-pixel mean exactly:
// out_c = up_c + (mOrig − mUp), so mean(out) = mOrig by construction (the luminance identity of
// chromaSmoothRGB) and only the colour differences move. Mutates orig in place.
func transferChroma(orig, up *fits.Image) {
	r0, g0, b0 := orig.Pix[0], orig.Pix[1], orig.Pix[2]
	r1, g1, b1 := up.Pix[0], up.Pix[1], up.Pix[2]
	for i := range r0 {
		m0 := (r0[i] + g0[i] + b0[i]) / 3
		m1 := (r1[i] + g1[i] + b1[i]) / 3
		d := m0 - m1
		r0[i] = r1[i] + d
		g0[i] = g1[i] + d
		b0[i] = b1[i] + d
	}
}

// denoiseScaleSigSuffix folds the active chroma-denoise scale into the denoise cache key: a
// changed scale must recompute, while unset/1.0 keeps the legacy key so existing caches stay valid.
func denoiseScaleSigSuffix(opts Options) string {
	if s := opts.DenoiseScale; s > 0 && s < 1 {
		return fmt.Sprintf("|scale=%.3f", s)
	}
	return ""
}
