// Per-stage processing previews: after a major milestone (stacked, aligned, combined, colour-calibrated,
// star-reduced, final) the pipeline saves a small autostretched PNG under <runDir>/previews/ and streams
// it, so the UI builds a labeled timeline instead of only showing the latest live preview. Linear masters
// are rendered with siril.PreviewScript; already-stretched PNGs are copied. Everything is gated on
// Preset.Previews and soft-fails — a preview never fails a run. Persistence is by globbing previews/ at
// the end of each mode's Process (collectStagePreviews), so the timeline survives a reload.
package pipeline

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// Stage keys — the UI maps each to a localized label. Per-channel stages also carry a filter (L/R/G/B/Ha).
const (
	stageStacked  = "stacked"
	stageAligned  = "aligned"
	stageCombined = "combined"
	stageColorCal = "colorcal"
	stageDeconv   = "deconv"
	stageStarless = "starless"
	stageFinal    = "final"
)

// Timeline ordinals: the filename's leading number sorts the strip left→right. Per-channel stacked frames
// add a small per-channel offset (ordStacked + channel index) so L/R/G/B/Ha sort stably even when the
// channels are stacked in parallel.
const (
	ordStacked  = 100
	ordAligned  = 200
	ordCombined = 300
	ordColorCal = 400
	ordDeconv   = 500
	ordFinal    = 900
	ordStarless = 950 // the star-reduced variant is produced after the with-stars final
)

// previewDownscale is the resample factor for a rendered stage preview (half-size, autostretched).
const previewDownscale = 0.5

// capturePreview saves one milestone preview into <outDir>/previews/ and streams it live. src is an
// absolute path: a linear FITS (rendered via load→resample→autostretch→savepng) when linear is true, else
// an already-stretched PNG (copied as-is). Gated on Preset.Previews; soft-fail (logs and returns so a
// preview never fails the run).
func capturePreview(ctx context.Context, opts Options, outDir string, index int, stage, filter, src string, linear bool) {
	if opts.Preset == nil || !opts.Preset.Previews || opts.Runner == nil {
		return
	}
	previewsDir := filepath.Join(outDir, "previews")
	if err := fsutil.EnsureDir(previewsDir); err != nil {
		log.Printf("stage preview %s: ensure previews dir: %v", stage, err)
		return
	}
	tag := ""
	if filter != "" {
		tag = filterTag(filter)
	}
	name := fmt.Sprintf("%03d_%s", index, stage)
	if tag != "" {
		name += "_" + tag
	}
	outBase := filepath.Join(previewsDir, name) // absolute, no extension (savepng appends .png)
	dest := outBase + ".png"

	if linear {
		if _, err := opts.Runner.Run(ctx, outDir, siril.PreviewScript(src, outBase, previewDownscale), nil); err != nil {
			log.Printf("stage preview %s: render: %v", stage, err)
			return
		}
	} else if err := fsutil.CopyFile(src, dest); err != nil {
		log.Printf("stage preview %s: copy: %v", stage, err)
		return
	}

	opts.report(Progress{
		Step:         stage,
		StagePreview: &postprocess.StagePreview{Index: index, Stage: stage, Filter: tag, PngPath: dest},
	})
}

// captureFinalPNG saves the "final" milestone preview from a finish result's first PNG output (copied,
// already stretched). No-op when there is no result or no PNG.
func captureFinalPNG(ctx context.Context, opts Options, outDir string, final *postprocess.Result) {
	if final == nil {
		return
	}
	for _, o := range final.Outputs {
		if strings.HasSuffix(o, ".png") {
			capturePreview(ctx, opts, outDir, ordFinal, stageFinal, "", o, false)
			return
		}
	}
}

// captureStackedMasters saves a "stacked" milestone preview for each channel master in the map (filter →
// master base path, no extension: a basename under outDir or an absolute path), ordered by filter so the
// timeline is stable. The masters are linear FITS, so each is autostretched.
func captureStackedMasters(ctx context.Context, opts Options, outDir string, masters map[string]string) {
	filters := make([]string, 0, len(masters))
	for f := range masters {
		filters = append(filters, f)
	}
	sort.Strings(filters)
	for i, f := range filters {
		src := masters[f]
		if !filepath.IsAbs(src) {
			src = filepath.Join(outDir, src)
		}
		capturePreview(ctx, opts, outDir, ordStacked+i, stageStacked, f, src+".fits", true)
	}
}

// collectStagePreviews reconstructs the saved milestone previews from <outDir>/previews/ (filenames
// <NN>_<stage>[_<filter>].png), sorted by NN, so a finished run persists its timeline for reload.
func collectStagePreviews(outDir string) []postprocess.StagePreview {
	paths, _ := filepath.Glob(filepath.Join(outDir, "previews", "*.png"))
	out := make([]postprocess.StagePreview, 0, len(paths))
	for _, p := range paths {
		if sp, ok := parseStagePreview(p); ok {
			out = append(out, sp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

// parseStagePreview reads a preview filename <NN>_<stage>[_<filter>].png back into a StagePreview.
func parseStagePreview(path string) (postprocess.StagePreview, bool) {
	base := strings.TrimSuffix(filepath.Base(path), ".png")
	parts := strings.SplitN(base, "_", 3) // NN | stage | optional filter
	if len(parts) < 2 {
		return postprocess.StagePreview{}, false
	}
	idx, err := strconv.Atoi(parts[0])
	if err != nil {
		return postprocess.StagePreview{}, false
	}
	sp := postprocess.StagePreview{Index: idx, Stage: parts[1], PngPath: path}
	if len(parts) == 3 {
		sp.Filter = parts[2]
	}
	return sp, true
}
