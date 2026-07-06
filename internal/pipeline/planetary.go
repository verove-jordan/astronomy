// Planetary adapter + entry for the supervised finish. planetary.Process does the expensive lucky-
// imaging (rank → align → stack → deconvolve) and persists the stacked+deconvolved masters, so a
// candidate is a cheap re-finish (planetary.Refinish): the model tunes the stretch / wavelet sharpen /
// local contrast / saturation. ProcessPlanetary wraps planetary.Process so the manager gets the same
// flat planetary.Result whether or not the agent runs; single-stage (tierA), no re-stack.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/planetary"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// ProcessPlanetary runs the lucky-imaging pipeline, then — when the finish supervisor is opted in — re-
// tunes the finish over the persisted masters, keeping the best pass. Returns the flat planetary.Result
// (with the best outputs + the iteration timeline); soft-falls to the standard finish on any error.
func ProcessPlanetary(ctx context.Context, opts Options) (*planetary.Result, error) {
	r, err := planetary.Process(ctx, opts.Runner, opts.FfmpegBin, opts.InputDir, opts.WorkDir, opts.OutputDir,
		opts.Preset.Planetary, opts.sirilLines("planetary"))
	if err != nil {
		return nil, err
	}
	if superviseOn(ctx, opts) && len(r.Masters) > 0 {
		if final, serr := superviseFinishPlanetary(ctx, opts, r); serr != nil {
			r.Notes = append(r.Notes, "supervised planetary finish failed, using standard finish: "+serr.Error())
		} else {
			r.Outputs = final.Outputs
			r.Iterations = final.Iterations
			r.Notes = append(r.Notes, final.Notes...)
		}
	}
	// Milestone previews + the durable run record, saved under the run dir (output/<object>/<runID>).
	if r.OutBase != "" {
		outDir := filepath.Dir(r.OutBase)
		captureStackedMasters(ctx, opts, outDir, r.Masters)
		captureFinalPNG(ctx, opts, outDir, &postprocess.Result{Outputs: r.Outputs})
		r.StagePreviews = collectStagePreviews(outDir) // persist the milestone timeline for reload
		// Persist run.json so the run is reopenable, shows in the gallery, and a post-run refine + full-S3
		// push resolve this run dir — matching ProcessOSC/ProcessComet. The flat planetary.Result is
		// projected onto the pipeline.Result contract the UI + refine read.
		writeRunJSON(outDir, &Result{
			InputDir: opts.InputDir, OutputDir: outDir, Object: r.Object, RunID: r.RunID,
			Final: &postprocess.Result{
				Mode: "planetary", Outputs: r.Outputs, Notes: r.Notes, Iterations: r.Iterations,
			},
			StagePreviews: r.StagePreviews,
		})
	}
	return r, nil
}

// superviseFinishPlanetary re-tunes the planetary finish under the agent, keeping the best pass.
func superviseFinishPlanetary(ctx context.Context, opts Options, r *planetary.Result) (*postprocess.Result, error) {
	pr := &planetaryRenderer{
		opts: opts, masters: r.Masters, sharpen: r.Sharpen,
		outDir: filepath.Dir(r.OutBase), outBase: r.OutBase,
		formats: opts.Preset.Planetary.Formats, orig: *opts.Preset,
	}
	if err := pr.ready(ctx); err != nil {
		return nil, err
	}
	return superviseFinish(ctx, opts, pr, filepath.Dir(r.OutBase))
}

// planetaryRenderer re-finishes a planetary run from its persisted stacked+deconvolved masters.
type planetaryRenderer struct {
	opts    Options
	masters map[string]string
	sharpen bool
	outDir  string
	outBase string   // canonical final base (<outDir>/<object>_stack) the winner is written to
	formats []string // the run's output formats (png/tif) for the final render
	orig    mode.Preset
}

func (c *planetaryRenderer) ready(ctx context.Context) error {
	if c.opts.Runner == nil {
		return fmt.Errorf("siril unavailable")
	}
	if len(c.masters) == 0 {
		return fmt.Errorf("no planetary masters to re-finish")
	}
	return c.opts.Runner.Available(ctx)
}

func (c *planetaryRenderer) firstTier() tier      { return tierA }
func (c *planetaryRenderer) maxTier(Options) tier { return tierA }

func (c *planetaryRenderer) render(ctx context.Context, working mode.Preset, _ tier, outBase string) (renderResult, []string, error) {
	// PNG only for the per-iteration render — that is all the metrics + model need to score.
	if _, err := planetary.Refinish(ctx, c.opts.Runner, c.outDir, c.masters, c.sharpen, working.Planetary.Finish, []string{"png"}, outBase); err != nil {
		return renderResult{}, nil, err
	}
	return renderResult{Png: outBase + ".png"}, nil, nil
}

func (c *planetaryRenderer) prompt(working mode.Preset, _ tier) supervisePrompt {
	return supervisePrompt{
		system:    planetarySystemPrompt,
		objective: planetaryObjective(),
		state:     finishState(working.Planetary.Finish),
		tiered:    false,
	}
}

func (c *planetaryRenderer) applyPatch(working mode.Preset, raw json.RawMessage, _ tier) (mode.Preset, tier, bool) {
	var patch planetaryPatch
	if err := json.Unmarshal(raw, &patch); err != nil {
		return working, tierA, false
	}
	next := working
	f := &next.Planetary.Finish
	setF(&f.Stretch, patch.Stretch)
	setF(&f.Highlight, patch.Highlight)
	setF(&f.Sharpen, patch.Sharpen)
	setF(&f.Clahe, patch.Clahe)
	setF(&f.Saturation, patch.Saturation)
	*f = clampPlanetaryFinish(*f)
	changed := next.Planetary.Finish != working.Planetary.Finish // all float64 → comparable
	return next, tierA, changed
}

func (c *planetaryRenderer) params(p mode.Preset) map[string]float64 {
	return finishParams(p.Planetary.Finish)
}

// finalize re-renders the winning finish into the canonical output base with the run's full formats.
func (c *planetaryRenderer) finalize(ctx context.Context, _ Options, best *superviseIter,
	records []postprocess.IterationRecord, history []string, _ string) (*postprocess.Result, error) {
	res, err := planetary.Refinish(ctx, c.opts.Runner, c.outDir, c.masters, c.sharpen, best.preset.Planetary.Finish, c.formats, c.outBase)
	if err != nil {
		return nil, err
	}
	return &postprocess.Result{
		Mode:    "planetary",
		Outputs: res.Outputs,
		Notes: append([]string{
			fmt.Sprintf("local AI agent planetary finish: %d iteration(s), best score %.1f", len(records), best.score),
		}, history...),
		Iterations: records,
	}, nil
}

// planetaryPatch is the model's proposed change to the planetary finish (all optional).
type planetaryPatch struct {
	Stretch    *float64 `json:"stretch,omitempty"`
	Highlight  *float64 `json:"highlight,omitempty"`
	Sharpen    *float64 `json:"sharpen,omitempty"`
	Clahe      *float64 `json:"clahe,omitempty"`
	Saturation *float64 `json:"saturation,omitempty"`
}

func clampPlanetaryFinish(f siril.PlanetaryFinish) siril.PlanetaryFinish {
	f.Stretch = clampf(f.Stretch, 0.1, 1.0)
	f.Highlight = clampf(f.Highlight, 0.5, 0.98)
	f.Sharpen = clampf(f.Sharpen, 0, 2.5)
	f.Clahe = clampf(f.Clahe, 0, 4)
	f.Saturation = clampf(f.Saturation, 0, 1.5)
	return f
}

func finishParams(f siril.PlanetaryFinish) map[string]float64 {
	return map[string]float64{
		"stretch": f.Stretch, "highlight": f.Highlight, "sharpen": f.Sharpen,
		"clahe": f.Clahe, "saturation": f.Saturation,
	}
}

func finishState(f siril.PlanetaryFinish) map[string]any {
	return map[string]any{
		"stretch": f.Stretch, "highlight": f.Highlight, "sharpen": f.Sharpen,
		"clahe": f.Clahe, "saturation": f.Saturation,
	}
}

func planetaryObjective() string {
	return "Produce a crisp, natural planetary/lunar image: fine surface detail (craters, cloud bands) " +
		"resolved and sharp without ringing halos or an over-sharpened, crunchy look; bright craters/limb " +
		"not blown to white; natural local contrast; and, for the Moon, subtle real mineral colour (not " +
		"garish). Change one control at a time, only when the image clearly needs it. Set done=true once " +
		"further sharpening or contrast would only add artefacts."
}

const planetaryIntro = `You are an expert planetary/lunar image-processing assistant. You are shown a rendered Moon or planet image (whole frame + a 100% centre crop) plus objective measurements, and you iteratively tune the finish to bring out real surface detail without introducing artefacts.

`

const planetaryKnobMenu = `You may tune these finish controls (each re-renders in seconds):
- stretch (0.1..1.0): overall brightness/contrast stretch. Higher lifts the mid-tones.
- highlight (0.5..0.98): highlight-protection point; higher keeps bright craters/limb from clipping to white.
- sharpen (0..2.5): wavelet detail boost. 1.0 is the default; raise for more crater/band detail, lower if it rings or looks crunchy.
- clahe (0..4): local contrast / surface relief. Higher is punchier; too high looks harsh.
- saturation (0..1.5): colour saturation (the Moon's real mineral colour). 0 = grey; ~0.8 = natural.`

const planetarySystemPrompt = planetaryIntro + planetaryKnobMenu + supervisorDefectRules
