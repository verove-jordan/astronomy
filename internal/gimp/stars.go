package gimp

import (
	"fmt"
	"strings"
)

// ReduceStars blends a starless image (from StarNet++) under the original, with-stars image to
// produce a star-reduced result. The original is composited over the starless base in Normal mode
// at `reduce` opacity (0..1), which is exactly result = (1-reduce)·starless + reduce·original — so
// the stars (original − starless) survive scaled by `reduce` (e.g. 0.5 halves their intensity, 0
// removes them entirely). Writes a flattened .tif and .png.
func ReduceStars(c *Client, original, starless string, reduce float64, outBase string) (*Result, error) {
	res := &Result{Tif: outBase + ".tif", Png: outBase + ".png"}
	if _, err := c.Eval(reduceStarsScript(original, starless, reduce, res)); err != nil {
		return nil, err
	}
	return res, nil
}

// reduceStarsScript builds the Script-Fu program for the star-reduction blend (pure, for testing).
func reduceStarsScript(original, starless string, reduce float64, res *Result) string {
	var b strings.Builder
	b.WriteString("(let* ((image (car (gimp-file-load RUN-NONINTERACTIVE " + sf(starless) + " " + sf(starless) + ")))\n")
	b.WriteString("       (stars (car (gimp-file-load-layer RUN-NONINTERACTIVE image " + sf(original) + "))))\n")
	b.WriteString("  (gimp-image-insert-layer image stars 0 -1)\n")
	fmt.Fprintf(&b, "  (gimp-layer-set-opacity stars %.0f)\n", clamp01(reduce)*100)
	b.WriteString("  (gimp-image-flatten image)\n")
	b.WriteString("  (gimp-file-save RUN-NONINTERACTIVE image (car (gimp-image-get-active-drawable image)) " + sf(res.Tif) + " " + sf(res.Tif) + ")\n")
	b.WriteString("  (gimp-file-save RUN-NONINTERACTIVE image (car (gimp-image-get-active-drawable image)) " + sf(res.Png) + " " + sf(res.Png) + ")\n")
	b.WriteString("  (gimp-image-delete image))\n")
	return b.String()
}
