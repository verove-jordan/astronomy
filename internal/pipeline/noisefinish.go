package pipeline

import (
	"context"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/noise"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// denoiseLinearMaster measures a stacked linear master's sky noise, denoises it (the Go starlet
// denoiser when DenoiseStarlet is set, else Siril's `denoise`) at a strength scaled by the measured
// noise + stacked-frame depth (DenoiseAuto), and records the before/after sigma on the channel.
// Everything is soft-fail: on any error the master is left as produced and a note is recorded.
func denoiseLinearMaster(ctx context.Context, opts Options, ch *ChannelResult, masterName, outDir, filter string,
	onProgress func(siril.Progress)) {
	if opts.Preset == nil {
		return
	}
	masterPath := filepath.Join(outDir, masterName+".fits")

	before, measured := measureNoise(masterPath, ch)
	base := denoiseFor(filter, opts.Preset)
	effMod := base.Modulation
	if opts.Preset.DenoiseAuto && measured {
		effMod = clampFloat(base.Modulation*noise.AdaptiveFactor(before.Sigma, ch.StackedFrames), 0, 1.2)
	}
	if effMod <= 0 {
		return
	}

	if note := applyDenoise(ctx, opts, masterName, outDir, base, effMod, onProgress); note != "" {
		ch.Selection.Notes = append(ch.Selection.Notes, note)
	}
	if measured && ch.Noise != nil {
		if im, err := fits.ReadImage(masterPath); err == nil {
			ch.Noise.SigmaAfter = noise.Measure(im).Sigma
		}
		if opts.Preset.Previews {
			_ = noise.WriteHeatmapPNG(before, filepath.Join(outDir, masterName+"_noise.png"))
		}
	}
}

// measureNoise reads the master and records its pre-denoise noise/SNR/background on the channel.
// The bool is false (and ch.Noise left nil) when the file cannot be read.
func measureNoise(masterPath string, ch *ChannelResult) (noise.Report, bool) {
	im, err := fits.ReadImage(masterPath)
	if err != nil {
		return noise.Report{}, false
	}
	rep := noise.Measure(im)
	ch.Noise = &noise.Summary{SigmaBefore: rep.Sigma, SNR: rep.SNR, Background: rep.Background}
	return rep, true
}

// applyDenoise runs the chosen denoise engine at the effective strength, returning a note on failure.
func applyDenoise(ctx context.Context, opts Options, masterName, outDir string, base siril.DenoiseOptions,
	effMod float64, onProgress func(siril.Progress)) string {
	if opts.Preset.DenoiseStarlet {
		return starletDenoiseFile(filepath.Join(outDir, masterName+".fits"), effMod)
	}
	base.Modulation = clampFloat(effMod, 0, 0.95) // Siril needs modulation < 1 to blend
	if _, err := opts.Runner.Run(ctx, outDir, siril.DenoiseScript(masterName+".fits", masterName, base), onProgress); err != nil {
		return "denoise skipped: " + err.Error()
	}
	return ""
}

// starletDenoiseFile denoises a linear master in place with the Go à-trous denoiser, preserving the
// FITS header (OverwriteData rewrites only the pixel data). Soft-fail: returns a note on any error.
func starletDenoiseFile(path string, strength float64) string {
	im, err := fits.ReadImage(path)
	if err != nil {
		return "starlet denoise skipped (read): " + err.Error()
	}
	o := noise.DefaultOptions()
	o.Strength = strength
	noise.Denoise(im, o)
	if err := im.OverwriteData(path); err != nil {
		return "starlet denoise skipped (write): " + err.Error()
	}
	return ""
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
