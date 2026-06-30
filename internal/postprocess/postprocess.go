// Package postprocess combines per-channel master stacks into a finished image with Siril:
// RGB / LRGB / Ha-enhanced RGB / SHO narrowband / mono depending on which channels exist. It runs
// as three staged Siril invocations — combine (+ background extraction) → color calibration (SPCC
// with a neutralization fallback) → finish (linked stretch, saturation, export) — so the engine can
// branch when plate-solving/SPCC is unavailable.
package postprocess

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/siril"
)

// Options tunes the post-processing chain.
type Options struct {
	BackgroundExtraction bool               // subtract a polynomial sky background (gradient removal)
	BackgroundDegree     int                // subsky polynomial degree (0 → 1)
	RemoveGreen          bool               // SCNR green removal (used as the color fallback)
	Saturation           float64            // color saturation boost (0 = skip; ~0.2 typical)
	ColorCalibration     bool               // attempt plate-solve + SPCC for natural color
	LinkedStretch        bool               // autostretch -linked (keep the neutral background)
	Solve                siril.SolveOptions // plate-solving inputs (focal/pixel/coords/catalog)
	Spcc                 siril.SpccOptions  // SPCC sensor/filter/white-reference
	Formats              []string           // output formats: png, tif, fits
}

// DefaultOptions returns a sensible automatic chain.
func DefaultOptions() Options {
	return Options{
		BackgroundExtraction: true,
		RemoveGreen:          true,
		Saturation:           0.2,
		LinkedStretch:        true,
		Formats:              []string{"png", "tif"},
	}
}

// Result describes the produced image.
type Result struct {
	Mode     string   `json:"mode"`     // LRGB / HaLRGB / RGB / HaRGB / SHO / mono
	Channels []string `json:"channels"` // filters used
	Outputs  []string `json:"outputs"`  // written file paths
	Notes    []string `json:"notes,omitempty"`
	// Iterations records each pass of the optional local-AI-agent finish supervisor (empty for a
	// normal finish). It feeds the UI's supervisor panel directly from the run result.
	Iterations []IterationRecord `json:"iterations,omitempty"`
}

// IterationRecord is one supervised finish render: its preview, the deterministic + model scores, the
// model's one-line reasoning, the composite params used, and whether it was the chosen best.
// Primitive fields only (no pipeline import) so package pipeline can populate it without a cycle.
type IterationRecord struct {
	Index         int                `json:"index"`
	PngPath       string             `json:"png_path"`
	DetScore      float64            `json:"det_score"`
	ModelScore    float64            `json:"model_score"`
	CombinedScore float64            `json:"combined_score"`
	Reasoning     string             `json:"reasoning"`
	Chosen        bool               `json:"chosen"`
	Params        map[string]float64 `json:"params,omitempty"`
}

// Combine builds the final image from channel masters located in dir (referenced by basename, e.g.
// "master_L"), writing finalBase.<ext> alongside them.
func Combine(ctx context.Context, runner *siril.Runner, dir string, channels map[string]string,
	finalBase string, opts Options, onProgress func(siril.Progress)) (*Result, error) {
	if len(channels) == 0 {
		return nil, fmt.Errorf("no channels to combine")
	}

	// Stage 1 — combine into a linear `combined.fits` and remove background gradients.
	combineScript, res := buildCombine(channels, opts)
	if _, err := runner.Run(ctx, dir, combineScript, onProgress); err != nil {
		return nil, fmt.Errorf("combine channels: %w", err)
	}

	// Stage 2 — color calibration (color modes only), SPCC with a neutralization fallback.
	if isColor(res.Mode) {
		note, err := ColorCalibrate(ctx, runner, dir, "combined", ColorCalOptions{
			Enabled: opts.ColorCalibration, RemoveGreen: opts.RemoveGreen, Solve: opts.Solve, Spcc: opts.Spcc,
		})
		if err != nil {
			return nil, err
		}
		if note != "" {
			res.Notes = append(res.Notes, note)
		}
	}

	// Stage 3 — finish: a linked stretch preserves the neutral balance; then saturate and export.
	sat := 0.0
	if isColor(res.Mode) {
		sat = opts.Saturation
	}
	finishScript := siril.FinishScript("combined", finalBase, opts.LinkedStretch, sat, opts.Formats)
	if _, err := runner.Run(ctx, dir, finishScript, onProgress); err != nil {
		return nil, fmt.Errorf("finish image: %w", err)
	}
	for _, f := range opts.Formats {
		res.Outputs = append(res.Outputs, filepath.Join(dir, finalBase+"."+f))
	}
	return res, nil
}

// buildCombine assembles the channels into a linear `combined.fits` (with background extraction),
// ready for color calibration and finishing.
func buildCombine(channels map[string]string, opts Options) (string, *Result) {
	has := func(f string) bool { _, ok := channels[f]; return ok }
	res := &Result{}
	for f := range channels {
		res.Channels = append(res.Channels, f)
	}

	var b strings.Builder
	b.WriteString("requires 1.2.0\nsetext fits\n")
	switch {
	case has("R") && has("G") && has("B"):
		buildColor(&b, channels, has, res)
	case has("Ha") && has("OIII") && has("SII"):
		// Hubble (SHO) palette: R=SII, G=Ha, B=OIII.
		fmt.Fprintf(&b, "rgbcomp %s %s %s -out=combined\n", channels["SII"], channels["Ha"], channels["OIII"])
		b.WriteString("load combined\n")
		res.Mode = "SHO"
	default:
		fmt.Fprintf(&b, "load %s\n", firstChannel(channels))
		res.Mode = "mono"
		res.Notes = append(res.Notes, "incomplete channel set — produced a monochrome result")
	}

	if opts.BackgroundExtraction {
		degree := opts.BackgroundDegree
		if degree <= 0 {
			degree = 1
		}
		fmt.Fprintf(&b, "subsky %d\n", degree)
	}
	b.WriteString("save combined\n")
	return b.String(), res
}

func buildColor(b *strings.Builder, channels map[string]string, has func(string) bool, res *Result) {
	red := channels["R"]
	if has("Ha") { // blend Ha into red (lighten) for nebulosity
		fmt.Fprintf(b, "pm \"max($%s$, $%s$)\"\nsave red_ha\n", channels["R"], channels["Ha"])
		red = "red_ha"
	}
	lum := ""
	if has("L") {
		lum = channels["L"]
		if has("Ha") {
			fmt.Fprintf(b, "pm \"max($%s$, $%s$)\"\nsave lum_ha\n", channels["L"], channels["Ha"])
			lum = "lum_ha"
		}
	}
	if lum != "" {
		fmt.Fprintf(b, "rgbcomp %s %s %s -lum=%s -out=combined\n", red, channels["G"], channels["B"], lum)
	} else {
		fmt.Fprintf(b, "rgbcomp %s %s %s -out=combined\n", red, channels["G"], channels["B"])
	}
	b.WriteString("load combined\n")
	switch {
	case has("Ha") && lum != "":
		res.Mode = "HaLRGB"
	case has("Ha"):
		res.Mode = "HaRGB"
	case lum != "":
		res.Mode = "LRGB"
	default:
		res.Mode = "RGB"
	}
}

// isColor reports whether a finished mode carries color (and thus wants color calibration/SCNR).
func isColor(mode string) bool {
	switch mode {
	case "RGB", "LRGB", "HaRGB", "HaLRGB", "SHO":
		return true
	default:
		return false
	}
}

// firstChannel prefers L, then Ha, then any remaining channel deterministically.
func firstChannel(channels map[string]string) string {
	for _, f := range []string{"L", "Ha", "R", "G", "B", "OIII", "SII"} {
		if v, ok := channels[f]; ok {
			return v
		}
	}
	for _, v := range channels { // any
		return v
	}
	return ""
}
