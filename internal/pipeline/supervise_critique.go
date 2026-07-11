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
// real tier from which fields actually changed) plus the parameter patch. Patch stays raw JSON so each
// mode's candidateRenderer unmarshals its own knob set (supervise_deepsky.go, supervise_comet.go, …).
type action struct {
	Tier  string          `json:"tier"`
	Patch json.RawMessage `json:"patch"`
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

// completer is the LLM seam the critique needs (satisfied by *llm.Runner) — an interface so the
// loop is testable with a scripted model.
type completer interface {
	Complete(ctx context.Context, msgs []llm.Message, opts llm.CompleteOptions) (string, error)
}

// critique asks the local vision model to judge a rendered finish and propose a parameter change,
// using the mode-specific prompt (system + objective + knob state) built by the renderer. It sends
// the full-frame thumbnail plus a native-res centre crop, the measured metrics, and — the model's
// MEMORY — a compact digest of the previous passes (what changed, what scored, what worsened) plus
// the best-so-far render as a third image when it isn't the current one. Soft-fail: any error yields
// a neutral verdict so the loop keeps the best render so far.
func critique(ctx context.Context, super completer, p supervisePrompt, m finishMetrics, pngPath, guidance, history, bestPng string) decision {
	stateJSON, _ := json.Marshal(p.state)
	mJSON, _ := json.Marshal(m)
	tierLine := ""
	if p.tiered {
		tierLine = fmt.Sprintf("You may use tiers up to %s (a change to a higher-tier knob is ignored).\n\n", p.maxTier)
	}
	user := llm.Message{Role: "user", Text: fmt.Sprintf(
		"Objective:\n%s\n\n%sCurrent parameters:\n%s\n\n"+
			"Measured metrics of the rendered image (fractions/levels in 0..1):\n%s\n\n"+
			"Image 1 is the whole frame; image 2 is a 100%% centre crop for judging noise, stars and colour.\n"+
			"Diagnose the defects, then propose the smallest change that fixes the worst one. Return JSON only.",
		p.objective, tierLine, stateJSON, mJSON)}
	if history != "" {
		user.Text += "\n\nPrevious passes (oldest first). NEVER re-propose a change that made the score worse, " +
			"and prefer refining the direction that improved it:\n" + history
	}
	if bestPng != "" && bestPng != pngPath {
		user.Text += "\n\nImage 3 is the BEST previous pass. Decide whether the current render beats it and what to change next."
	}
	if g := strings.TrimSpace(guidance); g != "" {
		user.Text += fmt.Sprintf("\n\nThe user is watching this run and asked for this change — honor it in "+
			"your diagnosis and your next patch: %q", g)
	}
	attachPreviews(&user, pngPath)
	if bestPng != "" && bestPng != pngPath {
		attachBestPreview(&user, bestPng)
	}
	reply, err := super.Complete(ctx,
		[]llm.Message{{Role: "system", Text: p.system}, user},
		llm.CompleteOptions{Temperature: 0.2, MaxTokens: 900, JSON: true})
	if err != nil {
		return decision{Score: 5, Assessment: "model unavailable: " + err.Error()}
	}
	return parseDecision(reply)
}

// attachBestPreview appends the best-so-far full-frame thumbnail (smaller than the current frame —
// it is a comparison anchor, not the subject) to the message's image list.
func attachBestPreview(user *llm.Message, bestPng string) {
	thumb, err := thumbnailJPEG(bestPng, 768, superviseThumbQuality)
	if err != nil {
		return
	}
	user.Images = append(user.Images, llm.InlineImage{Data: thumb, Mime: "image/jpeg"})
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
