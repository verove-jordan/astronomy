// The supervised-finish loop (supervise.go) is mode-agnostic: it renders a candidate, scores it
// deterministically + with the vision model, and keeps the best across iterations. Everything that IS
// mode-specific — how a candidate is rendered, which knobs the model may tune, and how the winner is
// finalized — lives behind candidateRenderer, so each stacking mode (deepsky, comet, milkyway,
// planetary) plugs in its own finish without duplicating the loop.
package pipeline

import (
	"context"
	"encoding/json"

	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/postprocess"
)

// renderResult is one rendered finish candidate: the PNG the model + metrics judge, plus the optional
// TIFF/XCF the winner is promoted from. Png is always set; Tif/Xcf may be empty (modes without layers).
type renderResult struct{ Png, Tif, Xcf string }

// supervisePrompt is the mode-specific context the critique sends to the vision model each iteration.
type supervisePrompt struct {
	system    string         // full system prompt (intro + knob menu + JSON/defect rules)
	objective string         // one-paragraph goal for this mode
	state     map[string]any // current tunable values, for the model to reason about
	maxTier   tier           // highest re-entry tier the model may still reach
	tiered    bool           // tell the model about cost tiers (deepsky) — off for single-stage modes
}

// candidateRenderer is one stacking mode's finish adapter for the supervised loop. The loop drives it:
// render a scored candidate for a tuned preset, describe the tunable knobs to the model, map the model's
// JSON patch back onto the preset, and finalize the winning pass. Deepsky wraps the staged GIMP reentry;
// the other modes re-run only their (cheap) finish stage, so their firstTier/maxTier are tierA.
type candidateRenderer interface {
	// ready reports whether this mode's finish tools are usable (soft precondition for the loop).
	ready(ctx context.Context) error
	// firstTier is the tier of pass 0: deepsky builds its linear prep (tierB) as the baseline; the
	// single-stage modes render their cheap finish (tierA).
	firstTier() tier
	// maxTier is the highest tier the model may reach this run (ceiling + availability).
	maxTier(opts Options) tier
	// render produces a candidate for `working` at tier `t`, returning the result and any prep notes
	// (colour-cal/background) to carry into the finalized run record.
	render(ctx context.Context, working mode.Preset, t tier, outBase string) (renderResult, []string, error)
	// prompt builds the mode-specific critique prompt for the working preset.
	prompt(working mode.Preset, maxTier tier) supervisePrompt
	// applyPatch unmarshals the model's patch, applies + clamps it (capped to `affordable`), and returns
	// the next working preset, the tier its change requires, and whether it effectively changed anything.
	applyPatch(working mode.Preset, patch json.RawMessage, affordable tier) (next mode.Preset, t tier, changed bool)
	// params flattens the tuned knobs into the per-iteration record shown in the supervisor panel.
	params(working mode.Preset) map[string]float64
	// finalize promotes the winning candidate to final.* and assembles the run finish result.
	finalize(ctx context.Context, opts Options, best *superviseIter, records []postprocess.IterationRecord, history []string, outDir string) (*postprocess.Result, error)
}
