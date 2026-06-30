package gimp

import (
	"fmt"
	"math"
	"strings"
)

// coreShoulderSamples is the LUT resolution for the L-luminance highlight shoulder (curves-explicit).
const coreShoulderSamples = 256

// Inputs are the stretched per-component TIFFs (produced by Siril) to composite.
type Inputs struct {
	Base    string  // RGB or mono base image (required)
	Lum     string  // optional luminance layer (LRGB)
	Ha      string  // optional Ha layer (screened, tinted red)
	Color   bool    // base is color → apply saturation
	HaBlack float64 // Ha layer black-point (levels low-input, 0..1): clip its background to black so the red Screen lifts only bright HII knots, not the whole sky. 0 → no clip.

	// ChromaBlur gaussian-blurs the colour base by this many px before the luminance layer is added.
	// In an LRGB composite the L layer supplies all the detail, so blurring the (thin, noisy) RGB
	// colour erases its chroma noise — the classic "pink noise" of short colour subs — with no loss of
	// sharpness. 0 → skip. Only applied when Lum is set (else it would soften the only detail there is).
	// Keep it modest (~6 px): too much smears colour into star halos.
	ChromaBlur float64
	// LumCurve is a curves-spline (flat x,y pairs in 0..1) applied to the L layer *before* it blends as
	// LUMINANCE — so the galaxy's brightness/contrast comes from the clean luminance. Curving the
	// luminance (not the combined value) avoids amplifying any residual background colour into banding.
	// Empty → no luminance curve. Only used when Lum is set.
	LumCurve []float64
	// CoreHighlightKnee / CoreHighlightCeil add a highlight roll-off to the L luminance, after LumCurve and
	// *before* it blends as luminance + the Ha screen lights it: a 3-point spline {0,0, knee,knee, 1,ceil}.
	// It is identity up to knee (outer nebula / stars / background untouched) and asymptotes the bright
	// core to ceil < 1, so a blown-white centre becomes a dim, structured knot the Ha screen then tints
	// deep pink. Disabled unless 0 < knee < ceil < 1. Only used when Lum is set.
	CoreHighlightKnee, CoreHighlightCeil float64
	// HaExcludeStars median-filters the Ha layer before it is screened, so point-like stars drop out
	// and the red screen lifts only extended HII nebulosity (not star halos). Default off → Ha on all.
	HaExcludeStars bool
	// CropFrac trims this fraction off each edge of the exported TIFF/PNG to drop the ragged
	// stacking-edge bands (dithered frame borders). 0 → no crop. The layered .xcf keeps the full frame.
	CropFrac float64
}

// Result holds the written file paths.
type Result struct {
	Xcf string
	Tif string
	Png string
}

// BuildImage composes a layered image in GIMP — base + optional L (Luminance blend) + optional Ha
// (red-tinted, Screen) — saves the layered .xcf, then exports a flattened, gently curve/saturation
// adjusted .tif and .png. The shared GIMP image is deleted afterward.
func BuildImage(c *Client, in Inputs, curve []float64, haScreen, saturation float64, outBase string) (*Result, error) {
	res := &Result{Xcf: outBase + ".xcf", Tif: outBase + ".tif", Png: outBase + ".png"}
	if _, err := c.Eval(composeScript(in, curve, haScreen, saturation, res)); err != nil {
		return nil, err
	}
	return res, nil
}

// composeScript builds the Script-Fu program for the layered composite (pure, for testing).
func composeScript(in Inputs, curve []float64, haScreen, saturation float64, res *Result) string {
	var b strings.Builder
	b.WriteString("(let* ((image (car (gimp-file-load RUN-NONINTERACTIVE " + sf(in.Base) + " " + sf(in.Base) + "))))\n")

	// Chroma denoise: blur the colour base; the L luminance layer below restores every bit of detail,
	// so this erases the thin RGB's chroma noise without softening the image. LRGB only (needs Lum).
	if in.ChromaBlur > 0 && in.Lum != "" {
		fmt.Fprintf(&b, "  (plug-in-gauss RUN-NONINTERACTIVE image (car (gimp-image-get-active-drawable image)) %.1f %.1f 0)\n", in.ChromaBlur, in.ChromaBlur)
	}

	if in.Lum != "" {
		b.WriteString("  (let ((lum (car (gimp-file-load-layer RUN-NONINTERACTIVE image " + sf(in.Lum) + "))))\n")
		b.WriteString("    (gimp-image-insert-layer image lum 0 -1)\n")
		if len(in.LumCurve) >= 4 { // brighten the galaxy from the luminance, not the combined value
			fmt.Fprintf(&b, "    (gimp-drawable-curves-spline lum HISTOGRAM-VALUE %d %s)\n", len(in.LumCurve), floatVec(in.LumCurve))
		}
		// Roll the bright CORE down before it blends as luminance and the Ha screen lifts it. An EXPLICIT
		// LUT (not a spline — a spline bows below the knee and would shift the whole nebula): exact identity
		// up to knee, then a smooth tanh shoulder asymptoting to ceil, so ONLY the blown centre dims and the
		// outer nebula / stars / background stay byte-identical.
		if k, c := in.CoreHighlightKnee, in.CoreHighlightCeil; k > 0 && k < c && c < 1 {
			fmt.Fprintf(&b, "    (gimp-drawable-curves-explicit lum HISTOGRAM-VALUE %d %s)\n", coreShoulderSamples, floatVec(coreShoulderLUT(k, c)))
		}
		b.WriteString("    (gimp-layer-set-mode lum LAYER-MODE-LUMINANCE))\n")
	}
	if in.Ha != "" {
		b.WriteString("  (let ((ha (car (gimp-file-load-layer RUN-NONINTERACTIVE image " + sf(in.Ha) + "))))\n")
		b.WriteString("    (gimp-image-insert-layer image ha 0 -1)\n")
		if in.HaExcludeStars { // median-filter point-like stars out so the red screen lifts only HII nebulosity
			b.WriteString("    (plug-in-median-blur RUN-NONINTERACTIVE image ha 8 50)\n")
		}
		if hb := clamp01(in.HaBlack); hb > 0 { // raise the black point so the screened red lifts only bright HII, not the sky pedestal
			fmt.Fprintf(&b, "    (gimp-drawable-levels ha HISTOGRAM-VALUE %.4f 1 TRUE 1 0 1 TRUE)\n", hb)
		}
		b.WriteString("    (gimp-drawable-levels ha HISTOGRAM-GREEN 0 1 TRUE 1 0 0 TRUE)\n") // kill green
		b.WriteString("    (gimp-drawable-levels ha HISTOGRAM-BLUE 0 1 TRUE 1 0 0 TRUE)\n")  // kill blue → red
		b.WriteString("    (gimp-layer-set-mode ha LAYER-MODE-SCREEN)\n")
		fmt.Fprintf(&b, "    (gimp-layer-set-opacity ha %.0f))\n", clamp01(haScreen)*100)
	}

	// Save the layered project (all layers preserved, full frame).
	b.WriteString("  (gimp-file-save RUN-NONINTERACTIVE image (car (gimp-image-get-active-drawable image)) " + sf(res.Xcf) + " " + sf(res.Xcf) + ")\n")

	// Flatten a copy, apply gentle curves (+ saturation for color), crop ragged edges, export.
	b.WriteString("  (let* ((dup (car (gimp-image-duplicate image))) (d (car (gimp-image-flatten dup))))\n")
	if len(curve) >= 4 {
		fmt.Fprintf(&b, "    (gimp-drawable-curves-spline d HISTOGRAM-VALUE %d %s)\n", len(curve), floatVec(curve))
	}
	if in.Color {
		// Neutralize the green sky-cast (a light SCNR-equivalent) for a natural background.
		b.WriteString("    (gimp-drawable-hue-saturation d HUE-RANGE-GREEN 0 0 -35 0)\n")
		if saturation > 0 {
			fmt.Fprintf(&b, "    (gimp-drawable-hue-saturation d HUE-RANGE-ALL 0 0 %.0f 0)\n", clamp(saturation*100, 0, 100))
		}
	}
	if cf := clamp(in.CropFrac, 0, 0.2); cf > 0 { // trim ragged stacking-edge bands off the export
		fmt.Fprintf(&b, "    (let* ((w (car (gimp-image-width dup))) (h (car (gimp-image-height dup))) (cx (inexact->exact (round (* w %.4f)))) (cy (inexact->exact (round (* h %.4f))))) (gimp-image-crop dup (- w (* 2 cx)) (- h (* 2 cy)) cx cy))\n", cf, cf)
		b.WriteString("    (set! d (car (gimp-image-get-active-drawable dup)))\n")
	}
	b.WriteString("    (gimp-file-save RUN-NONINTERACTIVE dup d " + sf(res.Tif) + " " + sf(res.Tif) + ")\n")
	b.WriteString("    (gimp-image-flatten dup)\n")
	b.WriteString("    (gimp-file-save RUN-NONINTERACTIVE dup (car (gimp-image-get-active-drawable dup)) " + sf(res.Png) + " " + sf(res.Png) + ")\n")
	b.WriteString("    (gimp-image-delete dup))\n")
	b.WriteString("  (gimp-image-delete image))\n")
	return b.String()
}

// sf escapes a string into a TinyScheme double-quoted literal.
func sf(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// coreShoulderLUT samples a highlight shoulder over [0,1] in coreShoulderSamples points: exact identity up
// to knee, then a tanh roll-off asymptoting to ceil. tanh has unit slope at 0, so the join at the knee is
// smooth (no visible brightness edge). Fed to gimp-drawable-curves-explicit so nothing below knee moves.
func coreShoulderLUT(knee, ceil float64) []float64 {
	span := ceil - knee
	lut := make([]float64, coreShoulderSamples)
	for i := range lut {
		x := float64(i) / float64(coreShoulderSamples-1)
		if x <= knee {
			lut[i] = x
		} else {
			lut[i] = knee + span*math.Tanh((x-knee)/span)
		}
	}
	return lut
}

// floatVec renders a flat float slice as a Scheme float vector #(a b c ...).
func floatVec(v []float64) string {
	parts := make([]string, len(v))
	for i, x := range v {
		parts[i] = fmt.Sprintf("%.4f", x)
	}
	return "#(" + strings.Join(parts, " ") + ")"
}

func clamp01(v float64) float64 { return clamp(v, 0, 1) }

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
