// Package postprocess combines per-channel master stacks into a finished image with Siril:
// RGB / LRGB / Ha-enhanced RGB / SHO narrowband / mono depending on which channels exist, then
// background extraction, an automatic stretch, saturation, and export to PNG/TIFF/FITS.
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
	BackgroundExtraction bool     // subtract a polynomial sky background
	BackgroundDegree     int      // subsky polynomial degree (0 → 1)
	RemoveGreen          bool     // SCNR green removal (off by default; not always applicable)
	Saturation           float64  // color saturation boost (0 = skip; ~0.2 typical)
	Formats              []string // output formats: png, tif, fits
}

// DefaultOptions returns a sensible automatic chain.
func DefaultOptions() Options {
	return Options{
		BackgroundExtraction: true,
		RemoveGreen:          false,
		Saturation:           0.2,
		Formats:              []string{"png", "tif"},
	}
}

// Result describes the produced image.
type Result struct {
	Mode     string   `json:"mode"`     // LRGB / HaLRGB / RGB / HaRGB / SHO / mono
	Channels []string `json:"channels"` // filters used
	Outputs  []string `json:"outputs"`  // written file paths
	Notes    []string `json:"notes,omitempty"`
}

// Combine builds the final image from channel masters located in dir (referenced by basename,
// e.g. "master_L"), writing finalBase.<ext> alongside them.
func Combine(ctx context.Context, runner *siril.Runner, dir string, channels map[string]string,
	finalBase string, opts Options, onProgress func(siril.Progress)) (*Result, error) {
	if len(channels) == 0 {
		return nil, fmt.Errorf("no channels to combine")
	}
	script, res := buildScript(channels, finalBase, opts)
	if _, err := runner.Run(ctx, dir, script, onProgress); err != nil {
		return nil, err
	}
	for _, f := range opts.Formats {
		res.Outputs = append(res.Outputs, filepath.Join(dir, finalBase+"."+f))
	}
	return res, nil
}

func buildScript(channels map[string]string, finalBase string, opts Options) (string, *Result) {
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
		mono := firstChannel(channels)
		fmt.Fprintf(&b, "load %s\n", mono)
		res.Mode = "mono"
		res.Notes = append(res.Notes, "incomplete channel set — produced a monochrome result")
	}

	writeFinish(&b, finalBase, res.Mode != "mono", opts, res)
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

func writeFinish(b *strings.Builder, finalBase string, color bool, opts Options, res *Result) {
	if opts.BackgroundExtraction {
		degree := opts.BackgroundDegree
		if degree <= 0 {
			degree = 1
		}
		fmt.Fprintf(b, "subsky %d\n", degree)
	}
	if opts.RemoveGreen && color {
		b.WriteString("rmgreen 1\n")
	}
	b.WriteString("autostretch\n")
	if color && opts.Saturation > 0 {
		fmt.Fprintf(b, "satu %.2f\n", opts.Saturation)
	}
	for _, f := range opts.Formats {
		b.WriteString(saveCmd(f, finalBase) + "\n")
	}
	res.Notes = append(res.Notes, "color calibration (PCC) skipped — requires plate solving")
}

func saveCmd(format, base string) string {
	switch format {
	case "png":
		return "savepng " + base
	case "tif", "tiff":
		return "savetif " + base
	case "jpg", "jpeg":
		return "savejpg " + base + " 95"
	default:
		return "save " + base // FITS
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
