// Milkyway (nightscape) adapter for the supervised finish. The expensive develop→register→clean-sky
// stack has already run and persisted the pre-grade linear sky/foreground + mask to the run dir, so a
// candidate is a cheap colour re-grade (nightscape.Regrade): the model picks the render look and the sky
// brightness. Single-stage (tierA); the same Regrade path also backs a post-run refine of a milkyway run.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/nightscape"
	"github.com/verove-jordan/astronomy/internal/postprocess"
)

// nightscapeRenderer re-grades a milkyway run from the persisted pre-grade linear inputs in srcDir.
type nightscapeRenderer struct {
	srcDir string      // holds lin_sky/lin_fg/sky_alpha/grade.orient (the run output dir)
	outDir string      // run output dir (where the winning final.png is written)
	orient string      // preset orientation (auto|none|cw|ccw|180 …); overridden by the persisted resolve
	orig   mode.Preset // base preset, for a stable objective across iterations
}

// superviseFinishNightscape re-tunes the milkyway grade under the agent, keeping the best pass. Returns
// the finish result or an error (the caller soft-falls to the standard nightscape finish).
func superviseFinishNightscape(ctx context.Context, opts Options, outDir string) (*postprocess.Result, error) {
	r := &nightscapeRenderer{srcDir: outDir, outDir: outDir, orient: opts.Preset.Orientation, orig: *opts.Preset}
	if err := r.ready(ctx); err != nil {
		return nil, err
	}
	return superviseFinish(ctx, opts, r, outDir)
}

func (n *nightscapeRenderer) ready(context.Context) error {
	if !fileExists(filepath.Join(n.srcDir, "lin_sky.fits")) {
		return fmt.Errorf("no persisted linear inputs to re-grade")
	}
	return nil
}

func (n *nightscapeRenderer) firstTier() tier      { return tierA }
func (n *nightscapeRenderer) maxTier(Options) tier { return tierA }

// gradeOpts maps the working preset onto a nightscape grade: the chosen look + the sky-brightness target.
func (n *nightscapeRenderer) gradeOpts(p mode.Preset) nightscape.Options {
	return nightscape.Options{
		Look:                  nightscape.LookByName(p.Look),
		Brightness:            p.BackgroundLevel,
		SaturationScale:       p.Saturation,
		HighlightCeilOverride: p.HighlightCeil,
		Orientation:           n.orient,
	}
}

func (n *nightscapeRenderer) render(ctx context.Context, working mode.Preset, _ tier, outBase string) (renderResult, []string, error) {
	o := n.gradeOpts(working)
	o.OutDir = filepath.Join(n.outDir, "sv", filepath.Base(outBase))
	o.PreviewOnly = true
	nres, err := nightscape.Regrade(ctx, o, n.srcDir)
	if err != nil {
		return renderResult{}, nil, err
	}
	return renderResult{Png: nres.FinalPNG}, nres.Warnings, nil
}

func (n *nightscapeRenderer) prompt(working mode.Preset, _ tier) supervisePrompt {
	return supervisePrompt{
		system:    nightscapeSystemPrompt,
		objective: nightscapeObjective(&n.orig),
		state: map[string]any{
			"look":              working.Look,
			"brightness":        working.BackgroundLevel,
			"saturation_scale":  working.Saturation,
			"highlight_ceiling": working.HighlightCeil,
		},
		tiered: false,
	}
}

func (n *nightscapeRenderer) applyPatch(working mode.Preset, raw json.RawMessage, _ tier) (mode.Preset, tier, bool) {
	return applyNightscapeParamPatch(working, raw)
}

func (n *nightscapeRenderer) params(p mode.Preset) map[string]float64 {
	return map[string]float64{
		"brightness": p.BackgroundLevel, "saturation_scale": p.Saturation, "highlight_ceiling": p.HighlightCeil,
	}
}

// finalize re-renders the winning grade directly into the run output dir (full exports), so final.png +
// the linear FITS are the chosen look, then assembles the standard nightscape run result.
func (n *nightscapeRenderer) finalize(ctx context.Context, _ Options, best *superviseIter,
	records []postprocess.IterationRecord, history []string, outDir string) (*postprocess.Result, error) {
	o := n.gradeOpts(best.preset)
	o.OutDir = outDir
	nres, err := nightscape.Regrade(ctx, o, n.srcDir)
	if err != nil {
		return nil, err
	}
	return &postprocess.Result{
		Mode: "OSC-RGB nightscape", Channels: []string{"RGB"},
		Outputs: []string{nres.FinalPNG, nres.CompositeFITS, nres.SkyFITS, nres.ForegroundFITS},
		Notes: append([]string{
			fmt.Sprintf("local AI agent milkyway finish: %d iteration(s), best score %.1f, %s look", len(records), best.score, o.Look.Name),
		}, history...),
		Iterations: records,
	}, nil
}

// nightscapePatch is the model's proposed change to the milkyway grade (all optional).
type nightscapePatch struct {
	Look       *string  `json:"look,omitempty"`       // natural | iphone | deepsky
	Brightness *float64 `json:"brightness,omitempty"` // → BackgroundLevel target (0.03..0.2)
	// SaturationScale scales the chosen look's own saturation (1 = as designed, 0 = grayscale-ish,
	// up to 2); HighlightCeiling overrides the look's core ceiling (0.3..0.95, lower = dimmer core).
	SaturationScale  *float64 `json:"saturation_scale,omitempty"`
	HighlightCeiling *float64 `json:"highlight_ceiling,omitempty"`
	// KeepMeteors blends the meteors the sigma-clip rejected back into the sky, and leaves the
	// satellites and aircraft out. It re-stacks rather than re-grades, so it is not a finishing knob.
	KeepMeteors *bool `json:"keep_meteors,omitempty"`
	// FlatRadialOnly reduces a master flat to its lens falloff, discarding the reflection a phone
	// flat carries in the middle. Re-stacks rather than re-grades.
	FlatRadialOnly *bool `json:"flat_radial_only,omitempty"`
}

func isLookName(name string) bool {
	for _, l := range nightscape.LookNames() {
		if l == name {
			return true
		}
	}
	return false
}

func nightscapeObjective(p *mode.Preset) string {
	return fmt.Sprintf(
		"Produce a natural, pleasing Milky-Way nightscape: a clean dark sky (background near %.3f) with the "+
			"Milky-Way core bright but not blown out, natural (not neon) colour, an untinted foreground, and no "+
			"strong colour cast or gradient. Pick the look and sky brightness that best fit the scene. Change one "+
			"control at a time, only when the image clearly needs it. Set done=true once further tuning would not clearly help.",
		p.BackgroundLevel)
}

const nightscapeIntro = `You are an expert astrophotography image-processing assistant. You are shown a rendered Milky-Way NIGHTSCAPE — a wide-field sky over a landscape foreground (whole frame + a 100% centre crop) — plus objective measurements, and you iteratively tune the colour grade to improve it.

`

const nightscapeKnobMenu = `You may tune these grade controls (each re-renders in seconds):
- look ("natural" | "iphone" | "deepsky"): the render style. "natural" is faithful and soft; "iphone" is a touch warmer and punchier with a deeper sky; "deepsky" is bold and dramatic with high saturation.
- brightness (0.03..0.2): the target sky-background level (lower = darker sky; higher lifts the Milky-Way core).
- saturation_scale (0..2): scales the look's own colour saturation (1 = as designed; below 1 tames neon colour).
- highlight_ceiling (0.3..0.95): the Milky-Way core's brightness ceiling (lower = dimmer, better-protected core; 0 keeps the look's own).`

// keep_meteors is deliberately NOT offered here. The supervised finish is a re-grade of the persisted
// linear sky and cannot re-stack, so it could not honour the change: the meteors are found in the
// registered frames and added before the grade. It is a job parameter, not a finishing knob.

const nightscapeSystemPrompt = nightscapeIntro + nightscapeKnobMenu + supervisorDefectRules
