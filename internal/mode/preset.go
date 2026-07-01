// Package mode defines the capture modes (deepsky/nebula/milkyway/planetary) and the output
// format, and maps each mode to a Preset that retunes grading, background extraction, stretch,
// Ha blending, saturation and final curves across the whole pipeline.
package mode

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/verove-jordan/astronomy/internal/grade"
	"github.com/verove-jordan/astronomy/internal/planetary"
)

// BrightnessTarget maps the milkyway brightness control to an auto-levels target sky-background level
// (Siril-style, in (0,0.5)). It accepts the keywords darker/balanced/brighter or a raw 0..0.5 number;
// ok is false for an empty value (keep the preset default) or an unparseable/out-of-range one.
func BrightnessTarget(s string) (level float64, ok bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return 0, false
	case "darker", "dark":
		return 0.035, true
	case "balanced", "balance", "default":
		return 0.05, true
	case "brighter", "bright":
		return 0.07, true
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && v > 0 && v < 0.5 {
		return v, true
	}
	return 0, false
}

// Mode is a capture subject type; it drives the whole pipeline preset.
type Mode string

const (
	Deepsky   Mode = "deepsky"   // galaxies/clusters at native focal length (mono LRGB+Ha)
	Nebula    Mode = "nebula"    // large faint emission objects (mono LRGB+Ha, Ha-forward)
	Milkyway  Mode = "milkyway"  // wide-field one-shot-color (e.g. iPhone ProRAW/HEIC)
	Planetary Mode = "planetary" // Moon/planets via lucky imaging
	Livestack Mode = "livestack" // watch a source + incrementally stack during a session; finalize = deepsky
	Comet     Mode = "comet"     // moving comet: dual star/comet stack + star-layer recomposite
)

// Format is the desired output artifact.
type Format string

const (
	FormatImage Format = "image"
	FormatVideo Format = "video"
	FormatBoth  Format = "both"
)

// ColorModel is how the sensor records color.
type ColorModel string

const (
	Mono ColorModel = "mono" // per-filter monochrome frames → LRGB+Ha
	OSC  ColorModel = "osc"  // one-shot color (Bayer) → a single RGB sequence
)

// Preset bundles every tunable the pipeline reads, derived from a Mode.
type Preset struct {
	Mode             Mode
	Color            ColorModel
	Grade            grade.Options     // frame-quality rejection thresholds
	BackgroundDegree int               // Siril subsky polynomial degree (0 = skip)
	HaScreen         float64           // Ha layer opacity when screened into the composite (0 = none)
	Saturation       float64           // final saturation boost
	Curve            []float64         // gentle value curve (post-combine); flat x,y pairs in 0..1
	LumCurve         []float64         // galaxy-brightness curve applied to the L luminance layer (LRGB)
	Planetary        planetary.Options // lucky-imaging settings (planetary only)

	// CoreHighlightKnee / CoreHighlightCeil roll off the L luminance highlights (after LumCurve, before the
	// Ha screen) to tame a blown nebula core: identity below knee (outer nebula/stars/sky untouched),
	// asymptoting the bright centre to ceil < 1 so it dims to a structured knot the Ha screen tints pink.
	// Disabled unless 0 < knee < ceil < 1. See internal/gimp/compose.go.
	CoreHighlightKnee float64
	CoreHighlightCeil float64

	// Noise reduction (Siril `denoise` on the linear stacks). Chroma is denoised harder than
	// luminance to cut color noise while preserving detail; 0 skips a channel class.
	DenoiseChroma float64
	DenoiseLum    float64
	DenoiseVST    bool
	DenoiseDA3D   bool

	// DropFilterWheelTransition drops the first frame of a run when its brightness is off (the
	// wheel was still moving). Conditional — only off-brightness frames are dropped.
	DropFilterWheelTransition bool

	// Optional astro-AI host tools (used only when the binary is installed; otherwise skipped).
	// BackgroundAI runs GraXpert background extraction on the linear masters instead of Siril's
	// polynomial subsky. StarReduce > 0 runs StarNet++ in the finish and screens the stars back at
	// this opacity (0 = keep full stars, e.g. 0.5 = halved star intensity).
	BackgroundAI bool
	// CombinedBackgroundAI runs a second GraXpert background-extraction pass on the COMBINED linear RGB
	// (after per-channel extraction + rgbcomp, before SPCC) to remove the residual large-scale colour
	// gradient (amp-glow + light-pollution) that survives the combine. Falls back to an RBF subsky when
	// GraXpert is absent. This is what makes the whole sky homogeneous.
	CombinedBackgroundAI bool
	// ColorDenoiseAI runs a GraXpert AI *denoise* pass on the combined linear RGB (before SPCC) to cut
	// the heavy chrominance noise of thin colour subs. Being edge-preserving, it cleans the colour
	// without smearing star halos — unlike a gaussian ChromaBlur, which can then be dropped to 0.
	ColorDenoiseAI bool
	StarReduce     float64

	// ColorCalibration attempts plate-solve + SPCC for natural color + a neutral background,
	// falling back to background neutralization. LinkedStretch keeps that neutral balance.
	ColorCalibration bool
	LinkedStretch    bool

	// BackgroundLevel is the target sky-background brightness for the finishing autostretch
	// (Siril `autostretch [-linked] <shadowsclip> <targetbg>`), in (0,0.5]. Siril's bare-command
	// default of 0.25 lifts the sky to a bright grey that reads as a washed brown haze; deep-sky
	// finishing wants a dark sky (~0.06). 0 → engine default (0.06). See siril.AutostretchCmd.
	BackgroundLevel float64
	// HaBlackPoint clips the Ha layer's background to black before it is red-screened into the
	// composite (GIMP levels low-input, [0,1]). Without it the Ha background pedestal screens a red
	// wash over the whole frame (the brown sky). ~0.12 zeroes the background while keeping bright HII
	// knots. 0 → no clip (legacy behavior). Only meaningful when HaScreen > 0.
	HaBlackPoint float64

	// ChromaBlur denoises colour in the GIMP LRGB finish: it blurs the (thin, noisy) RGB base this
	// many px while the L luminance keeps all detail — erasing the "pink" chroma noise of short colour
	// subs with no loss of sharpness. 0 → none (mono/OSC, where there is no separate luminance).
	ChromaBlur float64
	// CropFrac trims this fraction off each edge of the exported image to drop ragged stacking-edge
	// bands (dithered frame borders); the layered .xcf keeps the full frame. 0 → no crop.
	CropFrac float64
	// TrailMaskK enables cross-frame transient masking before each channel is stacked: across the
	// REGISTERED subs, any pixel above its per-pixel median + k·MADσ (a satellite/plane trail segment,
	// cosmic ray or hot pixel — present in only one sub at that sky position) is replaced by the median.
	// It cleans a slow satellite that lands in many subs at marching positions, which neither frame
	// rejection nor a normal stack sigma-clip removes, with no global SNR cost. ~3.0 is a good default;
	// lower is more aggressive. 0 → disabled. See internal/transient.
	TrailMaskK float64
	// HaExcludeStars screens Ha onto extended nebulosity only (point-like stars median-filtered out),
	// instead of over everything. The default (false) applies Ha to the whole frame.
	HaExcludeStars bool

	// Previews emits per-channel and final preview PNGs for the UI.
	Previews bool

	// Supervise enables the optional local-AI-agent finish: when on (set by the run request /
	// --supervise) and a host model server is reachable, the GIMP composite is re-rendered a few
	// times, each judged by a local vision model, keeping the best. Off in every mode by default.
	// SuperviseMaxIters bounds the loop (0 → engine default). See internal/pipeline supervise.go.
	Supervise         bool
	SuperviseMaxIters int
	// SuperviseTier caps how far the agent may reach when it re-processes between iterations:
	// "A" = GIMP composite only, "B" = also the linear finish prep, "C"/"" = also re-stack from the
	// raw frames (full autonomy). Empty → full. Tier C additionally needs raw frames (Options.Reprocess).
	SuperviseTier string

	// Nightscape (milkyway) controls. Look selects the render style (natural/iphone/deepsky);
	// ForegroundFrame optionally overrides the auto-picked clean foreground source (a raw frame path);
	// Orientation is the final display transform (auto|none|cw|ccw|180, optionally +"-flip").
	Look            string
	ForegroundFrame string
	Orientation     string

	// Comet controls (comet mode). When all four are > 0 they override auto-detection with the comet's
	// pixel position in the first (X1,Y1) and last (X2,Y2) star-aligned frame; otherwise the comet is
	// auto-centroided. CometX1 etc. are registered-frame pixel coordinates.
	CometX1, CometY1, CometX2, CometY2 float64
}

// WantsVideo reports whether a video artifact should be produced.
func (f Format) WantsVideo() bool { return f == FormatVideo || f == FormatBoth }

// WantsImage reports whether a still image should be produced (always, except pure video).
func (f Format) WantsImage() bool { return f != FormatVideo }

// ParseMode validates a mode string.
func ParseMode(s string) (Mode, error) {
	switch Mode(strings.ToLower(s)) {
	case Deepsky, Nebula, Milkyway, Planetary, Livestack, Comet:
		return Mode(strings.ToLower(s)), nil
	default:
		return "", fmt.Errorf("unknown mode %q (want: deepsky, nebula, milkyway, planetary, livestack, comet)", s)
	}
}

// ParseFormat validates a format string.
func ParseFormat(s string) (Format, error) {
	switch Format(strings.ToLower(s)) {
	case FormatImage, FormatVideo, FormatBoth:
		return Format(strings.ToLower(s)), nil
	default:
		return "", fmt.Errorf("unknown format %q (want: image, video, both)", s)
	}
}

// For returns the preset for a mode.
func For(m Mode) Preset {
	switch m {
	case Comet:
		// Comet mode reuses the deepsky LRGB tuning (it runs the channel pipeline twice) but enables
		// StarNet so the star layer can be lifted, and skips the supervisor/Ha-specific finish. The dual
		// star/comet stacking + recomposite is handled by pipeline.ProcessComet, not the preset.
		p := For(Deepsky)
		p.Mode = Comet
		p.StarReduce = 0.5 // ensure StarNet is wired (used to separate the star layer)
		p.Supervise = false
		return p
	case Nebula:
		return Preset{
			Mode:             Nebula,
			Color:            Mono,
			Grade:            grade.Options{RoundnessFloor: 0.50, RoundnessSigma: 3.0, FWHMSigma: 3.0, BackgroundSigma: 3.0, StarCountFrac: 0.4, RejectTrails: true},
			BackgroundDegree: 2,
			HaScreen:         0.60,
			Saturation:       0.10,
			Curve:            []float64{0, 0, 0.20, 0.27, 0.5, 0.58, 0.8, 0.85, 1, 1}, // lift faint nebulosity

			DenoiseChroma: 0.85, DenoiseLum: 0.30, DenoiseVST: true, DenoiseDA3D: true,
			BackgroundAI: true, StarReduce: 0.5, // emission nebulae benefit most from star reduction
			TrailMaskK:                3.0,  // cross-frame transient mask: clean satellite trails / cosmic rays pre-stack
			CoreHighlightKnee:         0.64, // roll off the L luminance core highlights → structured pink knot
			CoreHighlightCeil:         0.76,
			DropFilterWheelTransition: true,
			ColorCalibration:          true,
			LinkedStretch:             true,
			BackgroundLevel:           0.09, // a touch brighter than deepsky to keep faint nebulosity visible
			HaBlackPoint:              0.07, // lighter clip: Ha is the subject here, only zero the sky pedestal
			Previews:                  true,
		}
	case Milkyway:
		return Preset{
			Mode:             Milkyway,
			Color:            OSC,
			Grade:            grade.Options{RoundnessFloor: 0.45, RoundnessSigma: 3.5, FWHMSigma: 3.5, BackgroundSigma: 3.5, StarCountFrac: 0.3, RejectTrails: false},
			BackgroundDegree: 3, // strong light-pollution gradients from a phone
			Saturation:       0.10,
			Curve:            []float64{0, 0, 0.3, 0.30, 0.6, 0.62, 1, 1}, // near-linear, preserve star colors

			DenoiseChroma: 0.60, DenoiseVST: true, DenoiseDA3D: true,
			BackgroundAI:     true, // strong phone light-pollution gradients
			ColorCalibration: true,
			LinkedStretch:    true,
			// Auto-levels target sky-background ("Balanced") for the nightscape composite; the UI/CLI
			// brightness control overrides it (Darker 0.04 / Balanced 0.06 / Brighter 0.09). The dedicated
			// recipe reads this via nightscape.Options.Brightness (data-driven auto-stretch). After the v3
			// per-channel black-clip the sky is genuinely dark, so the targets are lower than v2's.
			BackgroundLevel: 0.05,
			Previews:        true,
			Look:            "natural", // dedicated nightscape recipe: foreground composite + faithful grade
			Orientation:     "auto",
		}
	case Planetary:
		return Preset{
			Mode:      Planetary,
			Planetary: planetary.Options{BestPercent: 40, Sharpen: true, APAlign: true, Formats: []string{"png", "tif"}},
			Curve:     []float64{0, 0, 0.5, 0.52, 1, 1},
			Previews:  true, // lucky-imaging sharpens; no denoise/color-cal
		}
	case Livestack:
		// Live stacking finalizes through the standard deep-sky path; the per-batch live preview reads
		// only the grade thresholds. Reuse the deepsky preset verbatim, just retagging the mode.
		p := For(Deepsky)
		p.Mode = Livestack
		return p
	default: // Deepsky
		return Preset{
			Mode:             Deepsky,
			Color:            Mono,
			Grade:            grade.DefaultOptions(),
			BackgroundDegree: 1,
			HaScreen:         0.42, // a touch brighter so HII regions read red (HaBlackPoint keeps it off the sky/stars)
			Saturation:       0.16, // color is clean after AI denoise + SCNR, so a touch more saturation reads natural
			// Gentle value curve: with the background already flat (combined GraXpert) + neutral (SPCC), a
			// strong value curve would only re-amplify residual colour. Brightness/contrast for the galaxy
			// comes from LumCurve (the L luminance) instead — so the sky stays homogeneous, no banding.
			Curve: []float64{0, 0, 0.25, 0.24, 0.75, 0.78, 1, 1},
			// LumCurve lifts the galaxy from the clean L luminance (sky ~0.044, galaxy ~0.08 after the 0.06
			// autostretch): pull deep sky to near-black, lift the 0.05–0.30 band so the galaxy stands out,
			// and roll off highlights so star cores aren't clipped (natural, faded halos).
			LumCurve: []float64{0, 0, 0.04, 0.025, 0.08, 0.20, 0.15, 0.40, 0.28, 0.58, 0.5, 0.75, 0.8, 0.92, 1, 1},

			// Denoise the linear masters: luminance gently (preserve galaxy detail) and chroma hard (the
			// thin R/G/B sub-stacks are very noisy). VST suits the photon-limited linear data. ChromaBlur
			// then smooths residual colour noise in the finish — kept modest (6) so it doesn't smear
			// colour into star halos.
			DenoiseChroma: 0.85, DenoiseLum: 0.50, DenoiseVST: true, DenoiseDA3D: true,
			ChromaBlur:                0,     // 0: GraXpert AI denoise handles colour noise; no blur → crisp star halos
			CropFrac:                  0.035, // trim ragged stacking-edge bands off the export
			TrailMaskK:                3.0,   // cross-frame transient mask: clean satellite trails / cosmic rays pre-stack
			CoreHighlightKnee:         0.64,  // roll off the L luminance above this so the blown nebula core dims
			CoreHighlightCeil:         0.76,  // ...to this asymptote → structured pink knot, outer tones untouched
			BackgroundAI:              true,  // per-channel GraXpert background extraction
			CombinedBackgroundAI:      true,  // 2nd GraXpert pass on the combined RGB + RBF subsky → homogeneous sky
			ColorDenoiseAI:            true,  // GraXpert AI denoise on the combined colour → no RGB chroma speckle
			HaExcludeStars:            true,  // Ha on the galaxy/nebulosity only (stars median-removed)
			DropFilterWheelTransition: true,
			ColorCalibration:          true,
			LinkedStretch:             true,
			BackgroundLevel:           0.06, // dark, natural sky (vs Siril's washed-out 0.25 default)
			HaBlackPoint:              0.12, // clip Ha background to black so its red screen lifts only HII knots
			Previews:                  true,
		}
	}
}
