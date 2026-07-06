// The shared "param brain": one mode-dispatched entry point that applies a JSON parameter patch to a
// preset with the SAME whitelists and clamps the in-run supervisor uses — so the supervisor, the API
// (RunRequest.Params) and the AstroAgent chat tools cannot drift apart on what is tunable or safe.
package pipeline

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/verove-jordan/astronomy/internal/mode"
)

// ParamPatchResult reports what a patch did: which knobs changed, which JSON keys were not part of
// the mode's tunable surface (ignored, never an error — the model/user learns from the report), and
// the highest re-entry tier the change requires ("A"/"B"/"C").
type ParamPatchResult struct {
	Changed []string `json:"changed,omitempty"`
	Ignored []string `json:"ignored,omitempty"`
	Tier    string   `json:"tier"`
}

// ApplyParamPatch applies a JSON object of tunable-knob overrides onto p in place, clamped to the
// mode's known-good ranges. Unknown keys are reported, not fatal; a malformed JSON body errors.
func ApplyParamPatch(p *mode.Preset, raw json.RawMessage) (ParamPatchResult, error) {
	if len(raw) == 0 {
		return ParamPatchResult{Tier: tierA.String()}, nil
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return ParamPatchResult{}, fmt.Errorf("params must be a JSON object of knob overrides: %w", err)
	}
	known := knownParamKeys(p.Mode)
	var ignored []string
	for k := range keys {
		if !known[k] {
			ignored = append(ignored, k)
		}
	}
	sort.Strings(ignored)

	before := ParamsFor(*p)
	next, t, _ := applyModeParamPatch(*p, raw)
	*p = next
	after := ParamsFor(*p)

	var changed []string
	for k, v := range after {
		if fmt.Sprint(before[k]) != fmt.Sprint(v) {
			changed = append(changed, k)
		}
	}
	sort.Strings(changed)
	return ParamPatchResult{Changed: changed, Ignored: ignored, Tier: t.String()}, nil
}

// applyModeParamPatch dispatches to the mode's patch model at the unrestricted (tierC) affordability —
// callers that must cap affordability (the supervised loop) go through the renderers instead.
func applyModeParamPatch(working mode.Preset, raw json.RawMessage) (mode.Preset, tier, bool) {
	switch working.Mode {
	case mode.Comet:
		return applyCometParamPatch(working, raw)
	case mode.Milkyway:
		return applyNightscapeParamPatch(working, raw)
	case mode.Planetary:
		return applyPlanetaryParamPatch(working, raw)
	default: // deepsky / nebula / livestack share the full tiered whitelist
		return applyDeepskyParamPatch(working, raw)
	}
}

// applyDeepskyParamPatch is the deepsky/nebula patch model (the supervisor's full tiered whitelist).
func applyDeepskyParamPatch(working mode.Preset, raw json.RawMessage) (mode.Preset, tier, bool) {
	var patch supervisePatch
	if err := json.Unmarshal(raw, &patch); err != nil {
		return working, tierA, false
	}
	next := clampPreset(patch.apply(working))
	t := tierOf(working, next)
	changed := t > tierA || composeChanged(working, next)
	return next, t, changed
}

// applyCometParamPatch is the comet patch model: the colour-finish knobs (tierA re-combine) plus the
// re-stack knobs (grade thresholds / trail mask — tierC, honoured when a re-stack path is available).
func applyCometParamPatch(working mode.Preset, raw json.RawMessage) (mode.Preset, tier, bool) {
	var patch cometPatch
	if err := json.Unmarshal(raw, &patch); err != nil {
		return working, tierA, false
	}
	next := working
	setF(&next.BackgroundLevel, patch.BackgroundLevel)
	setI(&next.BackgroundDegree, patch.BackgroundDegree)
	setF(&next.Saturation, patch.Saturation)
	setF(&next.Grade.RoundnessFloor, patch.RoundnessFloor)
	setF(&next.Grade.FWHMSigma, patch.FWHMSigma)
	setF(&next.Grade.BackgroundSigma, patch.BackgroundSigma)
	setF(&next.Grade.StarCountFrac, patch.StarCountFrac)
	setF(&next.TrailMaskK, patch.TrailMaskK)
	setB(&next.CometPerFrameStarnet, patch.PerFrameStarnet)
	next = clampComet(next)
	next.Grade.RoundnessFloor = clampf(next.Grade.RoundnessFloor, 0.2, 0.95)
	next.Grade.FWHMSigma = clampf(next.Grade.FWHMSigma, 1, 5)
	next.Grade.BackgroundSigma = clampf(next.Grade.BackgroundSigma, 1, 5)
	next.Grade.StarCountFrac = clampf(next.Grade.StarCountFrac, 0.1, 1)
	next.TrailMaskK = clampf(next.TrailMaskK, 0, 6)

	t := tierA
	if gradeChanged(working.Grade, next.Grade) || floatChanged(working.TrailMaskK, next.TrailMaskK) ||
		working.CometPerFrameStarnet != next.CometPerFrameStarnet {
		t = tierC
	}
	changed := t == tierC ||
		floatChanged(working.BackgroundLevel, next.BackgroundLevel) ||
		working.BackgroundDegree != next.BackgroundDegree ||
		floatChanged(working.Saturation, next.Saturation)
	return next, t, changed
}

// applyNightscapeParamPatch is the milkyway patch model: the grade knobs, all tierA (a re-grade of
// the persisted linear inputs takes seconds).
func applyNightscapeParamPatch(working mode.Preset, raw json.RawMessage) (mode.Preset, tier, bool) {
	var patch nightscapePatch
	if err := json.Unmarshal(raw, &patch); err != nil {
		return working, tierA, false
	}
	next := working
	if patch.Look != nil {
		if l := strings.ToLower(strings.TrimSpace(*patch.Look)); isLookName(l) {
			next.Look = l
		}
	}
	setF(&next.BackgroundLevel, patch.Brightness)
	next.BackgroundLevel = clampf(next.BackgroundLevel, 0.03, 0.2)
	setF(&next.Saturation, patch.SaturationScale)
	next.Saturation = clampf(next.Saturation, 0, 2) // a SCALE on the look's own saturation (1 = as designed)
	setF(&next.HighlightCeil, patch.HighlightCeiling)
	if next.HighlightCeil != 0 {
		next.HighlightCeil = clampf(next.HighlightCeil, 0.3, 0.95)
	}
	changed := next.Look != working.Look ||
		floatChanged(next.BackgroundLevel, working.BackgroundLevel) ||
		floatChanged(next.Saturation, working.Saturation) ||
		floatChanged(next.HighlightCeil, working.HighlightCeil)
	return next, tierA, changed
}

// applyPlanetaryParamPatch is the planetary patch model: the finish knobs (tierA Refinish) plus the
// stack/deconvolution knobs (tierC, honoured when a re-stack path is available).
func applyPlanetaryParamPatch(working mode.Preset, raw json.RawMessage) (mode.Preset, tier, bool) {
	var patch planetaryPatch
	if err := json.Unmarshal(raw, &patch); err != nil {
		return working, tierA, false
	}
	next := working
	f := next.Planetary.Finish
	setF(&f.Stretch, patch.Stretch)
	setF(&f.Highlight, patch.Highlight)
	setF(&f.Sharpen, patch.Sharpen)
	setF(&f.Clahe, patch.Clahe)
	setF(&f.Saturation, patch.Saturation)
	next.Planetary.Finish = clampPlanetaryFinish(f)

	setI(&next.Planetary.BestPercent, patch.BestPercent)
	next.Planetary.BestPercent = clampi(next.Planetary.BestPercent, 5, 90)
	setB(&next.Planetary.APAlign, patch.APAlign)
	setF(&next.Planetary.DeconvFWHM, patch.DeconvFWHM)
	if next.Planetary.DeconvFWHM != 0 {
		next.Planetary.DeconvFWHM = clampf(next.Planetary.DeconvFWHM, 1, 6)
	}
	setI(&next.Planetary.DeconvIters, patch.DeconvIters)
	if next.Planetary.DeconvIters != 0 {
		next.Planetary.DeconvIters = clampi(next.Planetary.DeconvIters, 5, 40)
	}
	setF(&next.Planetary.DeconvAlpha, patch.DeconvAlpha)
	if next.Planetary.DeconvAlpha != 0 {
		next.Planetary.DeconvAlpha = clampf(next.Planetary.DeconvAlpha, 300, 5000)
	}

	t := tierA
	if working.Planetary.BestPercent != next.Planetary.BestPercent ||
		working.Planetary.APAlign != next.Planetary.APAlign ||
		floatChanged(working.Planetary.DeconvFWHM, next.Planetary.DeconvFWHM) ||
		working.Planetary.DeconvIters != next.Planetary.DeconvIters ||
		floatChanged(working.Planetary.DeconvAlpha, next.Planetary.DeconvAlpha) {
		t = tierC
	}
	changed := t == tierC || working.Planetary.Finish != next.Planetary.Finish
	return next, t, changed
}

// ParamsFor flattens a preset's full tunable surface for its mode — the values behind Changed
// detection, the iteration-history diffs, and the get_mode_params agent tool.
func ParamsFor(p mode.Preset) map[string]any {
	switch p.Mode {
	case mode.Comet:
		return map[string]any{
			"background_level": p.BackgroundLevel, "background_degree": p.BackgroundDegree,
			"saturation": p.Saturation, "roundness_floor": p.Grade.RoundnessFloor,
			"fwhm_sigma": p.Grade.FWHMSigma, "background_sigma": p.Grade.BackgroundSigma,
			"star_count_frac": p.Grade.StarCountFrac, "trail_mask_k": p.TrailMaskK,
			"per_frame_starnet": p.CometPerFrameStarnet,
		}
	case mode.Milkyway:
		return map[string]any{
			"look": p.Look, "brightness": p.BackgroundLevel,
			"saturation_scale": p.Saturation, "highlight_ceiling": p.HighlightCeil,
		}
	case mode.Planetary:
		f := p.Planetary.Finish
		return map[string]any{
			"stretch": f.Stretch, "highlight": f.Highlight, "sharpen": f.Sharpen,
			"clahe": f.Clahe, "saturation": f.Saturation,
			"best_percent": p.Planetary.BestPercent, "ap_align": p.Planetary.APAlign,
			"deconv_fwhm": p.Planetary.DeconvFWHM, "deconv_iters": p.Planetary.DeconvIters,
			"deconv_alpha": p.Planetary.DeconvAlpha,
		}
	default:
		return map[string]any{
			"saturation": p.Saturation, "ha_screen": p.HaScreen, "ha_black_point": p.HaBlackPoint,
			"chroma_blur": p.ChromaBlur, "crop_frac": p.CropFrac,
			"core_highlight_knee": p.CoreHighlightKnee, "core_highlight_ceil": p.CoreHighlightCeil,
			"highlight_knee": p.HighlightKnee, "highlight_ceil": p.HighlightCeil,
			"ha_exclude_stars": p.HaExcludeStars,
			"background_level": p.BackgroundLevel, "linked_stretch": p.LinkedStretch,
			"color_calibration": p.ColorCalibration, "combined_background_ai": p.CombinedBackgroundAI,
			"background_degree": p.BackgroundDegree, "color_denoise_ai": p.ColorDenoiseAI,
			"star_reduce":     p.StarReduce,
			"roundness_floor": p.Grade.RoundnessFloor, "fwhm_sigma": p.Grade.FWHMSigma,
			"background_sigma": p.Grade.BackgroundSigma, "star_count_frac": p.Grade.StarCountFrac,
			"trail_mask_k": p.TrailMaskK, "denoise_chroma": p.DenoiseChroma, "denoise_lum": p.DenoiseLum,
			"background_ai": p.BackgroundAI,
		}
	}
}

// knownParamKeys is each mode's tunable-key set, derived from the patch structs' json tags so the
// validation surface can never drift from what apply actually reads.
func knownParamKeys(m mode.Mode) map[string]bool {
	var t reflect.Type
	switch m {
	case mode.Comet:
		t = reflect.TypeOf(cometPatch{})
	case mode.Milkyway:
		t = reflect.TypeOf(nightscapePatch{})
	case mode.Planetary:
		t = reflect.TypeOf(planetaryPatch{})
	default:
		t = reflect.TypeOf(supervisePatch{})
	}
	keys := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if name, _, _ := strings.Cut(tag, ","); name != "" && name != "-" {
			keys[name] = true
		}
	}
	return keys
}

// KnobMenuFor returns the mode's human/model-readable knob menu (the same text the supervisor's
// critique prompt embeds), for the get_mode_params agent tool.
func KnobMenuFor(m mode.Mode) string {
	switch m {
	case mode.Comet:
		return cometKnobMenu
	case mode.Milkyway:
		return nightscapeKnobMenu
	case mode.Planetary:
		return planetaryKnobMenu
	default:
		return tierKnobMenu
	}
}
