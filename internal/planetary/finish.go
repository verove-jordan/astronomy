package planetary

import (
	"context"
	"os"

	"github.com/verove-jordan/astronomy/internal/siril"
)

// runFinish executes the planetary finish over the channel masters — the one path shared by
// Process and Refinish. Three orthogonal flavours:
//   - legacy: mono runs, or colour with TrueLum off — the historical single finish script,
//     byte-identical when EarthshineGain is also off;
//   - TrueLum (colour + L): compose script → Go re-imposes L as the exact luminance → tone script;
//   - EarthshineGain > 0: the tone/finish stage saves a FITS, Go composites the earthshine lift,
//     a tiny export script writes the run's real formats.
//
// Go stages soft-fail into notes; only Siril failures abort the finish.
func runFinish(ctx context.Context, runner *siril.Runner, dir, r, g, b, l, mono, outBase string,
	sharpen bool, fin siril.PlanetaryFinish, formats []string, onProgress func(siril.Progress)) ([]string, error) {
	var notes []string
	// Limb balance runs FIRST, on compressed COPIES of the masters (the persisted originals stay
	// pristine — a supervised re-finish re-derives from them and never compounds the compression).
	if fin.LimbBalance > 0 {
		var n string
		r, g, b, l, mono, n = limbBalance(r, g, b, l, mono, fin.LimbBalance)
		if n != "" {
			notes = append(notes, n)
		}
	}
	color := r != "" && g != "" && b != ""
	trueLum := color && fin.TrueLum && l != ""
	earthshine := fin.EarthshineGain > 0
	if !trueLum && !earthshine {
		_, err := runner.Run(ctx, dir, siril.PlanetaryFinishScript(r, g, b, l, mono, outBase, sharpen, fin, formats), onProgress)
		return notes, err
	}
	toneFormats := formats
	if earthshine {
		toneFormats = []string{"fits"}
	}
	if trueLum {
		if _, err := runner.Run(ctx, dir, siril.PlanetaryComposeScript(r, g, b, l, outBase), onProgress); err != nil {
			return nil, err
		}
		if n := reimposeLuminance(outBase, l); n != "" {
			notes = append(notes, n)
		}
		if _, err := runner.Run(ctx, dir, siril.PlanetaryToneScript(outBase, outBase, true, sharpen, fin, toneFormats), onProgress); err != nil {
			return notes, err
		}
	} else {
		script := siril.PlanetaryFinishScript(r, g, b, l, mono, outBase, sharpen, fin, toneFormats)
		if _, err := runner.Run(ctx, dir, script, onProgress); err != nil {
			return notes, err
		}
	}
	if !earthshine {
		return notes, nil
	}
	notes = append(notes, applyEarthshine(r, g, b, l, mono, outBase, fin)...)
	if _, err := runner.Run(ctx, dir, siril.ExportScript(outBase, formats), onProgress); err != nil {
		return notes, err
	}
	if !hasFormat(formats, "fits") && !hasFormat(formats, "fit") {
		// The intermediate float FITS weighs 64–192 MB per render (every supervised iteration
		// re-finishes); keep it only when the user asked for fits output.
		if rmErr := os.Remove(outBase + ".fits"); rmErr != nil && !os.IsNotExist(rmErr) {
			notes = append(notes, "note: intermediate finish FITS not removed: "+rmErr.Error())
		}
	}
	return notes, nil
}

func hasFormat(formats []string, want string) bool {
	for _, f := range formats {
		if f == want {
			return true
		}
	}
	return false
}
