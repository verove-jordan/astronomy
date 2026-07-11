// Deepsky/nebula adapter for the supervised finish: the LRGB/Ha layered GIMP composite. It wraps the
// staged reentry (Tier A composite / B linear prep / C re-stack, supervise_reentry.go) and the tiered
// param model (supervise_params.go) behind candidateRenderer, so the shared loop (supervise.go) drives
// it exactly as before. This is the reference adapter; the other modes mirror its shape with their own
// cheap re-finish and knob set.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/postprocess"
)

// deepskyRenderer renders the layered GIMP composite for the supervised finish. orig is the run's base
// preset, kept so the model's objective text stays stable while working knobs change.
type deepskyRenderer struct {
	re   *reentry
	orig mode.Preset
}

func newDeepskyRenderer(opts Options, channels map[string]string, workRun, outDir string) (*deepskyRenderer, error) {
	re, err := newReentry(opts, channels, workRun, outDir)
	if err != nil {
		return nil, err
	}
	return &deepskyRenderer{re: re, orig: *opts.Preset}, nil
}

// superviseFinishDeepsky builds the deepsky renderer and runs the shared supervised-finish loop; called
// by finishAligned when the agent is enabled (soft-falls to the standard finish on error).
func superviseFinishDeepsky(ctx context.Context, opts Options, channels map[string]string, workRun, outDir string) (*postprocess.Result, error) {
	r, err := newDeepskyRenderer(opts, channels, workRun, outDir)
	if err != nil {
		return nil, err
	}
	return superviseFinish(ctx, opts, r, outDir)
}

func (d *deepskyRenderer) ready(context.Context) error {
	if d.re.opts.Gimp == nil {
		return fmt.Errorf("GIMP unavailable")
	}
	return d.re.opts.Gimp.Available()
}

// firstTier: pass 0 builds the linear prep = the standard-finish baseline.
func (d *deepskyRenderer) firstTier() tier { return tierB }

// maxTier is the preset ceiling (SuperviseTier), capped to Tier B when no raw frames are available to
// re-stack (Options.Reprocess is nil).
func (d *deepskyRenderer) maxTier(opts Options) tier {
	ceiling := tierC
	if s := strings.TrimSpace(opts.Preset.SuperviseTier); s != "" {
		ceiling = parseTier(s)
	}
	if opts.Reprocess == nil && ceiling > tierB {
		ceiling = tierB
	}
	return ceiling
}

func (d *deepskyRenderer) render(ctx context.Context, working mode.Preset, t tier, outBase string) (renderResult, []string, error) {
	g, err := d.re.render(ctx, t, working, outBase)
	if err != nil {
		return renderResult{}, nil, err
	}
	return renderResult{Png: g.Png, Tif: g.Tif, Xcf: g.Xcf}, d.re.notes, nil
}

func (d *deepskyRenderer) prompt(working mode.Preset, maxTier tier) supervisePrompt {
	return supervisePrompt{
		system:    supervisorSystemPrompt,
		objective: objectiveText(&d.orig),
		state:     supervisorState(working),
		maxTier:   maxTier,
		tiered:    true,
	}
}

func (d *deepskyRenderer) applyPatch(working mode.Preset, raw json.RawMessage, affordable tier) (mode.Preset, tier, bool) {
	var patch supervisePatch
	if err := json.Unmarshal(raw, &patch); err != nil {
		return working, tierA, false
	}
	next := clampPreset(capToTier(working, patch.apply(working), affordable))
	t := tierOf(working, next)
	// A pure Tier-A patch that changes no composite knob is a no-op we can afford → converged.
	changed := !(t == tierA && !composeChanged(working, next))
	return next, t, changed
}

func (d *deepskyRenderer) params(working mode.Preset) map[string]float64 { return paramsMap(working) }

// finalize promotes the winning iteration to final.*, applies StarNet++ once with the winner's preset,
// and assembles the result record (mode/channels from the reentry's channel map).
func (d *deepskyRenderer) finalize(ctx context.Context, opts Options, best *superviseIter,
	records []postprocess.IterationRecord, history []string, outDir string) (*postprocess.Result, error) {
	finalBase := filepath.Join(outDir, "final")
	if err := promoteResult(best.result, finalBase); err != nil {
		return nil, err
	}
	out := &postprocess.Result{
		Mode:     compMode(d.re.channels, d.re.opts.Preset),
		Channels: filterList(d.re.channels),
		Outputs:  []string{finalBase + ".xcf", finalBase + ".tif", finalBase + ".png"},
		Notes: append([]string{
			fmt.Sprintf("local AI agent finish: %d iteration(s), best score %.1f", len(records), best.score),
		}, best.notes...),
	}

	// StarNet++ star reduction once, on the winning composite, using the winner's (possibly tuned)
	// StarReduce. Same soft-fail semantics as the standard finish.
	star := opts
	star.Preset = &best.preset
	if aiStars(ctx, star) {
		extra, note := reduceStarsAI(ctx, star, finalBase+".tif", outDir, nil)
		out.Outputs = append(out.Outputs, extra...)
		if note != "" {
			out.Notes = append(out.Notes, note)
		} else {
			out.Notes = append(out.Notes, fmt.Sprintf("StarNet++ star reduction (stars at %.0f%%)", best.preset.StarReduce*100))
		}
	}
	out.Notes = append(out.Notes, history...)
	out.Iterations = records
	return out, nil
}

// objectiveText is the deepsky finish goal sent to the model (stable across iterations).
func objectiveText(p *mode.Preset) string {
	return fmt.Sprintf(
		"Produce a clean, natural %s image: a neutral sky background near %.3f with NO colour cast — not green, "+
			"not magenta, and not warm/orange; shadows not crushed to pure black; natural, VARIED star colours "+
			"(white/blue/yellow/orange as appropriate) — never a uniform orange tint and no pink or violet star "+
			"fringing; bright star cores rolled just below pure white so they keep colour instead of burning out "+
			"(a diffuse nebula core may stay bright); pleasing but not oversaturated colour; tight round stars; and "+
			"no gradients or trail residue. Change parameters in small steps, only when the image clearly needs it, "+
			"and prefer the cheapest tier that can fix the defect. Set done=true once further tuning would not clearly help.",
		p.Mode, p.BackgroundLevel)
}

// supervisorState is the compact current-parameter view sent to the model, grouped by tier so the model
// can reason about the cost of each knob it might change.
func supervisorState(p mode.Preset) map[string]any {
	return map[string]any{
		"tierA": map[string]any{
			"saturation": p.Saturation, "ha_screen": p.HaScreen, "ha_black_point": p.HaBlackPoint,
			"chroma_blur": p.ChromaBlur, "crop_frac": p.CropFrac,
			"core_highlight_knee": p.CoreHighlightKnee, "core_highlight_ceil": p.CoreHighlightCeil,
			"highlight_knee": p.HighlightKnee, "highlight_ceil": p.HighlightCeil,
			"star_desat":       p.StarDesat,
			"ha_exclude_stars": p.HaExcludeStars,
		},
		"tierB": map[string]any{
			"background_level": p.BackgroundLevel, "linked_stretch": p.LinkedStretch,
			"color_calibration": p.ColorCalibration, "combined_background_ai": p.CombinedBackgroundAI,
			"background_degree": p.BackgroundDegree, "color_denoise_ai": p.ColorDenoiseAI,
			"star_reduce": p.StarReduce, "stretch_headroom": p.StretchHeadroom,
		},
		"tierC": map[string]any{
			"roundness_floor": p.Grade.RoundnessFloor, "fwhm_sigma": p.Grade.FWHMSigma,
			"background_sigma": p.Grade.BackgroundSigma, "star_count_frac": p.Grade.StarCountFrac,
			"trail_mask_k": p.TrailMaskK, "denoise_chroma": p.DenoiseChroma,
			"denoise_lum": p.DenoiseLum, "background_ai": p.BackgroundAI,
		},
	}
}
