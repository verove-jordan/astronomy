// AI enhancement steps that augment the Siril/GIMP pipeline when the optional host tools are
// installed: GraXpert background-gradient extraction on the linear masters (ahead of a gentle Siril
// subsky cleanup), and StarNet++ star removal in the finish (see finishWithGimp). Every step is
// soft-fail — a missing or erroring tool leaves the Siril/GIMP result untouched.
package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/gimp"
	"github.com/verove-jordan/astronomy/internal/graxpert"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/starnet"
)

// aiBackground reports whether GraXpert background extraction is enabled by the preset and the
// tool can actually run (deep health probe, not a mere binary lookup — a present-but-broken
// GraXpert must NOT capture the gradient-removal path). When true, the linear masters are
// gradient-removed by GraXpert and Siril runs only a gentle degree-1 subsky cleanup at finish
// (see backgroundDegree).
func aiBackground(ctx context.Context, opts Options) bool {
	return opts.Graxpert != nil && opts.Preset != nil && opts.Preset.BackgroundAI &&
		opts.Graxpert.Healthy(ctx) == nil
}

// aiToolWarnings reports preset-enabled AI steps whose host binary is unreachable. Both steps
// soft-fall-back (GraXpert→Siril subsky, StarNet→keep stars), but those fallbacks are exactly what
// produces the "the AI isn't doing anything" symptom — an uncorrected gradient/brown sky and full
// stars. Emitting a warning makes the skip visible in the run record instead of a silent no-op.
func aiToolWarnings(ctx context.Context, opts Options) []string {
	if opts.Preset == nil {
		return nil
	}
	var w []string
	if opts.Preset.BackgroundAI {
		switch {
		case opts.Graxpert == nil:
			w = append(w, "GraXpert background extraction is enabled for this mode but disabled for this run (--no-ai); using Siril subsky — expect residual gradients")
		default:
			if err := opts.Graxpert.Healthy(ctx); err != nil {
				w = append(w, "GraXpert background extraction enabled but not working ("+err.Error()+"); using Siril subsky/RBF — expect residual gradients")
			}
		}
	}
	if opts.Preset.StarReduce > 0 {
		switch {
		case opts.Starnet == nil:
			w = append(w, "StarNet++ star reduction is enabled for this mode but disabled for this run (--no-ai); keeping full stars")
		default:
			if err := opts.Starnet.Available(ctx); err != nil {
				w = append(w, "StarNet++ star reduction enabled but unavailable ("+err.Error()+"); keeping full stars")
			}
		}
	}
	return w
}

// backgroundDegree is the Siril subsky polynomial degree for the finish stage, always in Siril's
// valid [1,4] range (Siril rejects 0). When GraXpert already extracted the background it returns 1 —
// a gentle linear cleanup after the AI extraction, and a safety net if GraXpert soft-failed;
// otherwise it returns the preset degree clamped to [1,4].
func backgroundDegree(ctx context.Context, opts Options) int {
	if aiBackground(ctx, opts) {
		return 1
	}
	deg := 1
	if opts.Preset != nil && opts.Preset.BackgroundDegree > 0 {
		deg = opts.Preset.BackgroundDegree
	}
	if deg > 4 {
		deg = 4
	}
	return deg
}

// extractBackgroundAI runs GraXpert background extraction in place on a linear FITS master. It is
// soft-fail by contract: any problem returns a human-readable note (never an error) so a missing or
// erroring GraXpert leaves the original master untouched and the pipeline continues with Siril.
func extractBackgroundAI(ctx context.Context, opts Options, masterPath string, onProgress func(siril.Progress)) (note string) {
	out := strings.TrimSuffix(masterPath, ".fits") + "_graxpert.fits"
	fwd := func(p graxpert.Progress) {
		if onProgress != nil {
			onProgress(siril.Progress{Line: p.Line, Percent: p.Percent, Sample: p.Sample})
		}
	}
	if err := opts.Graxpert.ExtractBackground(ctx, masterPath, out, graxpert.BackgroundOptions{}, fwd); err != nil {
		return "GraXpert background extraction skipped: " + err.Error()
	}
	if !fileExists(out) {
		return "GraXpert background extraction skipped: no output produced"
	}
	if err := os.Rename(out, masterPath); err != nil {
		return "GraXpert background extraction skipped: " + err.Error()
	}
	return ""
}

// extractCombinedBackground runs a SECOND background-extraction pass on the combined linear RGB
// (outDir/<base>.fits) to remove the residual large-scale colour gradient (amp-glow + light pollution)
// that survives per-channel extraction + the combine — this is what makes the whole sky homogeneous.
// GraXpert when available, else a deterministic RBF subsky (far better than a polynomial for an
// asymmetric gradient). Soft-fail: returns a human-readable note, never an error; a no-op when the
// preset disables it (CombinedBackgroundAI false).
func extractCombinedBackground(ctx context.Context, opts Options, runner *siril.Runner, outDir, base, hdr string) (note string) {
	if opts.Preset == nil || !opts.Preset.CombinedBackgroundAI {
		return ""
	}
	rbf := func() (string, bool) { // RBF subsky flattens the asymmetric amp-glow/light-pollution residual
		if _, err := runner.Run(ctx, outDir, hdr+"load "+base+"\n"+siril.SubskyRBFCmd()+"save "+base+"\n", nil); err != nil {
			return "combined RBF subsky skipped: " + err.Error(), false
		}
		return "", true
	}
	// Every branch reports what actually flattened the combined RGB, so run.json always records whether
	// the gradient removal ran (a silent "" made a left-behind gradient indistinguishable from a pass
	// that worked).
	if opts.Graxpert != nil && opts.Graxpert.Healthy(ctx) == nil {
		if n := extractBackgroundAI(ctx, opts, filepath.Join(outDir, base+".fits"), nil); n != "" {
			// The AI pass failed at runtime — the RBF pass below is now the ONLY gradient removal,
			// so it must still run (returning early here shipped un-flattened, blotchy skies).
			if rn, ok := rbf(); !ok {
				return "combined " + n + "; " + rn
			}
			return "combined " + n + " — RBF subsky fallback applied"
		}
		if rn, ok := rbf(); !ok { // GraXpert removes most; the follow-up RBF cleans the residual it leaves
			return "combined background: GraXpert applied; " + rn
		}
		return "combined background extracted (GraXpert + RBF residual pass)"
	}
	if rn, ok := rbf(); !ok { // GraXpert absent/broken → RBF alone (deterministic, better than a polynomial here)
		return rn
	}
	return "combined background flattened (RBF subsky; GraXpert unavailable)"
}

// denoiseAI runs GraXpert AI denoising in place on a linear FITS (the combined RGB colour base). It is
// an edge-preserving learned denoiser, so it cuts the heavy chrominance noise of thin colour subs
// WITHOUT smearing star colour halos (unlike a gaussian blur). Soft-fail by contract: returns a
// human-readable note, never an error, so a missing/erroring GraXpert leaves the input untouched.
func denoiseAI(ctx context.Context, opts Options, path string, onProgress func(siril.Progress)) (note string) {
	out := strings.TrimSuffix(path, ".fits") + "_graxpert.fits"
	fwd := func(p graxpert.Progress) {
		if onProgress != nil {
			onProgress(siril.Progress{Line: p.Line, Percent: p.Percent, Sample: p.Sample})
		}
	}
	if err := opts.Graxpert.Denoise(ctx, path, out, graxpert.DenoiseOptions{}, fwd); err != nil {
		return "GraXpert denoise skipped: " + err.Error()
	}
	if !fileExists(out) {
		return "GraXpert denoise skipped: no output produced"
	}
	if err := os.Rename(out, path); err != nil {
		return "GraXpert denoise skipped: " + err.Error()
	}
	// Success is reported too — a silent "" made "denoise never ran" indistinguishable from "denoise
	// ran fine" when chasing chroma-noise blotches in the final.
	return "denoise applied (GraXpert AI)"
}

// aiStars reports whether StarNet++ star reduction is enabled by the preset (StarReduce > 0) and
// the binary is reachable.
func aiStars(ctx context.Context, opts Options) bool {
	return opts.Starnet != nil && opts.Gimp != nil && opts.Preset != nil && opts.Preset.StarReduce > 0 &&
		opts.Starnet.Available(ctx) == nil
}

// reduceStarsAI runs StarNet++ on the flattened composite TIFF, then blends the stars back at the
// preset opacity to produce a star-reduced .tif/.png (plus the starless TIFF as a bonus artifact).
// Soft-fail by contract: it returns extra output paths and a note rather than an error, so the
// with-stars final produced by GIMP is always kept even when StarNet or the blend fails.
func reduceStarsAI(ctx context.Context, opts Options, withStarsTif, outDir string, onProgress func(siril.Progress)) (outputs []string, note string) {
	starless := filepath.Join(outDir, "final_starless.tif")
	fwd := func(p starnet.Progress) {
		if onProgress != nil {
			onProgress(siril.Progress{Line: p.Line, Percent: p.Percent, Sample: p.Sample})
		}
	}
	if err := opts.Starnet.RemoveStars(ctx, withStarsTif, starless, starnet.Options{}, fwd); err != nil {
		return nil, "StarNet++ star removal skipped: " + err.Error()
	}
	if !fileExists(starless) {
		return nil, "StarNet++ star removal skipped: no output produced"
	}
	red, err := gimp.ReduceStars(opts.Gimp, withStarsTif, starless, opts.Preset.StarReduce, filepath.Join(outDir, "final_reduced"))
	if err != nil {
		return []string{starless}, "star reduction blend failed (keeping starless): " + err.Error()
	}
	return []string{starless, red.Tif, red.Png}, ""
}
