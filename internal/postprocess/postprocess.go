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
	// Quality is the objective post-render snapshot measured from the exported finish PNG on EVERY
	// run (not only supervised ones) — the deterministic colour/clipping guardrails, persisted so a
	// warm or clipped result is flagged in the run record instead of discovered by eye.
	Quality *FinishQuality `json:"finish_quality,omitempty"`
}

// FinishQuality mirrors the supervisor's finish metrics for the run record (primitive fields only —
// the measuring code lives in internal/pipeline, which imports this package).
type FinishQuality struct {
	BlackClip  [3]float64 `json:"black_clip"`  // fraction of pixels at 0, per channel R,G,B
	WhiteClip  [3]float64 `json:"white_clip"`  // fraction of pixels at 255, per channel R,G,B
	Median     [3]float64 `json:"median"`      // per-channel median, 0..1
	Background float64    `json:"background"`  // sky level estimate (10th-percentile luma), 0..1
	GreenCast  float64    `json:"green_cast"`  // medianG − mean(medianR, medianB); >0 → green cast
	WarmCast   float64    `json:"warm_cast"`   // sky red-excess on the 10th-pct background; >0 → warm
	SignalCast float64    `json:"signal_cast"` // bright-signal green balance; <0 → magenta/pink
}

// Defect is one issue the vision model diagnosed in a rendered finish (a fixed Kind vocabulary, a
// low|medium|high Severity, and an optional free-text Note). Primitive fields only (no pipeline
// import) so it can be persisted and surfaced in the UI without a cycle.
type Defect struct {
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Note     string `json:"note,omitempty"`
}

// IterationRecord is one supervised finish render: its preview, the pipeline re-entry Tier it used,
// the deterministic + model scores, the model's diagnosed Defects and one-line reasoning, the params
// used, and whether it was the chosen best. Primitive fields only (no pipeline import) so package
// pipeline can populate it without a cycle.
type IterationRecord struct {
	Index         int                `json:"index"`
	Tier          string             `json:"tier,omitempty"`
	PngPath       string             `json:"png_path"`
	DetScore      float64            `json:"det_score"`
	ModelScore    float64            `json:"model_score"`
	CombinedScore float64            `json:"combined_score"`
	Reasoning     string             `json:"reasoning"`
	Defects       []Defect           `json:"defects,omitempty"`
	Chosen        bool               `json:"chosen"`
	Params        map[string]float64 `json:"params,omitempty"`
}

// StagePreview is one saved preview PNG of the image at a major processing milestone (stacked, aligned,
// combined, colour-calibrated, star-reduced, final). Index drives the timeline order; Stage is a key the
// UI maps to a localized label; Filter is set for per-channel milestones (L/R/G/B/Ha). Primitive fields
// only (no pipeline import) so package pipeline can populate it without a cycle.
type StagePreview struct {
	Index   int    `json:"index"`
	Stage   string `json:"stage"`
	Filter  string `json:"filter,omitempty"`
	PngPath string `json:"png_path"`
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
		note, _, err := ColorCalibrate(ctx, runner, dir, "combined", ColorCalOptions{
			Enabled: opts.ColorCalibration, RemoveGreen: opts.RemoveGreen, StarField: true, Solve: opts.Solve, Spcc: opts.Spcc,
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
