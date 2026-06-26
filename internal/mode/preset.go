// Package mode defines the capture modes (deepsky/nebula/milkyway/planetary) and the output
// format, and maps each mode to a Preset that retunes grading, background extraction, stretch,
// Ha blending, saturation and final curves across the whole pipeline.
package mode

import (
	"fmt"
	"strings"

	"github.com/verove-jordan/astronomy/internal/grade"
	"github.com/verove-jordan/astronomy/internal/planetary"
)

// Mode is a capture subject type; it drives the whole pipeline preset.
type Mode string

const (
	Deepsky   Mode = "deepsky"   // galaxies/clusters at native focal length (mono LRGB+Ha)
	Nebula    Mode = "nebula"    // large faint emission objects (mono LRGB+Ha, Ha-forward)
	Milkyway  Mode = "milkyway"  // wide-field one-shot-color (e.g. iPhone ProRAW/HEIC)
	Planetary Mode = "planetary" // Moon/planets via lucky imaging
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
	Curve            []float64         // gentle curves spline control points, flat x,y pairs in 0..1
	Planetary        planetary.Options // lucky-imaging settings (planetary only)

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
	StarReduce   float64

	// ColorCalibration attempts plate-solve + SPCC for natural color + a neutral background,
	// falling back to background neutralization. LinkedStretch keeps that neutral balance.
	ColorCalibration bool
	LinkedStretch    bool

	// Previews emits per-channel and final preview PNGs for the UI.
	Previews bool
}

// WantsVideo reports whether a video artifact should be produced.
func (f Format) WantsVideo() bool { return f == FormatVideo || f == FormatBoth }

// WantsImage reports whether a still image should be produced (always, except pure video).
func (f Format) WantsImage() bool { return f != FormatVideo }

// ParseMode validates a mode string.
func ParseMode(s string) (Mode, error) {
	switch Mode(strings.ToLower(s)) {
	case Deepsky, Nebula, Milkyway, Planetary:
		return Mode(strings.ToLower(s)), nil
	default:
		return "", fmt.Errorf("unknown mode %q (want: deepsky, nebula, milkyway, planetary)", s)
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
			DropFilterWheelTransition: true,
			ColorCalibration:          true,
			LinkedStretch:             true,
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
			Previews:         true,
		}
	case Planetary:
		return Preset{
			Mode:      Planetary,
			Planetary: planetary.Options{BestPercent: 40, Sharpen: true, Formats: []string{"png", "tif"}},
			Curve:     []float64{0, 0, 0.5, 0.52, 1, 1},
			Previews:  true, // lucky-imaging sharpens; no denoise/color-cal
		}
	default: // Deepsky
		return Preset{
			Mode:             Deepsky,
			Color:            Mono,
			Grade:            grade.DefaultOptions(),
			BackgroundDegree: 1,
			HaScreen:         0.30,
			Saturation:       0.15,
			Curve:            []float64{0, 0, 0.25, 0.28, 0.5, 0.55, 0.75, 0.80, 1, 1}, // gentle S

			DenoiseChroma: 0.70, DenoiseLum: 0, DenoiseVST: true, DenoiseDA3D: true,
			BackgroundAI:              true, // galaxies sit on faint gradients; keep stars (StarReduce 0)
			DropFilterWheelTransition: true,
			ColorCalibration:          true,
			LinkedStretch:             true,
			Previews:                  true,
		}
	}
}
