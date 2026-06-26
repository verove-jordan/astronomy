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
// binary is reachable. When true, the linear masters are gradient-removed by GraXpert and Siril runs
// only a gentle degree-1 subsky cleanup at finish (see backgroundDegree).
func aiBackground(ctx context.Context, opts Options) bool {
	return opts.Graxpert != nil && opts.Preset != nil && opts.Preset.BackgroundAI &&
		opts.Graxpert.Available(ctx) == nil
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
			onProgress(siril.Progress{Line: p.Line, Percent: p.Percent})
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
			onProgress(siril.Progress{Line: p.Line, Percent: p.Percent})
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
