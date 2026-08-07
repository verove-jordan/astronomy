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
	"github.com/verove-jordan/astronomy/internal/planetary"
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
	if err := validateStackPatch(p.Mode, raw); err != nil {
		return ParamPatchResult{}, err
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
	case mode.Mosaic:
		return applyMosaicParamPatch(working, raw)
	case mode.Sun:
		return applySunParamPatch(working, raw)
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
	next = applyStackPatchRaw(next, raw)
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
	next = applyStackPatchRaw(next, raw)
	next = clampComet(next)
	next.Grade.RoundnessFloor = clampf(next.Grade.RoundnessFloor, 0.2, 0.95)
	next.Grade.FWHMSigma = clampf(next.Grade.FWHMSigma, 1, 5)
	next.Grade.BackgroundSigma = clampf(next.Grade.BackgroundSigma, 1, 5)
	next.Grade.StarCountFrac = clampf(next.Grade.StarCountFrac, 0.1, 1)
	next.TrailMaskK = clampf(next.TrailMaskK, 0, 6)

	t := tierA
	if gradeChanged(working.Grade, next.Grade) || floatChanged(working.TrailMaskK, next.TrailMaskK) ||
		working.CometPerFrameStarnet != next.CometPerFrameStarnet || stackChanged(working, next) {
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
	setF(&f.ShadowLift, patch.ShadowLift)
	setF(&f.Sharpen, patch.Sharpen)
	setF(&f.Clahe, patch.Clahe)
	setF(&f.Saturation, patch.Saturation)
	setF(&f.Headroom, patch.Headroom)
	setF(&f.LimbBalance, patch.LimbBalance)
	setF(&f.EarthshineGain, patch.EarthshineGain)
	setF(&f.EarthshineFeather, patch.EarthshineFeather)
	setB(&f.TrueLum, patch.TrueLum)
	next.Planetary.Finish = clampPlanetaryFinish(f)

	setI(&next.Planetary.BestPercent, patch.BestPercent)
	next.Planetary.BestPercent = clampi(next.Planetary.BestPercent, 5, 90)
	setB(&next.Planetary.APAlign, patch.APAlign)
	setB(&next.Planetary.DoubleStack, patch.DoubleStack)
	setB(&next.Planetary.Calibrate, patch.Calibrate)
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
	setF(&next.Planetary.DrizzleScale, patch.DrizzleScale)
	if next.Planetary.DrizzleScale != 0 {
		next.Planetary.DrizzleScale = planetary.SnapDrizzle(next.Planetary.DrizzleScale)
	}
	setI(&next.Planetary.AlignPoints, patch.AlignPoints)
	next.Planetary.AlignPoints = planetary.SnapAlignPoints(next.Planetary.AlignPoints)

	t := tierA
	if working.Planetary.BestPercent != next.Planetary.BestPercent ||
		working.Planetary.APAlign != next.Planetary.APAlign ||
		working.Planetary.DoubleStack != next.Planetary.DoubleStack ||
		working.Planetary.Calibrate != next.Planetary.Calibrate ||
		floatChanged(working.Planetary.DeconvFWHM, next.Planetary.DeconvFWHM) ||
		working.Planetary.DeconvIters != next.Planetary.DeconvIters ||
		floatChanged(working.Planetary.DeconvAlpha, next.Planetary.DeconvAlpha) ||
		floatChanged(working.Planetary.DrizzleScale, next.Planetary.DrizzleScale) ||
		working.Planetary.AlignPoints != next.Planetary.AlignPoints {
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
		m := map[string]any{
			"background_level": p.BackgroundLevel, "background_degree": p.BackgroundDegree,
			"saturation": p.Saturation, "roundness_floor": p.Grade.RoundnessFloor,
			"fwhm_sigma": p.Grade.FWHMSigma, "background_sigma": p.Grade.BackgroundSigma,
			"star_count_frac": p.Grade.StarCountFrac, "trail_mask_k": p.TrailMaskK,
			"per_frame_starnet": p.CometPerFrameStarnet,
		}
		mergeParams(m, stackParams(p), masterStackParams(p), cometStackParams(p))
		return m
	case mode.Milkyway:
		return map[string]any{
			"look": p.Look, "brightness": p.BackgroundLevel,
			"saturation_scale": p.Saturation, "highlight_ceiling": p.HighlightCeil,
		}
	case mode.Planetary:
		f := p.Planetary.Finish
		return map[string]any{
			"stretch": f.Stretch, "highlight": f.Highlight, "shadow_lift": f.ShadowLift,
			"sharpen": f.Sharpen,
			"clahe":   f.Clahe, "saturation": f.Saturation, "headroom": f.Headroom,
			"limb_balance":    f.LimbBalance,
			"earthshine_gain": f.EarthshineGain, "earthshine_feather": f.EarthshineFeather,
			"true_lum":     f.TrueLum,
			"best_percent": p.Planetary.BestPercent, "ap_align": p.Planetary.APAlign,
			"double_stack": p.Planetary.DoubleStack,
			"calibrate":    p.Planetary.Calibrate,
			"deconv_fwhm":  p.Planetary.DeconvFWHM, "deconv_iters": p.Planetary.DeconvIters,
			"deconv_alpha":  p.Planetary.DeconvAlpha,
			"drizzle_scale": p.Planetary.DrizzleScale,
			"align_points":  p.Planetary.AlignPoints,
		}
	case mode.Sun:
		s, f := p.Sun, p.Sun.Finish
		return map[string]any{
			"flat_strength": f.FlatStrength, "deconv_sigma": f.DeconvSigma, "deconv_iters": f.DeconvIters,
			"deconv_auto":   f.DeconvAuto,
			"sharpen_small": sharpenGroup(f.Sharpen.Gains, 0), "sharpen_medium": sharpenGroup(f.Sharpen.Gains, 1),
			"sharpen_large": sharpenGroup(f.Sharpen.Gains, 2), "sharpen_denoise": sharpenDenoise(f.Sharpen.Thresholds),
			"limb_flatten": f.LimbFlatten, "prominence_boost": f.ProminenceBoost,
			"prominence_feather": f.ProminenceFeather, "palette": f.Palette,
			"stretch": f.Stretch, "contrast": f.Contrast, "saturation": f.Saturation,
			"background_level": f.BackgroundLevel, "background_tint": f.BackgroundTint,
			"glow_strength": f.GlowStrength, "glow_radius": f.GlowRadius,
			"keep_percent": s.KeepPercent, "max_frames": s.MaxFrames, "drizzle": s.Drizzle,
			"clip_sigma": s.ClipSigma, "window_seconds": s.WindowSeconds, "window_frames": s.WindowFrames,
			"min_frames": s.MinFrames, "crop_margin": s.CropMargin,
			"scale_tolerance": s.ScaleTolerance, "band": string(s.Band),
			"rescale_groups": s.RescaleGroups, "bracket_merge": s.BracketMerge,
			"ap_align": s.APAlign, "ap_scale": s.APScale,
			"bracket_stops": s.BracketStops, "transparency_floor": s.TransparencyFloor,
		}
	case mode.Mosaic:
		m := deepskyParams(p)
		m["overlap_expected"] = p.MosaicOverlapExpected
		m["feather_frac"] = p.MosaicFeatherFrac
		m["photom_match"] = p.MosaicPhotomMatch
		m["canvas_crop"] = p.MosaicCanvasCrop
		m["min_panel_frames"] = p.MosaicMinPanelFrames
		m["panel_source"] = p.MosaicPanelSource
		return m
	default:
		return deepskyParams(p)
	}
}

// deepskyParams is the deepsky-family tunable surface (deepsky/nebula/livestack; the mosaic mode
// extends it with the assembler knobs).
func deepskyParams(p mode.Preset) map[string]any {
	m := map[string]any{
		"saturation": p.Saturation, "ha_screen": p.HaScreen, "ha_black_point": p.HaBlackPoint,
		"oiii_screen": p.OIIIScreen, "oiii_black_point": p.OIIIBlackPoint,
		"sii_screen": p.SIIScreen, "sii_black_point": p.SIIBlackPoint, "sii_tint": p.SIITint,
		"lum_opacity": p.LumOpacity, "lum_boost": p.LumBoost,
		"chroma_blur": p.ChromaBlur, "crop_frac": p.CropFrac,
		"core_highlight_knee": p.CoreHighlightKnee, "core_highlight_ceil": p.CoreHighlightCeil,
		"highlight_knee": p.HighlightKnee, "highlight_ceil": p.HighlightCeil,
		"star_desat":       p.StarDesat,
		"ha_exclude_stars": p.HaExcludeStars,
		"ha_continuum_sub": p.HaContinuumSub,
		"background_level": p.BackgroundLevel, "linked_stretch": p.LinkedStretch,
		"color_calibration": p.ColorCalibration, "combined_background_ai": p.CombinedBackgroundAI,
		"background_degree": p.BackgroundDegree, "color_denoise_ai": p.ColorDenoiseAI,
		"chroma_smooth_px": p.ChromaSmoothPx, "chroma_bg_smooth_px": p.ChromaBgSmoothPx,
		"sky_chroma_flatten_px": p.SkyChromaFlattenPx,
		"sky_lum_flatten_px":    p.SkyLumFlattenPx,
		"star_reduce":           p.StarReduce, "stretch_headroom": p.StretchHeadroom,
		"emit_luminance_mono":   p.EmitLuminanceMono,
		"emit_all_channel_mono": p.EmitAllChannelMono,
		"palette":               p.Palette,
		"roundness_floor":       p.Grade.RoundnessFloor, "fwhm_sigma": p.Grade.FWHMSigma,
		"background_sigma": p.Grade.BackgroundSigma, "star_count_frac": p.Grade.StarCountFrac,
		"trail_mask_k": p.TrailMaskK, "denoise_chroma": p.DenoiseChroma, "denoise_lum": p.DenoiseLum,
		"background_ai":     p.BackgroundAI,
		"seam_offset_refit": p.SeamOffsetRefit, "seam_noise_eq": p.SeamNoiseEq,
		"union_canvas": p.Mosaic, "union_canvas_fill": p.MosaicFill,
	}
	mergeParams(m, stackParams(p), masterStackParams(p))
	return m
}

// mergeParams folds extra knob maps into base (later maps win). It keeps the per-mode surfaces
// composable: the stacking keys are declared once and spread onto every mode that offers them.
func mergeParams(base map[string]any, extras ...map[string]any) {
	for _, e := range extras {
		for k, v := range e {
			base[k] = v
		}
	}
}

// consentParamKeys lists per-mode knobs that are user opt-ins: the cross-run warm start must never
// resurrect them on a run where the user left them off (the supervisor may only tune, never enable).
func consentParamKeys(m mode.Mode) map[string]bool {
	if m == mode.Planetary {
		return map[string]bool{"earthshine_gain": true}
	}
	// The union canvas reshapes the whole output — a warm-started rerun must never resurrect it.
	// Both the current wire key and its legacy alias are consent-gated. So is the native stacking
	// engine: choosing a non-Siril combiner is a deliberate, per-run decision.
	return map[string]bool{"union_canvas": true, "mosaic": true, "stack_engine": true}
}

// knownParamKeys is each mode's tunable-key set, derived from the patch structs' json tags so the
// validation surface can never drift from what apply actually reads.
func knownParamKeys(m mode.Mode) map[string]bool {
	var types []reflect.Type
	switch m {
	case mode.Comet:
		types = []reflect.Type{reflect.TypeOf(cometPatch{})}
	case mode.Milkyway:
		types = []reflect.Type{reflect.TypeOf(nightscapePatch{})}
	case mode.Planetary:
		types = []reflect.Type{reflect.TypeOf(planetaryPatch{})}
	case mode.Sun:
		types = []reflect.Type{reflect.TypeOf(sunPatch{})}
	case mode.Mosaic: // the full deepsky surface plus the assembler keys
		types = []reflect.Type{reflect.TypeOf(supervisePatch{}), reflect.TypeOf(mosaicPatch{})}
	default:
		types = []reflect.Type{reflect.TypeOf(supervisePatch{})}
	}
	// The Siril-backed modes additionally accept the stacking panel's keys; planetary/sun/milkyway
	// stack natively with their own knobs and must NOT advertise them.
	switch m {
	case mode.Planetary, mode.Sun, mode.Milkyway:
	default:
		types = append(types, reflect.TypeOf(stackPatch{}))
	}
	keys := map[string]bool{}
	for _, t := range types {
		for i := 0; i < t.NumField(); i++ {
			tag := t.Field(i).Tag.Get("json")
			if name, _, _ := strings.Cut(tag, ","); name != "" && name != "-" {
				keys[name] = true
			}
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
	case mode.Sun:
		return sunKnobMenu
	default:
		return tierKnobMenu
	}
}

// KnobRange is the min/max the UI shows beside a tunable knob (its clamp bounds) plus whether the knob
// is integer-valued. Boolean and enum knobs (ap_align, palette, look, *_stars, …) have no meaningful
// range and are omitted — the glossary shows only their default for those. These bounds MIRROR the
// clampPreset / clampPlanetaryFinish / clampComet / applyNightscapeParamPatch clamps; a knob whose 0 is
// an "off"/"auto" value outside [Min,Max] carries that note in its description, not here.
// TestKnobRangesFor_MatchClamps re-derives every bound from the real clamp so the two can never drift.
type KnobRange struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
	Int bool    `json:"int,omitempty"`
}

// KnobRangesFor returns the clamp bounds for a mode's numeric tunable knobs, keyed exactly like
// ParamsFor so the UI can line each range up with its default. Non-numeric knobs are absent by design.
func KnobRangesFor(m mode.Mode) map[string]KnobRange {
	switch m {
	case mode.Comet:
		r := map[string]KnobRange{
			"background_level":  {Min: 0.03, Max: 0.2},
			"background_degree": {Min: 1, Max: 4, Int: true},
			"saturation":        {Min: 0, Max: 0.6},
			"roundness_floor":   {Min: 0.2, Max: 0.95},
			"fwhm_sigma":        {Min: 1, Max: 5},
			"background_sigma":  {Min: 1, Max: 5},
			"star_count_frac":   {Min: 0.1, Max: 1},
			"trail_mask_k":      {Min: 0, Max: 6},
		}
		mergeRanges(r, stackKnobRanges(), masterStackKnobRanges(), cometStackKnobRanges())
		return r
	case mode.Sun:
		return map[string]KnobRange{
			"flat_strength":      {Min: 0, Max: 1},
			"deconv_sigma":       {Min: 0, Max: 5},
			"deconv_iters":       {Min: 0, Max: 80, Int: true},
			"transparency_floor": {Min: 0, Max: 1},
			"bracket_stops":      {Min: 0, Max: 6},
			"ap_scale":           {Min: 0, Max: 8, Int: true},
			"sharpen_small":      {Min: 0, Max: 4},
			"sharpen_medium":     {Min: 0, Max: 4},
			"sharpen_large":      {Min: 0, Max: 4},
			"sharpen_denoise":    {Min: 0, Max: 1},
			"limb_flatten":       {Min: 0, Max: 1},
			"prominence_boost":   {Min: 0, Max: 4},
			"prominence_feather": {Min: 0, Max: 0.05},
			"stretch":            {Min: 0, Max: 1},
			"contrast":           {Min: 0.2, Max: 3},
			"saturation":         {Min: 0, Max: 2},
			"background_level":   {Min: 0, Max: 0.3},
			"background_tint":    {Min: 0, Max: 1},
			"glow_strength":      {Min: 0, Max: 1},
			"glow_radius":        {Min: 0, Max: 0.3},
			"keep_percent":       {Min: 5, Max: 100, Int: true},
			"max_frames":         {Min: 8, Max: 2000, Int: true},
			"drizzle":            {Min: 1, Max: 2},
			"clip_sigma":         {Min: 1, Max: 6},
			"window_seconds":     {Min: 5, Max: 3600},
			"window_frames":      {Min: 8, Max: 2000, Int: true},
			"min_frames":         {Min: 2, Max: 500, Int: true},
			"crop_margin":        {Min: 0.02, Max: 1},
			"scale_tolerance":    {Min: 0.002, Max: 0.2},
		}
	case mode.Milkyway:
		return map[string]KnobRange{
			"brightness":        {Min: 0.03, Max: 0.2},
			"saturation_scale":  {Min: 0, Max: 2},
			"highlight_ceiling": {Min: 0.3, Max: 0.95},
		}
	case mode.Planetary:
		return map[string]KnobRange{
			"stretch":            {Min: 0.1, Max: 1.0},
			"highlight":          {Min: 0.5, Max: 0.98},
			"shadow_lift":        {Min: 0, Max: 1},
			"sharpen":            {Min: 0, Max: 2.5},
			"clahe":              {Min: 0, Max: 4},
			"saturation":         {Min: 0, Max: 1.5},
			"headroom":           {Min: 0, Max: 1},
			"limb_balance":       {Min: 0, Max: 1},
			"earthshine_gain":    {Min: 0.2, Max: 2},
			"earthshine_feather": {Min: 0.002, Max: 0.02},
			"best_percent":       {Min: 5, Max: 90, Int: true},
			"deconv_fwhm":        {Min: 1, Max: 6},
			"deconv_iters":       {Min: 5, Max: 40, Int: true},
			"deconv_alpha":       {Min: 300, Max: 5000},
			"drizzle_scale":      {Min: 1, Max: 2},
			"align_points":       {Min: 100, Max: 2304, Int: true},
		}
	case mode.Mosaic:
		r := deepskyKnobRanges()
		r["overlap_expected"] = KnobRange{Min: 0.05, Max: 0.5}
		r["feather_frac"] = KnobRange{Min: 0.1, Max: 1}
		r["min_panel_frames"] = KnobRange{Min: 1, Max: 50, Int: true}
		return r
	default: // deepsky / nebula / livestack share the full tiered surface
		return deepskyKnobRanges()
	}
}

// deepskyKnobRanges is the deepsky-family clamp table (shared by the mosaic mode).
func deepskyKnobRanges() map[string]KnobRange {
	r := map[string]KnobRange{
		"saturation":            {Min: 0, Max: 0.35},
		"ha_screen":             {Min: 0, Max: 0.8},
		"ha_black_point":        {Min: 0, Max: 0.3},
		"oiii_screen":           {Min: 0, Max: 0.8},
		"oiii_black_point":      {Min: 0, Max: 0.3},
		"sii_screen":            {Min: 0, Max: 0.8},
		"sii_black_point":       {Min: 0, Max: 0.3},
		"lum_opacity":           {Min: 0, Max: 1},
		"lum_boost":             {Min: 0, Max: 0.25},
		"chroma_blur":           {Min: 0, Max: 12},
		"crop_frac":             {Min: 0, Max: 0.1},
		"core_highlight_knee":   {Min: 0, Max: 0.95},
		"core_highlight_ceil":   {Min: 0, Max: 0.99},
		"highlight_knee":        {Min: 0, Max: 0.98},
		"highlight_ceil":        {Min: 0, Max: 0.995},
		"star_desat":            {Min: 0, Max: 1},
		"background_level":      {Min: 0.03, Max: 0.2},
		"background_degree":     {Min: 1, Max: 4, Int: true},
		"chroma_smooth_px":      {Min: 0, Max: 16, Int: true},
		"chroma_bg_smooth_px":   {Min: 0, Max: 64, Int: true},
		"sky_chroma_flatten_px": {Min: 0, Max: 128, Int: true},
		"sky_lum_flatten_px":    {Min: 0, Max: 256, Int: true},
		"star_reduce":           {Min: 0, Max: 1},
		"stretch_headroom":      {Min: 0.7, Max: 1.0},
		"roundness_floor":       {Min: 0.2, Max: 0.95},
		"fwhm_sigma":            {Min: 1, Max: 5},
		"background_sigma":      {Min: 1, Max: 5},
		"star_count_frac":       {Min: 0.1, Max: 1},
		"trail_mask_k":          {Min: 0, Max: 6},
		"denoise_chroma":        {Min: 0, Max: 1},
		"denoise_lum":           {Min: 0, Max: 1},
	}
	mergeRanges(r, stackKnobRanges(), masterStackKnobRanges())
	return r
}

// mergeRanges folds extra clamp tables into base, mirroring mergeParams.
func mergeRanges(base map[string]KnobRange, extras ...map[string]KnobRange) {
	for _, e := range extras {
		for k, v := range e {
			base[k] = v
		}
	}
}
