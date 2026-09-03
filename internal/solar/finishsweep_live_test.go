package solar

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// finishsweep_live_test.go renders one persisted master under several finishes at once, so the
// LOOK can be chosen by looking rather than by re-stacking once per guess.
//
// TestRefinish_Live already re-enters the finish from a master; this extends it to a table, and —
// the part that matters — drives each variant through the SAME knobs the job API exposes rather
// than through the starlet gain vector directly. A look that can only be reached by editing the
// gains is a look the user cannot ask for again. Whatever wins here is a `params` block.
//
//	ASTRO_SOLAR_MASTER=output/<obj>/<run>/master_w01.fits \
//	ASTRO_SOLAR_OUT=/tmp/sweep go test ./internal/solar -run FinishSweep_Live -v
func TestFinishSweep_Live(t *testing.T) {
	master := os.Getenv("ASTRO_SOLAR_MASTER")
	if master == "" {
		t.Skip("set ASTRO_SOLAR_MASTER=<a run's master_wNN.fits> to sweep the finish")
	}
	outDir := os.Getenv("ASTRO_SOLAR_OUT")
	if outDir == "" {
		outDir = t.TempDir()
	}
	require.NoError(t, os.MkdirAll(outDir, 0o755))

	im, err := fits.ReadImage(master)
	require.NoError(t, err)
	mono := firstPlane(im)
	// The geometry registration produced, not a re-fit — see canonicalLimbOf. A re-fit reads the
	// disc several percent too large and every radial stage of the finish then prints rings.
	g := Pair{Sun: canonicalLimbOf(mono.W, envFloat(t, "ASTRO_SOLAR_MARGIN", defaultCropMargin))}

	base, psf, notes := ResolveFinish(mono, g.Sun, DefaultFinish())
	for _, n := range notes {
		t.Log("  ", n)
	}
	t.Logf("psf: sigma %.2f px, fwhm %.1f\", ok=%v", psf.SigmaPx, psf.FWHMArcsec, psf.OK)

	// With the canonical geometry the width now measures what the run measured, so the pin is only
	// an escape hatch rather than a correction.
	if v := os.Getenv("ASTRO_SOLAR_SIGMA"); v != "" {
		sigma, perr := strconv.ParseFloat(v, 64)
		require.NoError(t, perr)
		base.DeconvAuto, base.DeconvSigma = false, sigma
		base.Sharpen.DeconvSigma = sigma
		t.Logf("pinned deconv sigma to %.3f px", sigma)
	}

	logOffLimb(t, mono, g.Sun)

	// The knobs, exactly as `params` would carry them. Defaults are small 1.15, medium 1.35,
	// large 1.10, denoise 0.5, iters 50, limbFlatten 0.85, contrast 1.0.
	tests := []struct {
		name                          string
		small, medium, large, denoise float64
		iters                         int
		limbFlatten, contrast         float64
		stretch                       float64
		palette                       string
		promBoost, glow               float64
	}{
		{name: "40_final_gold", promBoost: 0.20, glow: 0.5, palette: PaletteGold},
		{name: "41_final_mono", promBoost: 0.20, glow: 0.5, palette: PaletteMono},
		{name: "42_final_inverted", promBoost: 0.20, glow: 0.5, palette: PaletteInverted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A zero field means "leave the natural recipe alone", so a variant names only what it
			// varies and the table stays readable as the one thing each row is testing.
			fin := naturalFinish(base)
			if tt.small > 0 || tt.medium > 0 || tt.large > 0 {
				fin.Sharpen.Gains = sweepGains(tt.small, tt.medium, tt.large)
			}
			if tt.denoise > 0 {
				fin.Sharpen.Thresholds = sweepThresholds(tt.denoise)
			}
			if tt.iters > 0 {
				fin.DeconvIters, fin.Sharpen.DeconvIters = tt.iters, tt.iters
			}
			if tt.limbFlatten > 0 {
				fin.LimbFlatten = tt.limbFlatten
			}
			if tt.contrast > 0 {
				fin.Contrast = tt.contrast
			}
			if tt.stretch > 0 {
				fin.Stretch = tt.stretch
			}
			if tt.palette != "" {
				fin.Palette = tt.palette
			}
			if tt.promBoost > 0 {
				fin.ProminenceBoost = tt.promBoost
			}
			fin.GlowStrength = tt.glow

			dst := filepath.Join(outDir, tt.name+".png")
			require.NoError(t, WritePNG(FinishPair(mono, g, fin), dst))
			t.Log("wrote", dst, fmt.Sprintf("gains=%.2v", fin.Sharpen.Gains))
		})
	}
}

// sweepGains mirrors applySunParamPatch's mapping from the three sharpen knobs onto the five
// starlet gains, so a winning variant is reproducible as a `params` block and not only here.
func sweepGains(small, medium, large float64) []float64 {
	return []float64{small * 0.8 / 1.15, small, medium, medium * 1.25 / 1.35, large}
}

// sweepThresholds mirrors the sharpen_denoise mapping.
func sweepThresholds(denoise float64) []float64 {
	k := denoise * 2
	return []float64{4 * k, 2 * k, 1 * k, 0, 0}
}

// logOffLimb reports what blendProminences actually sees: the off-limb signal as a fraction of the
// disc, after the same radial background model the finish subtracts. It exists because a prominence
// that renders as a flat cut-out has two very different possible causes — a tone curve that clips
// it, or an input fraction already near 1 — and the knob only helps in the first case.
func logOffLimb(t *testing.T, im *fits.Image, l Limb) {
	t.Helper()
	p, w, h := im.Pix[0], im.W, im.H
	halo := offLimbProfile(p, w, h, l, nil)
	sky := offLimbLevel(p, w, h, l, nil)
	ref := float64(imgops.Percentile(imgops.Subsample(onDiscSamples(p, w, h, l, 0.5, nil), 100000), 50))
	t.Logf("off-limb: disc ref %.5g, sky %.5g, ref-sky %.5g", ref, sky, ref-sky)
	if ref-sky <= 0 {
		return
	}
	// The residual above the modelled background, in bands of radius, as a fraction of the disc.
	for _, band := range [][2]float64{{1.00, 1.02}, {1.02, 1.05}, {1.05, 1.10}, {1.10, 1.20}} {
		var fr []float64
		for y := 0; y < h; y++ {
			dy := float64(y) - l.CY
			for x := 0; x < w; x++ {
				dx := float64(x) - l.CX
				d := math.Hypot(dx, dy) / l.R
				if d < band[0] || d >= band[1] {
					continue
				}
				fr = append(fr, (float64(p[y*w+x])-halo.at(d, math.Atan2(dy, dx)))/(ref-sky))
			}
		}
		if len(fr) == 0 {
			continue
		}
		sort.Float64s(fr)
		q := func(f float64) float64 { return fr[clampInt(int(f*float64(len(fr))), 0, len(fr)-1)] }
		t.Logf("  %.2f-%.2f R  n=%-8d p50=%+.4f p99=%+.4f p999=%+.4f max=%+.4f",
			band[0], band[1], len(fr), q(0.50), q(0.99), q(0.999), fr[len(fr)-1])
	}
}

// naturalFinish is the recipe chosen on 2026-08-14 against the Canon Hα stacks: the finest starlet
// scale carries the detail and the mid scales are left flat, which is what separates thin structure
// from the embossed "orange peel" the defaults produce on a deep stack.
func naturalFinish(base FinishOptions) FinishOptions {
	f := base
	f.Sharpen.Gains = sweepGains(1.55, 1.00, 1.00)
	f.Sharpen.Thresholds = sweepThresholds(0.5)
	f.Stretch = 0.40
	f.Palette = PaletteGold
	return f
}
