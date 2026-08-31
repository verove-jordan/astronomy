package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/solar"
)

// refine_sun.go re-renders a finished solar run from its persisted window masters.
//
// It never re-stacks. The masters written by the run are linear and already registered, so the
// whole finish — flat, deconvolution, sharpening, limb flattening, prominence composite, palette —
// replays over them in a second or two. That is what makes the Refine panel and the supervised
// auto-tuner usable at all: a re-stack of a solar group means re-reading thousands of frames.
func refineSun(ctx context.Context, opts Options, runDir string) (*postprocess.Result, error) {
	masters, err := sunMasters(runDir)
	if err != nil {
		return nil, err
	}
	preset := solar.DefaultPreset()
	if opts.Preset != nil {
		preset = opts.Preset.Sun
	}
	object := sunObject(runDir)
	if opts.PriorObject != "" {
		object = opts.PriorObject
	}
	var notes []string
	// The recording's own colour is a property of the SOURCE, measured during the run and not carried
	// in the preset, so a re-finish that wants it has to measure it again. Without this the native
	// palette silently falls back to gold — the same picture, in the wrong colour, with no warning.
	if preset.WantsNativeColour() && !preset.Finish.NativeChroma.OK() {
		if rep, rerr := readTriageReport(runDir); rerr == nil {
			if ch, cerr := measureNativeColour(ctx, solar.MergeGroups(stackableGroups(rep)), opts.FfmpegBin); cerr == nil {
				preset.Finish.NativeChroma = ch
			} else {
				notes = append(notes, "native colour: "+cerr.Error()+" — falling back to the gold palette")
				preset.Finish.Palette = solar.PaletteGold
			}
		}
	}

	hero := masters[0]
	im, err := fits.ReadImage(hero)
	if err != nil {
		return nil, err
	}
	mono := &fits.Image{W: im.W, H: im.H, C: 1, Pix: [][]float32{im.Pix[0]}}
	// The SAME geometry the run resolved, two-body when the run was two-body. A re-finish that fitted
	// one circle to a crescent would not merely lose the occulter — it would fit whichever body won
	// the trim, and then fill nothing, so the tone curve would anchor on a disc that is mostly empty
	// canvas and render the crescent clipped white. That is exactly what a refine produced before
	// this line matched the run's.
	g, ok := solar.FitGeometry(mono, preset.TwoBody)
	if !ok {
		return nil, fmt.Errorf("refine sun: no limb in %s", filepath.Base(hero))
	}
	limb := g.Sun
	// Resolved the same way the run resolved it, from the same master: a re-finish that deconvolved
	// at a different width from the run would not be a re-tune of the run's image, it would be a
	// different image, and every knob judged against it would be judged against the wrong thing.
	fin, _, resolved := solar.ResolveFinish(mono, limb, preset.Finish)
	notes = append(notes, resolved...)
	outs, err := writeSunImage(solar.FinishPair(mono, g, fin), filepath.Join(runDir, object+"_stack"))
	if err != nil {
		return nil, err
	}
	// A run that made a phase sequence re-lays it here too, from its own persisted panels. Without
	// this the Refine panel would hand back a re-tuned hero image beside a sheet still rendered with
	// the old settings — two pictures of the same run that no longer agree.
	if preset.WantsSequence() {
		say := func(line string) {
			opts.report(Progress{Step: "laying the phase sequence out again", Line: line})
		}
		seqOuts, seqNotes := refineSequence(ctx, opts, runDir, preset, object, say)
		outs = append(outs, seqOuts...)
		notes = append(notes, seqNotes...)
		if len(seqOuts) > 0 {
			notes = append(notes, fmt.Sprintf("laid the phase sequence out again onto %d sheet(s)", len(seqOuts)))
		}
	}
	return &postprocess.Result{
		Mode:    string(preset0Mode(opts)),
		Outputs: outs,
		Notes: append([]string{fmt.Sprintf("re-finished from %d persisted master(s)", len(masters))},
			notes...),
	}, nil
}

// sunMasters lists a run's persisted masters, the one to re-finish first.
//
// A bracketed run's hero is the exposure COMPOSITE, not any single window, so when the run left one
// behind it leads the list. Refining has to replay the image the run actually finished; re-rendering
// one tier of a bracket would silently drop the other exposures and hand back a different picture
// from the one the knobs are being judged against.
func sunMasters(runDir string) ([]string, error) {
	entries, err := os.ReadDir(runDir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "master_w") && strings.HasSuffix(e.Name(), ".fits") {
			out = append(out, filepath.Join(runDir, e.Name()))
		}
	}
	sort.Strings(out)
	if hdr := filepath.Join(runDir, sunCompositeMaster); fileExists(hdr) {
		out = append([]string{hdr}, out...)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("refine sun: no window master in %s", runDir)
	}
	return out, nil
}

// preset0Mode names the mode a refine is replaying, so an eclipse re-finish does not report itself
// as a solar one in the result the UI reads.
func preset0Mode(opts Options) mode.Mode {
	if opts.Preset != nil && opts.Preset.Mode != "" {
		return opts.Preset.Mode
	}
	return mode.Sun
}
