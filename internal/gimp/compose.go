package gimp

import (
	"fmt"
	"strings"
)

// Inputs are the stretched per-component TIFFs (produced by Siril) to composite.
type Inputs struct {
	Base  string // RGB or mono base image (required)
	Lum   string // optional luminance layer (LRGB)
	Ha    string // optional Ha layer (screened, tinted red)
	Color bool   // base is color → apply saturation
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

	if in.Lum != "" {
		b.WriteString("  (let ((lum (car (gimp-file-load-layer RUN-NONINTERACTIVE image " + sf(in.Lum) + "))))\n")
		b.WriteString("    (gimp-image-insert-layer image lum 0 -1)\n")
		b.WriteString("    (gimp-layer-set-mode lum LAYER-MODE-LUMINANCE))\n")
	}
	if in.Ha != "" {
		b.WriteString("  (let ((ha (car (gimp-file-load-layer RUN-NONINTERACTIVE image " + sf(in.Ha) + "))))\n")
		b.WriteString("    (gimp-image-insert-layer image ha 0 -1)\n")
		b.WriteString("    (gimp-drawable-levels ha HISTOGRAM-GREEN 0 1 TRUE 1 0 0 TRUE)\n") // kill green
		b.WriteString("    (gimp-drawable-levels ha HISTOGRAM-BLUE 0 1 TRUE 1 0 0 TRUE)\n")  // kill blue → red
		b.WriteString("    (gimp-layer-set-mode ha LAYER-MODE-SCREEN)\n")
		fmt.Fprintf(&b, "    (gimp-layer-set-opacity ha %.0f))\n", clamp01(haScreen)*100)
	}

	// Save the layered project (all layers preserved).
	b.WriteString("  (gimp-file-save RUN-NONINTERACTIVE image (car (gimp-image-get-active-drawable image)) " + sf(res.Xcf) + " " + sf(res.Xcf) + ")\n")

	// Flatten a copy, apply gentle curves (+ saturation for color), export.
	b.WriteString("  (let* ((dup (car (gimp-image-duplicate image))) (d (car (gimp-image-flatten dup))))\n")
	if len(curve) >= 4 {
		fmt.Fprintf(&b, "    (gimp-drawable-curves-spline d HISTOGRAM-VALUE %d %s)\n", len(curve), floatVec(curve))
	}
	if in.Color && saturation > 0 {
		fmt.Fprintf(&b, "    (gimp-drawable-hue-saturation d HUE-RANGE-ALL 0 0 %.0f 0)\n", clamp(saturation*100, 0, 100))
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
