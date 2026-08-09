// color.go holds the one-shot-color seams of the deep-sky pipeline.
//
// The design decision worth stating once: a colour run is NOT a second pipeline. It is the ordinary
// per-channel pipeline with exactly one channel, named RGB (inspect.nameColorChannel stamps it), so
// calibration against the master library, frame grading, trail masking, set QA, dither diagnostics,
// plate-solving, SPCC, GraXpert, StarNet, denoise, star-quality auto-fix, the supervised finish,
// stage previews, per-stage rerun and the S3 paths all apply to colour without being reimplemented.
// Only three things genuinely differ, and each is one small seam here:
//
//  1. INGEST — a raw CFA mosaic must reach Siril still undebayered, so calibration can divide the
//     flat and repair defects in CFA space and demosaic afterwards (colorIngest / CalibMasters.CFA).
//     Debayering first smears every hot pixel across its neighbours.
//  2. COMBINE — there is nothing to combine. The stacked master is already RGB, so the LRGB
//     rgbcomp step is skipped and the master goes straight to the finish (isColorRun).
//  3. PALETTE — the palettes map FILTERS onto output channels, which is meaningless here, so a
//     colour run resolves to a fixed pass-through palette with no emission screens (colorPalette).
package pipeline

import (
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/filters"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// markColorPreset records on the preset that this run is one-shot color. mode.Preset.Color already
// existed as documentation of each mode's expected sensor; this makes it the live switch, decided
// from the inventory rather than from the mode the user picked, so a colour folder submitted as
// "deepsky" is processed as colour instead of losing every frame.
func markColorPreset(p *mode.Preset) {
	if p != nil {
		p.Color = mode.OSC
	}
}

// isColorRun reports whether the run stacks one-shot-color frames.
func isColorRun(p *mode.Preset) bool { return p != nil && p.Color == mode.OSC }

// isColorChannel reports whether a channel tag is the single one-shot-color channel.
func isColorChannel(tag string) bool { return filters.IsColor(tag) }

// needsDebayer reports whether a set of light frames is still a raw CFA mosaic, so calibration has
// to run CFA-aware and demosaic at the end. An already-debayered source (a developed camera raw, a
// colour TIFF, an RGB FITS) must NOT take that path: Siril would try to demosaic three planes that
// have already been demosaiced. A group is CFA only if EVERY frame is — a mixed group is safer
// treated as already-RGB, since debayering an RGB frame corrupts it while skipping the debayer of a
// CFA frame merely leaves it looking like a checkerboard, which is visible rather than subtle.
func needsDebayer(frames []*inspect.Frame) bool {
	if len(frames) == 0 {
		return false
	}
	for _, fr := range frames {
		if !fr.NeedsDebayer() {
			return false
		}
	}
	return true
}

// fitsExts are the extensions Siril's `link` can symlink into a sequence without decoding.
var fitsExts = map[string]bool{".fits": true, ".fit": true, ".fts": true}

// seqIngest picks how a set of light frames is brought into a Siril sequence. FITS is linked, which
// is what every mono run has always done and costs nothing; anything else — a Nikon NEF, a colour
// TIFF, a JPEG — has to be DECODED by `convert` first, because `link` only symlinks and Siril would
// then find no sequence at all. This is what lets a DSLR folder travel the ordinary deep-sky path
// instead of needing a separate raw pipeline.
func seqIngest(frames []*inspect.Frame) siril.SeqIngest {
	for _, fr := range frames {
		if !fitsExts[strings.ToLower(filepath.Ext(fr.Path))] {
			return siril.IngestConvert
		}
	}
	return siril.IngestLink
}

// oscSource returns the single one-shot-color channel from a resolved channel map. It tolerates a
// map keyed by any of the colour aliases so a channel named by an older run (or by a hand-edited
// rerun manifest) still resolves; with exactly one entry, that entry is the answer regardless.
func oscSource(channels map[string]string) string {
	for tag, src := range channels {
		if isColorChannel(tag) {
			return src
		}
	}
	for _, src := range channels { // single-channel map with an unexpected tag
		return src
	}
	return ""
}

// colorPalette is the palette a one-shot-color run resolves to: the stacked RGB master passes
// through untouched, in colour, with no emission screens. It bypasses resolvePalette entirely —
// every entry in paletteSpecs is written in terms of filter names (Ha→R, OIII→G …), which a colour
// sensor does not have, and the fallback chain would land on "mono" and throw the colour away.
func colorPalette() paletteResolved {
	return paletteResolved{Name: "rgb", R: "R", G: "G", B: "B", Color: true}
}
