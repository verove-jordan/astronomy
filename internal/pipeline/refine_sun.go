package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fits"
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

	hero := masters[0]
	im, err := fits.ReadImage(hero)
	if err != nil {
		return nil, err
	}
	mono := &fits.Image{W: im.W, H: im.H, C: 1, Pix: [][]float32{im.Pix[0]}}
	limb, ok := solar.FitLimb(mono)
	if !ok {
		return nil, fmt.Errorf("refine sun: no limb in %s", filepath.Base(hero))
	}
	// Resolved the same way the run resolved it, from the same master: a re-finish that deconvolved
	// at a different width from the run would not be a re-tune of the run's image, it would be a
	// different image, and every knob judged against it would be judged against the wrong thing.
	fin, _, notes := solar.ResolveFinish(mono, limb, preset.Finish)
	outs, err := writeSunImage(solar.Finish(mono, limb, fin), filepath.Join(runDir, object+"_stack"))
	if err != nil {
		return nil, err
	}
	return &postprocess.Result{
		Mode:    "sun",
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
