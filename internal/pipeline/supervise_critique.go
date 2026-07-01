// Structured critique for the supervised finish: the local vision model diagnoses the rendered image
// (a fixed defect vocabulary), then proposes a tiered parameter change to fix the worst defect. Kept
// separate from the loop (supervise.go) and the param model (supervise_params.go). Everything is
// soft-fail: a model/transport error yields a neutral verdict with no change, so the loop just keeps
// the best render so far.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/verove-jordan/astronomy/internal/llm"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/postprocess"
)

// Preview payload sizes: a downscaled whole-frame view for composition/gradient, plus a native-res
// centre crop for noise/stars/colour.
const (
	superviseCropFrac    = 0.4 // central fraction sent at 100%
	superviseCropDim     = 900 // long-side px cap of the centre crop
	superviseCropQuality = 88
)

// action is the model's proposed change: a target tier (informational — the engine re-derives the
// real tier from which fields actually changed) plus the parameter patch.
type action struct {
	Tier  string          `json:"tier"`
	Patch *supervisePatch `json:"patch"`
}

// decision is the model's verdict for one iteration: the diagnosed defects, a one-line assessment, an
// overall score, whether it is done, and the proposed next action (nil → keep current).
type decision struct {
	Defects    []postprocess.Defect `json:"defects"`
	Assessment string               `json:"assessment"`
	Score      float64              `json:"score"` // 0..10 overall quality
	Done       bool                 `json:"done"`
	Action     *action              `json:"action"`
}

// critique asks the local vision model to judge a rendered finish and propose a parameter change. It
// sends the full-frame thumbnail plus a native-res centre crop and the measured metrics, and tells the
// model which tiers it may still use (maxTier). Soft-fail: any error yields a neutral verdict so the
// loop stops on the best render so far.
func critique(ctx context.Context, super *llm.Runner, objective string, cur mode.Preset, maxTier tier, m finishMetrics, pngPath string) decision {
	stateJSON, _ := json.Marshal(supervisorState(cur))
	mJSON, _ := json.Marshal(m)
	user := llm.Message{Role: "user", Text: fmt.Sprintf(
		"Objective:\n%s\n\nYou may use tiers up to %s (a change to a higher-tier knob is ignored).\n\n"+
			"Current parameters:\n%s\n\nMeasured metrics of the rendered image (fractions/levels in 0..1):\n%s\n\n"+
			"Image 1 is the whole frame; image 2 is a 100%% centre crop for judging noise, stars and colour.\n"+
			"Diagnose the defects, then propose the smallest-tier change that fixes the worst one. Return JSON only.",
		objective, maxTier, stateJSON, mJSON)}
	attachPreviews(&user, pngPath)
	reply, err := super.Complete(ctx,
		[]llm.Message{{Role: "system", Text: supervisorSystemPrompt}, user},
		llm.CompleteOptions{Temperature: 0.2, MaxTokens: 900, JSON: true})
	if err != nil {
		return decision{Score: 5, Assessment: "model unavailable: " + err.Error()}
	}
	return parseDecision(reply)
}

// attachPreviews attaches the whole-frame thumbnail (legacy single-image slot) and the centre crop
// (multi-image slot). A failed crop just omits that image — the whole frame alone still works.
func attachPreviews(user *llm.Message, pngPath string) {
	if thumb, err := thumbnailJPEG(pngPath, superviseThumbDim, superviseThumbQuality); err == nil {
		user.Image, user.ImageMime = thumb, "image/jpeg"
	}
	if crop, err := centerCropJPEG(pngPath, superviseCropFrac, superviseCropDim, superviseCropQuality); err == nil {
		user.Images = append(user.Images, llm.InlineImage{Data: crop, Mime: "image/jpeg"})
	}
}

func parseDecision(reply string) decision {
	d := decision{Score: 5}
	if err := json.Unmarshal([]byte(extractJSON(reply)), &d); err != nil {
		return decision{Score: 5, Assessment: "unparseable model reply"}
	}
	return d
}

// extractJSON pulls the JSON object out of a model reply that may wrap it in prose or ``` fences.
func extractJSON(s string) string {
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}

// supervisorState is the compact current-parameter view sent to the model, grouped by tier so the
// model can reason about the cost of each knob it might change.
func supervisorState(p mode.Preset) map[string]any {
	return map[string]any{
		"tierA": map[string]any{
			"saturation": p.Saturation, "ha_screen": p.HaScreen, "ha_black_point": p.HaBlackPoint,
			"chroma_blur": p.ChromaBlur, "crop_frac": p.CropFrac,
			"core_highlight_knee": p.CoreHighlightKnee, "core_highlight_ceil": p.CoreHighlightCeil,
			"ha_exclude_stars": p.HaExcludeStars,
		},
		"tierB": map[string]any{
			"background_level": p.BackgroundLevel, "linked_stretch": p.LinkedStretch,
			"color_calibration": p.ColorCalibration, "combined_background_ai": p.CombinedBackgroundAI,
			"background_degree": p.BackgroundDegree, "color_denoise_ai": p.ColorDenoiseAI,
			"star_reduce": p.StarReduce,
		},
		"tierC": map[string]any{
			"roundness_floor": p.Grade.RoundnessFloor, "fwhm_sigma": p.Grade.FWHMSigma,
			"background_sigma": p.Grade.BackgroundSigma, "star_count_frac": p.Grade.StarCountFrac,
			"trail_mask_k": p.TrailMaskK, "denoise_chroma": p.DenoiseChroma,
			"denoise_lum": p.DenoiseLum, "background_ai": p.BackgroundAI,
		},
	}
}

func objectiveText(p *mode.Preset) string {
	return fmt.Sprintf(
		"Produce a clean, natural %s image: a neutral sky background near %.3f (no green or magenta cast), "+
			"shadows not crushed to pure black, highlights not blown (star cores excepted), pleasing but not "+
			"oversaturated colour, tight round stars, and no gradients or trail residue. Change parameters in "+
			"small steps, only when the image clearly needs it, and prefer the cheapest tier that can fix the "+
			"defect. Set done=true once further tuning would not clearly help.",
		p.Mode, p.BackgroundLevel)
}

// supervisorSystemPrompt documents the whitelisted knobs as a tiered "tool menu" (each with its cost
// and safe range) and the JSON reply shape. The model diagnoses first, then proposes one tiered patch.
// The knob menu + defect vocabulary come from prompts.go so the chat assistant (AssistSystemPrompt)
// stays in lockstep.
const supervisorIntro = `You are an expert astrophotography image-processing assistant. You are shown a rendered deep-sky image (whole frame + a 100% centre crop) plus objective measurements, and you iteratively tune the processing to improve it. You choose changes from three cost tiers and should prefer the cheapest tier that fixes the worst defect.

`

const supervisorDefectRules = `

Diagnose defects using this fixed vocabulary for "kind": ` + defectVocabulary + `. Severity is "low", "medium" or "high".

Respond with JSON ONLY, no prose:
{"defects":[{"kind":"<vocab>","severity":"low|medium|high","note":"<short>"}],
 "assessment":"<one short sentence>","score":<0-10>,"done":<true|false>,
 "action":{"tier":"A|B|C","patch":{<only the parameters you want to change>}}}
Make small, deliberate changes. Set done=true (and omit action) when further tuning would not clearly help.`

const supervisorSystemPrompt = supervisorIntro + tierKnobMenu + supervisorDefectRules
