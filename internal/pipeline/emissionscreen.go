package pipeline

import (
	"github.com/verove-jordan/astronomy/internal/gimp"
	"github.com/verove-jordan/astronomy/internal/mode"
)

// The emission screens: Hα, [OIII] and [SII] composited as additive tinted layers over the broadband
// base, so narrowband data taken alongside LRGB lights up the image instead of sitting unused.
//
// All three run the same machine — continuum-subtract, RBF-flatten, autostretch dark, wash-gate,
// Screen in GIMP — and differ only in which filter feeds them, which broadband channels estimate
// their continuum, and what colour they end up. They used to be copy-pasted blocks in prepGimpInputs,
// and drifted: Hα gained continuum subtraction and a wash gate that [OIII] only received later, and
// [SII] had none of it at all. This table is what keeps them in step.
type emissionScreen struct {
	filter   string // channel that feeds the layer
	base     string // stretched-TIFF base name in the stretch dir
	statBase string // measurement-FITS base name the wash gate reads
	tint     string // how the screen reads, for the run note ("red", "teal", …)
	enabled  bool   // the palette says screen it, and the preset opacity is > 0

	dest         *string // where the stretched TIFF path lands in gimp.Inputs
	continuumSub func(workDir string, channels map[string]string, writeDir string) (*haContinuum, string)
	// applyGate stores the wash-gate factor — and the final opacity and black point — into
	// gimp.Inputs. nil for Hα, whose opacity is still a positional BuildImage argument, so the caller
	// applies HaScreenFactor itself via Inputs.HaOpacity.
	applyGate func(factor float64)
}

// emissionScreens returns the three screens in composite order, each wired to write into `in` and
// flagged with whether this run should actually apply it.
//
// Hα screens whenever it was captured (it has been the default look for years). [OIII] and [SII] are
// OPT-IN: their opacity knobs default to 0, so a run that does not ask for them emits byte-identical
// script to before those knobs existed.
func emissionScreens(pal paletteResolved, p *mode.Preset, in *gimp.Inputs) []emissionScreen {
	return []emissionScreen{{
		filter:       "Ha",
		base:         "ha",
		statBase:     haStatBase,
		tint:         "red",
		enabled:      pal.HaScreen,
		dest:         &in.Ha,
		continuumSub: haContinuumSubtract,
		applyGate:    func(f float64) { in.HaScreenFactor = f },
	}, {
		filter:       "OIII",
		base:         "oiii",
		statBase:     oiiiStatBase,
		tint:         "teal",
		enabled:      pal.OIIIScreen && p != nil && p.OIIIScreen > 0,
		dest:         &in.OIII,
		continuumSub: oiiiContinuumSubtract,
		applyGate: func(f float64) {
			in.OIIIScreenFactor = f
			in.OIIIScreen = in.OIIIOpacity(p.OIIIScreen)
			in.OIIIBlack = p.OIIIBlackPoint
		},
	}, {
		filter:       "SII",
		base:         "sii",
		statBase:     siiStatBase,
		tint:         siiTintName(p),
		enabled:      pal.SIIScreen && p != nil && p.SIIScreen > 0,
		dest:         &in.SII,
		continuumSub: siiContinuumSubtract,
		applyGate: func(f float64) {
			in.SIIScreenFactor = f
			in.SIIScreen = in.SIIOpacity(p.SIIScreen)
			in.SIIBlack = p.SIIBlackPoint
			in.SIITint = p.SIITint
		},
	}}
}

// siiTintName describes the [SII] screen's colour for the run notes.
func siiTintName(p *mode.Preset) string {
	if p != nil && p.SIITint == mode.SIITintGold {
		return "gold"
	}
	return "deep-red"
}
