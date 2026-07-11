package pipeline

import (
	"context"
	"fmt"
	"math"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/comet"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/noise"
)

// jointColorDenoise reports whether the combined-RGB GraXpert denoise pass will run for this run (the
// preset opts in AND GraXpert is healthy). When true the per-channel R/G/B denoise is DEFERRED to this
// single joint pass: three independent per-channel smoothing fields cannot cancel in the combine, so
// they leave coherent colour patches (worse than the fine grain they replace); one denoise on the
// combined RGB does not. L and Ha keep their own denoise (Ha is screened as a separate layer, L is the
// luminance) — only the R/G/B trio that feeds rgbcomp is deferred.
func jointColorDenoise(ctx context.Context, opts Options) bool {
	return opts.Preset != nil && opts.Preset.ColorDenoiseAI &&
		opts.Graxpert != nil && opts.Graxpert.Healthy(ctx) == nil
}

// isRGBChannel reports whether a filter is one of the R/G/B channels that feed the rgbcomp colour base
// (and whose denoise is deferred to the joint pass). L and narrowband are excluded — they are separate
// layers, not part of the combined RGB.
func isRGBChannel(filter string) bool {
	return filter == "R" || filter == "G" || filter == "B"
}

// equalizeBackgrounds additively matches the sky background of the R/G/B channel masters so the combined
// RGB has a neutral (grey) sky. A per-channel background OFFSET is otherwise stretched into a colour
// cast, and — worse — it makes the chroma noise sit off-grey, so a saturation boost turns it into
// coloured blotches. Each channel is shifted down by (bg_channel − bg_min); gains stay SPCC's job
// (additive only, never multiplicative). Idempotent: once the levels match, a second pass shifts by ~0.
// Soft-fail by contract — returns a note and any error; callers keep going on error.
func equalizeBackgrounds(rPath, gPath, bPath string) (string, error) {
	type chan_ struct {
		path string
		im   *fits.Image
		bg   float64
	}
	chans := []*chan_{{path: rPath}, {path: gPath}, {path: bPath}}
	minBg := math.Inf(1)
	for _, c := range chans {
		im, err := fits.ReadImage(c.path)
		if err != nil {
			return "", fmt.Errorf("equalize backgrounds: read %s: %w", filepath.Base(c.path), err)
		}
		if im.C < 1 || len(im.Pix) == 0 || len(im.Pix[0]) == 0 { // a malformed master → skip, never crash the combine
			return "", nil
		}
		c.im, c.bg = im, noise.Measure(im).Background
		if c.bg < minBg {
			minBg = c.bg
		}
	}
	for _, c := range chans {
		off := float32(c.bg - minBg)
		if off == 0 {
			continue
		}
		for i := range c.im.Pix[0] {
			c.im.Pix[0][i] -= off
		}
		if err := c.im.OverwriteData(c.path); err != nil {
			return "", fmt.Errorf("equalize backgrounds: write %s: %w", filepath.Base(c.path), err)
		}
	}
	return fmt.Sprintf("channel backgrounds equalized (R %.4g G %.4g B %.4g → %.4g)",
		chans[0].bg, chans[1].bg, chans[2].bg, minBg), nil
}

// chromaSmoothRGB smooths ONLY the colour of a combined RGB linear FITS in place, preserving the
// per-pixel mean EXACTLY: m=(R+G+B)/3, then c'=m+blur(c−m). By linearity blur(ΣΔ)=Σblur(Δ)=0, so
// (R'+G'+B')/3 is byte-for-byte m — every bit of luminance/detail survives (and in LRGB the L layer
// supplies detail anyway); only the mutual channel disagreements (the coherent colour patches that
// survive the joint denoise, which a stretch + saturation would amplify into red/blue blotches) are
// flattened. This is the same technique the planetary finish uses (internal/planetary smoothChroma).
// radius ≤ 0 or a non-colour image → no-op. Soft-fail: returns a note and any error.
func chromaSmoothRGB(path string, radius int) (string, error) {
	if radius <= 0 {
		return "", nil
	}
	im, err := fits.ReadImage(path)
	if err != nil {
		return "", fmt.Errorf("chroma smooth: read: %w", err)
	}
	if im.C < 3 {
		return "", nil // colour smoothing only makes sense on an RGB image
	}
	r, g, b := im.Pix[0], im.Pix[1], im.Pix[2]
	mean := make([]float32, len(r))
	for i := range mean {
		mean[i] = (r[i] + g[i] + b[i]) / 3
	}
	// Reuse ONE diff buffer across the three channels (a 16MP master is ~64 MB/plane, and a deepsky combine
	// already holds several large masters; a fresh diff per channel needlessly triples the transient churn).
	diff := make([]float32, len(r))
	for _, pix := range [][]float32{r, g, b} {
		for i := range diff {
			diff[i] = pix[i] - mean[i]
		}
		sm := comet.BoxBlur(diff, im.W, im.H, radius)
		for i := range pix {
			pix[i] = mean[i] + sm[i]
		}
	}
	if err := im.OverwriteData(path); err != nil {
		return "", fmt.Errorf("chroma smooth: write: %w", err)
	}
	return fmt.Sprintf("chroma smoothed (mean-preserving, %dpx): residual colour patches flattened", radius), nil
}
