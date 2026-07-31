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
	outs, err := writeSunImage(solar.Finish(mono, limb, preset.Finish), filepath.Join(runDir, object+"_stack"))
	if err != nil {
		return nil, err
	}
	return &postprocess.Result{
		Mode:    "sun",
		Outputs: outs,
		Notes:   []string{fmt.Sprintf("re-finished from %d persisted window master(s)", len(masters))},
	}, nil
}

// sunMasters lists a run's persisted window masters, in window order.
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
	if len(out) == 0 {
		return nil, fmt.Errorf("refine sun: no window master in %s", runDir)
	}
	sort.Strings(out)
	return out, nil
}
