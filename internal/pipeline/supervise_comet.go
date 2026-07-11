// Comet adapter for the supervised finish. The expensive work (calibrate → dual star/comet stack) has
// already run and left the per-channel star_master_*/comet_master_* on disk, so a candidate is a cheap
// re-combine of the colour composite (background level/degree + saturation) via combineCometFinish. No
// re-stack tier — the renderer is single-stage (tierA). See supervise_deepsky.go for the reference shape.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/comet"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/postprocess"
)

// cometRenderer re-finishes a comet run from its persisted colour-channel masters.
type cometRenderer struct {
	opts                      Options
	starMasters, cometMasters map[string]string
	haveTrack                 bool
	pMid                      comet.Point
	outDir                    string
	orig                      mode.Preset // base preset, for a stable objective across iterations
}

// superviseFinishComet re-tunes the comet colour composite under the agent, keeping the best pass.
// Returns the finish result or an error (the caller soft-falls to the standard finishComet).
func superviseFinishComet(ctx context.Context, opts Options, starMasters, cometMasters map[string]string,
	haveTrack bool, pMid comet.Point, outDir string) (*postprocess.Result, error) {
	r := &cometRenderer{
		opts: opts, starMasters: starMasters, cometMasters: cometMasters,
		haveTrack: haveTrack, pMid: pMid, outDir: outDir, orig: *opts.Preset,
	}
	if err := r.ready(ctx); err != nil {
		return nil, err
	}
	return superviseFinish(ctx, opts, r, outDir)
}

func (c *cometRenderer) ready(ctx context.Context) error {
	if c.opts.Runner == nil {
		return fmt.Errorf("siril unavailable")
	}
	return c.opts.Runner.Available(ctx)
}

func (c *cometRenderer) firstTier() tier      { return tierA }
func (c *cometRenderer) maxTier(Options) tier { return tierA }

func (c *cometRenderer) render(ctx context.Context, working mode.Preset, _ tier, outBase string) (renderResult, []string, error) {
	o := c.opts
	o.Preset = &working
	scratch := &Result{}
	final := combineCometFinish(ctx, o, scratch, c.starMasters, c.cometMasters, c.haveTrack, c.pMid, c.outDir, filepath.Base(outBase))
	if final == nil {
		return renderResult{}, scratch.Warnings, fmt.Errorf("comet finish produced no image")
	}
	png, tif := pngTifOf(final.Outputs)
	if png == "" {
		return renderResult{}, scratch.Warnings, fmt.Errorf("comet finish produced no PNG")
	}
	return renderResult{Png: png, Tif: tif}, scratch.Warnings, nil
}

func (c *cometRenderer) prompt(working mode.Preset, _ tier) supervisePrompt {
	return supervisePrompt{
		system:    cometSystemPrompt,
		objective: cometObjective(&c.orig),
		state: map[string]any{
			"background_level":  working.BackgroundLevel,
			"background_degree": working.BackgroundDegree,
			"saturation":        working.Saturation,
		},
		tiered: false,
	}
}

func (c *cometRenderer) applyPatch(working mode.Preset, raw json.RawMessage, affordable tier) (mode.Preset, tier, bool) {
	next, t, changed := applyCometParamPatch(working, raw)
	if t > affordable {
		// Revert the unaffordable re-stack fields so the working preset never drifts from the render.
		next.Grade, next.TrailMaskK, next.CometPerFrameStarnet = working.Grade, working.TrailMaskK, working.CometPerFrameStarnet
		t = tierA
		changed = floatChanged(working.BackgroundLevel, next.BackgroundLevel) ||
			working.BackgroundDegree != next.BackgroundDegree ||
			floatChanged(working.Saturation, next.Saturation)
	}
	return next, t, changed
}

func (c *cometRenderer) params(p mode.Preset) map[string]float64 {
	return map[string]float64{
		"background_level":  p.BackgroundLevel,
		"background_degree": float64(p.BackgroundDegree),
		"saturation":        p.Saturation,
	}
}

// finalize promotes the winning composite to the canonical comet_final.* and assembles the run result.
func (c *cometRenderer) finalize(ctx context.Context, opts Options, best *superviseIter,
	records []postprocess.IterationRecord, history []string, outDir string) (*postprocess.Result, error) {
	finalBase := filepath.Join(outDir, "comet_final")
	if err := promoteResult(best.result, finalBase); err != nil {
		return nil, err
	}
	out := cometResult(outDir, "comet_final",
		fmt.Sprintf("local AI agent comet finish: %d iteration(s), best score %.1f", len(records), best.score))
	out.Notes = append(out.Notes, history...)
	out.Iterations = records
	return out, nil
}

// cometPatch is the model's proposed change to the comet finish: the colour-combine knobs (tierA)
// plus the re-stack knobs (tierC — honoured when a re-stack path is available).
type cometPatch struct {
	BackgroundLevel  *float64 `json:"background_level,omitempty"`
	BackgroundDegree *int     `json:"background_degree,omitempty"`
	Saturation       *float64 `json:"saturation,omitempty"`

	// Tier C — re-stack from the calibrated frames.
	RoundnessFloor  *float64 `json:"roundness_floor,omitempty"`
	FWHMSigma       *float64 `json:"fwhm_sigma,omitempty"`
	BackgroundSigma *float64 `json:"background_sigma,omitempty"`
	StarCountFrac   *float64 `json:"star_count_frac,omitempty"`
	TrailMaskK      *float64 `json:"trail_mask_k,omitempty"`
	PerFrameStarnet *bool    `json:"per_frame_starnet,omitempty"`
}

func clampComet(p mode.Preset) mode.Preset {
	p.BackgroundLevel = clampf(p.BackgroundLevel, 0.03, 0.2)
	p.BackgroundDegree = clampi(p.BackgroundDegree, 1, 4)
	p.Saturation = clampf(p.Saturation, 0, 0.6)
	return p
}

// pngTifOf extracts the .png and .tif paths from a finish result's outputs.
func pngTifOf(outputs []string) (png, tif string) {
	for _, o := range outputs {
		switch filepath.Ext(o) {
		case ".png":
			png = o
		case ".tif":
			tif = o
		}
	}
	return png, tif
}

func cometObjective(p *mode.Preset) string {
	return fmt.Sprintf(
		"Produce a clean, natural comet image: a neutral, dark sky background near %.3f (no green or magenta "+
			"cast), the coma and tail clearly visible without crushing the shadows or blowing the nucleus, "+
			"pinpoint stars, and pleasing but not oversaturated colour. Change parameters in small steps, only "+
			"when the image clearly needs it. Set done=true once further tuning would not clearly help.",
		p.BackgroundLevel)
}

const cometIntro = `You are an expert astrophotography image-processing assistant. You are shown a rendered COMET image — a sharp comet with pinpoint stars (whole frame + a 100% centre crop) — plus objective measurements, and you iteratively tune the colour finish to improve it.

`

const cometKnobMenu = `You may tune these colour-finish controls (each re-renders the comet composite in seconds):
- background_level (0.03..0.2): target sky brightness of the autostretch (lower = darker sky).
- background_degree (1..4): Siril polynomial background-extraction degree (raise to remove stronger gradients).
- saturation (0..0.6): colour saturation of the comet + stars (0 = none).`

const cometSystemPrompt = cometIntro + cometKnobMenu + supervisorDefectRules
