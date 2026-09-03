package pipeline

// Full-resolution export of a finished run's intermediate stages.
//
// The timeline previews under previews/ are half-scale (previewDownscale) 8-bit PNGs — right for a
// strip in the browser, useless for anything else. This renders the underlying data at NATIVE
// resolution, as PNG or TIFF, so any stage can be inspected, printed or taken into another editor,
// not just the final image.
//
// It offers only what the run actually PRESERVED, and that restriction is the whole design. Most of
// the linear intermediates are processed IN PLACE: rgb_base.fits is background-extracted, denoised,
// chroma-smoothed and colour-calibrated on top of itself. So "re-render the combined stage on
// demand" would hand back colour-calibrated pixels under the combined stage's label — the export
// would look plausible and be wrong. Every entry below names a file that still holds what its label
// claims; a stage whose source was overwritten is simply not offered rather than approximated.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// exportDirName is where rendered exports land, beside the run's own artifacts.
const exportDirName = "export"

// StageArtifact is one exportable stage of a finished run.
type StageArtifact struct {
	Key    string `json:"key"`              // stable identifier used to request the export
	Label  string `json:"label"`            // human-readable stage name
	Path   string `json:"path"`             // absolute path of the SOURCE that will be rendered
	Linear bool   `json:"linear"`           // linear data: needs an autostretch to be viewable
	Filter string `json:"filter,omitempty"` // channel tag for per-channel stages (L/R/G/B/Ha/RGB)
	Order  int    `json:"order"`            // pipeline order, for display
}

// stageSpec describes one candidate artifact by filename, in pipeline order.
type stageSpec struct {
	file   string
	key    string
	label  string
	linear bool
	order  int
}

// stageSpecs are the fixed (non per-channel) artifacts, in pipeline order. Their names are the ones
// the pipeline itself writes; see enhance.go (the linear/ caches), pipeline.go (rgb_base*) and the
// finish. A missing file just means that stage did not run for this mode — it is skipped.
var stageSpecs = []stageSpec{
	{"linear/rgb_base_bg.fits", "background", "Combined, background extracted", true, 310},
	{"linear/rgb_base_denoised.fits", "denoised", "Combined, AI colour denoised", true, 320},
	{"linear/rgb_base_spcc.fits", "colorcal", "Colour calibrated (linear)", true, 400},
	{"rgb_base.fits", "linear_final", "Linear composite (final state)", true, 450},
	{"rgb_base_stretch.fits", "stretched", "Stretched, before the composite", false, 500},
	{"final.tif", "final", "Final image", false, 900},
}

// StageArtifacts lists the full-resolution stages a finished run can export, in pipeline order.
// Per-channel stacked and trimmed masters are discovered by globbing, so a mono LRGB run offers one
// entry per filter and a one-shot-colour run offers a single RGB one.
func StageArtifacts(outDir string) []StageArtifact {
	var out []StageArtifact
	add := func(path, key, label string, linear bool, order int, filter string) {
		if st, err := os.Stat(path); err != nil || st.IsDir() || st.Size() == 0 {
			return
		}
		out = append(out, StageArtifact{Key: key, Label: label, Path: path, Linear: linear, Filter: filter, Order: order})
	}
	for _, g := range []struct {
		glob, prefix, key, label string
		order                    int
	}{
		{"master_*.fits", "master_", "stacked", "Stacked master", 100},
		{"trim_*.fits", "trim_", "trimmed", "Trimmed to the common field", 200},
	} {
		paths, _ := filepath.Glob(filepath.Join(outDir, g.glob))
		sort.Strings(paths)
		for _, p := range paths {
			tag := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), g.prefix), ".fits")
			if tag == "" || strings.Contains(tag, "_") { // master_RGB_noise.png etc. never match .fits, but be strict
				continue
			}
			add(p, g.key+"_"+tag, g.label+" ("+tag+")", true, g.order, tag)
		}
	}
	for _, s := range stageSpecs {
		add(filepath.Join(outDir, filepath.FromSlash(s.file)), s.key, s.label, s.linear, s.order, "")
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out
}

// ExportStage renders one stage at full resolution into <outDir>/export/ and returns the written
// file. format is "png" or "tif". Linear sources are autostretched exactly as the timeline preview
// is — the same `autostretch` Siril applies at half scale — so the export matches what the strip
// showed, only at native resolution; already-stretched sources are converted as they are.
func ExportStage(ctx context.Context, runner *siril.Runner, outDir, key, format string) (string, error) {
	// Arguments are validated before the runner: a bad format or an unknown stage is the caller's
	// error and must say so, not be masked by "no siril runner configured".
	switch format {
	case "png", "tif", "tiff":
	default:
		return "", fmt.Errorf("stage export: unsupported format %q (want png or tif)", format)
	}
	var art *StageArtifact
	for _, a := range StageArtifacts(outDir) {
		if a.Key == key {
			art = &a
			break
		}
	}
	if art == nil {
		return "", fmt.Errorf("stage export: no exportable stage %q in this run", key)
	}
	if runner == nil {
		return "", fmt.Errorf("stage export: no siril runner configured")
	}
	dir := filepath.Join(outDir, exportDirName)
	if err := fsutil.EnsureDir(dir); err != nil {
		return "", err
	}
	base := filepath.Join(dir, key)
	if _, err := runner.Run(ctx, outDir, stageExportScript(art.Path, base, art.Linear, format), nil); err != nil {
		return "", fmt.Errorf("stage export %s: %w", key, err)
	}
	written := base + "." + exportExt(format)
	if _, err := os.Stat(written); err != nil {
		return "", fmt.Errorf("stage export %s: siril wrote no %s", key, format)
	}
	return written, nil
}

// stageExportScript loads src (absolute), optionally autostretches it, and saves it at FULL
// resolution — no `resample`, which is the only difference from siril.PreviewScript.
func stageExportScript(src, outBase string, linear bool, format string) string {
	var b strings.Builder
	b.WriteString("requires 1.2.0\nsetext fits\n")
	fmt.Fprintf(&b, "load %s\n", src)
	if linear {
		b.WriteString("autostretch\n")
	}
	b.WriteString(siril.SaveAsCmd(format, outBase) + "\n")
	return b.String()
}

// exportExt is the extension Siril's savepng/savetif actually append.
func exportExt(format string) string {
	if format == "png" {
		return "png"
	}
	return "tif"
}
