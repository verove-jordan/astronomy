// Optional local-AI-agent finish supervisor. When opted in (preset.Supervise, set by the run
// request / --supervise) and a host model server is reachable, the engine re-renders only the fast
// GIMP composite a few times, each pass judged by a local vision model against an objective, and
// keeps the best-scoring render. The expensive linear prep (rgbcomp, GraXpert, SPCC, stretch) runs
// once, before the loop. Everything here is soft-fail: any error makes combine() fall back to the
// standard single-pass finish, so a run never fails because of the agent. When the feature is off
// the standard finish path is used unchanged.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/gimp"
	"github.com/verove-jordan/astronomy/internal/llm"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/postprocess"
)

const (
	superviseDefaultIters = 4    // iterations when the preset does not set a cap
	superviseHardMaxIters = 8    // never loop more than this, whatever the preset asks
	superviseThumbDim     = 1024 // long-side px of the JPEG sent to the vision model
	superviseThumbQuality = 85
)

// composeParams are the post-stretch GIMP-composite knobs the supervisor tunes between iterations.
// The expensive linear processing upstream is fixed; these are the fast finishing controls.
type composeParams struct {
	Saturation   float64 `json:"saturation"`
	HaScreen     float64 `json:"ha_screen"`
	HaBlackPoint float64 `json:"ha_black_point"`
	ChromaBlur   float64 `json:"chroma_blur"`
	CropFrac     float64 `json:"crop_frac"`
}

func presetComposeParams(p *mode.Preset) composeParams {
	return composeParams{
		Saturation:   p.Saturation,
		HaScreen:     p.HaScreen,
		HaBlackPoint: p.HaBlackPoint,
		ChromaBlur:   p.ChromaBlur,
		CropFrac:     p.CropFrac,
	}
}

// clamp keeps each knob inside the range the GIMP finish actually honours, so a wild model
// suggestion can never push the composite outside known-good bounds.
func (c composeParams) clamp() composeParams {
	c.Saturation = clampf(c.Saturation, 0, 0.6)
	c.HaScreen = clampf(c.HaScreen, 0, 0.8)
	c.HaBlackPoint = clampf(c.HaBlackPoint, 0, 0.3)
	c.ChromaBlur = clampf(c.ChromaBlur, 0, 12)
	c.CropFrac = clampf(c.CropFrac, 0, 0.1)
	return c
}

// composeParamsPatch is the model's proposed change: every field optional, so a partial reply (e.g.
// only saturation) overrides just that knob instead of zeroing the rest.
type composeParamsPatch struct {
	Saturation   *float64 `json:"saturation"`
	HaScreen     *float64 `json:"ha_screen"`
	HaBlackPoint *float64 `json:"ha_black_point"`
	ChromaBlur   *float64 `json:"chroma_blur"`
	CropFrac     *float64 `json:"crop_frac"`
}

func (p composeParamsPatch) apply(c composeParams) composeParams {
	if p.Saturation != nil {
		c.Saturation = *p.Saturation
	}
	if p.HaScreen != nil {
		c.HaScreen = *p.HaScreen
	}
	if p.HaBlackPoint != nil {
		c.HaBlackPoint = *p.HaBlackPoint
	}
	if p.ChromaBlur != nil {
		c.ChromaBlur = *p.ChromaBlur
	}
	if p.CropFrac != nil {
		c.CropFrac = *p.CropFrac
	}
	return c
}

// decision is the model's verdict for one iteration.
type decision struct {
	Score     float64             `json:"score"` // 0..10 overall quality
	Done      bool                `json:"done"`  // good enough; stop
	Reasoning string              `json:"reasoning"`
	Next      *composeParamsPatch `json:"next"` // proposed changes; nil → keep current
}

// superviseIter records one rendered candidate for best-pick.
type superviseIter struct {
	params composeParams
	result *gimp.Result
	score  float64
}

// superviseEnabled reports whether the optional local-AI-agent finish should run: it is opt-in
// (preset.Supervise), needs both GIMP and the model server reachable, and only tunes the GIMP
// composite path.
func superviseEnabled(ctx context.Context, opts Options) bool {
	return opts.Supervisor != nil && opts.Preset != nil && opts.Preset.Supervise &&
		opts.Gimp != nil && opts.Gimp.Available() == nil && opts.Supervisor.Available(ctx) == nil
}

// superviseFinish prepares the stretched component TIFFs once, then repeatedly re-renders the fast
// GIMP composite with model-proposed tweaks, scoring each and keeping the best. StarNet++ star
// reduction is applied once, to the winning composite. An error makes combine() fall back to the
// standard finish.
func superviseFinish(ctx context.Context, opts Options, channels map[string]string, workRun, outDir string, res *Result) (*postprocess.Result, error) {
	stretchDir := filepath.Join(workRun, "05_stretched")
	if err := fsutil.EnsureDir(stretchDir); err != nil {
		return nil, err
	}
	deg := backgroundDegree(ctx, opts) // 0 when GraXpert already extracted the background
	cc := postprocess.ColorCalOptions{
		Enabled: opts.Preset.ColorCalibration, RemoveGreen: true, Solve: opts.Solve, Spcc: opts.Spcc,
	}
	base, notes, err := prepGimpInputs(ctx, opts, opts.Runner, channels, outDir, stretchDir, deg, cc,
		opts.Preset.BackgroundLevel, opts.Preset.LinkedStretch)
	if err != nil {
		return nil, err
	}
	// Composite-time settings that are not tuned by the loop carry over from the preset.
	base.LumCurve = opts.Preset.LumCurve
	base.CoreHighlightKnee = opts.Preset.CoreHighlightKnee
	base.CoreHighlightCeil = opts.Preset.CoreHighlightCeil
	base.HaExcludeStars = opts.Preset.HaExcludeStars

	iters := opts.Preset.SuperviseMaxIters
	if iters <= 0 {
		iters = superviseDefaultIters
	}
	if iters > superviseHardMaxIters {
		iters = superviseHardMaxIters
	}

	objective := objectiveText(opts.Preset)
	cur := presetComposeParams(opts.Preset).clamp()
	var best *superviseIter
	bestIdx := -1
	var history []string
	var records []postprocess.IterationRecord
	var iterIDs []int64

	for i := 0; i < iters; i++ {
		outBase := filepath.Join(outDir, fmt.Sprintf("final_iter%d", i))
		g, err := buildComposite(opts.Gimp, base, cur, opts.Preset.Curve, outBase)
		if err != nil {
			if best != nil {
				break // keep the best render we already have
			}
			return nil, err
		}
		m, err := measureFinish(g.Png)
		if err != nil {
			if best != nil {
				break
			}
			return nil, err
		}
		opts.report(Progress{Step: fmt.Sprintf("supervise %d/%d", i+1, iters), Preview: g.Png})

		det := scoreFinish(m, opts.Preset.BackgroundLevel)
		dec := critique(ctx, opts.Supervisor, objective, cur, m, g.Png)
		modelScore := clampf(dec.Score, 0, 10)
		// Deterministic metrics dominate the model's aesthetic vote, so a clipped or cast render can
		// never win on the model's word alone.
		combined := 0.6*det + 0.4*modelScore
		reason := strings.TrimSpace(dec.Reasoning)
		opts.report(Progress{Step: "supervise", Line: fmt.Sprintf(
			"iter %d/%d: score %.1f (metrics %.1f, model %.1f) — %s",
			i+1, iters, combined, det, dec.Score, reason)})
		history = append(history, fmt.Sprintf("iter %d (score %.1f): %s", i+1, combined, reason))
		records = append(records, postprocess.IterationRecord{
			Index: i, PngPath: g.Png, DetScore: det, ModelScore: modelScore,
			CombinedScore: combined, Reasoning: reason, Params: cur.asMap(),
		})
		iterIDs = append(iterIDs, persistIteration(ctx, opts, i, cur, m, det, modelScore, combined, reason))

		if best == nil || combined > best.score {
			best, bestIdx = &superviseIter{params: cur, result: g, score: combined}, i
		}
		if dec.Done || dec.Next == nil {
			break
		}
		next := dec.Next.apply(cur).clamp()
		if next == cur { // model proposed no effective change → converged
			break
		}
		cur = next
	}
	if best == nil {
		return nil, fmt.Errorf("supervised finish produced no render")
	}
	if bestIdx >= 0 && bestIdx < len(records) {
		records[bestIdx].Chosen = true
	}
	markChosen(ctx, opts, iterIDs, bestIdx)

	// Promote the winning iteration to the canonical final.* outputs.
	finalBase := filepath.Join(outDir, "final")
	if err := promoteResult(best.result, finalBase); err != nil {
		return nil, err
	}
	out := &postprocess.Result{
		Mode:     compMode(channels),
		Channels: filterList(channels),
		Outputs:  []string{finalBase + ".xcf", finalBase + ".tif", finalBase + ".png"},
		Notes: append([]string{
			fmt.Sprintf("local AI agent finish: %d iteration(s), best score %.1f", len(history), best.score),
			"chosen params: " + paramsNote(best.params),
		}, notes...),
	}
	out.Notes = append(out.Notes, history...)
	out.Iterations = records

	// StarNet++ star reduction once, on the winning composite (same as the standard finish).
	if aiStars(ctx, opts) {
		extra, note := reduceStarsAI(ctx, opts, finalBase+".tif", outDir, nil)
		out.Outputs = append(out.Outputs, extra...)
		if note != "" {
			out.Notes = append(out.Notes, note)
		} else {
			out.Notes = append(out.Notes, fmt.Sprintf("StarNet++ star reduction (stars at %.0f%%)", opts.Preset.StarReduce*100))
		}
	}
	return out, nil
}

// buildComposite renders one layered composite from the prepped inputs with the given tunable
// params (the rest of the GIMP Inputs — Base/Lum/Ha/Color/LumCurve/HaExcludeStars — are carried in
// base). Shared so the loop renders exactly what the standard finish would for the same params.
func buildComposite(c *gimp.Client, base gimp.Inputs, p composeParams, curve []float64, outBase string) (*gimp.Result, error) {
	in := base
	in.HaBlack = p.HaBlackPoint
	in.ChromaBlur = p.ChromaBlur
	in.CropFrac = p.CropFrac
	return gimp.BuildImage(c, in, curve, p.HaScreen, p.Saturation, outBase)
}

// promoteResult copies a winning iteration's artifacts onto the canonical final.* basename.
func promoteResult(g *gimp.Result, finalBase string) error {
	for _, p := range [][2]string{
		{g.Xcf, finalBase + ".xcf"},
		{g.Tif, finalBase + ".tif"},
		{g.Png, finalBase + ".png"},
	} {
		if p[0] == p[1] {
			continue
		}
		if err := fsutil.CopyFile(p[0], p[1]); err != nil {
			return fmt.Errorf("promote %s: %w", filepath.Base(p[0]), err)
		}
	}
	return nil
}

// scoreFinish is the deterministic, no-reference quality score (0..10) from the measured metrics:
// it penalizes crushed shadows, blown highlights, colour cast and a background off the stretch
// target. It is the guardrail that keeps a bad render from winning on the model's vote alone.
func scoreFinish(m finishMetrics, targetBg float64) float64 {
	s := 10.0
	for _, bc := range m.BlackClip {
		s -= 60 * maxf(0, bc-0.01) // crushed shadows beyond 1% of pixels
	}
	for _, wc := range m.WhiteClip {
		s -= 15 * maxf(0, wc-0.01) // blown highlights beyond 1% (some star cores are fine)
	}
	s -= 12 * absf(m.GreenCast)          // colour cast (per-channel median spread)
	s -= 8 * absf(m.Background-targetBg) // sky off the autostretch target
	return clampf(s, 0, 10)
}

// critique asks the local vision model to judge a rendered finish and propose parameter changes. It
// is soft-fail: a model/transport error yields a neutral verdict with no proposed change, so the
// loop simply stops and keeps the best render so far (iteration 0 = the standard finish).
func critique(ctx context.Context, super *llm.Runner, objective string, cur composeParams, m finishMetrics, pngPath string) decision {
	curJSON, _ := json.Marshal(cur)
	mJSON, _ := json.Marshal(m)
	user := llm.Message{Role: "user", Text: fmt.Sprintf(
		"Objective:\n%s\n\nCurrent finishing parameters:\n%s\n\nMeasured metrics (fractions/levels in 0..1):\n%s\n\nReturn JSON only.",
		objective, curJSON, mJSON)}
	if thumb, err := thumbnailJPEG(pngPath, superviseThumbDim, superviseThumbQuality); err == nil {
		user.Image, user.ImageMime = thumb, "image/jpeg"
	}
	reply, err := super.Complete(ctx, []llm.Message{{Role: "system", Text: supervisorSystemPrompt}, user},
		llm.CompleteOptions{Temperature: 0.2, MaxTokens: 700, JSON: true})
	if err != nil {
		return decision{Score: 5, Reasoning: "model unavailable: " + err.Error()}
	}
	return parseDecision(reply)
}

func parseDecision(reply string) decision {
	d := decision{Score: 5}
	if err := json.Unmarshal([]byte(extractJSON(reply)), &d); err != nil {
		return decision{Score: 5, Reasoning: "unparseable model reply"}
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

func objectiveText(p *mode.Preset) string {
	return fmt.Sprintf(
		"Produce a clean, natural %s image: a neutral sky background near %.3f (no green or magenta cast), "+
			"shadows not crushed to pure black, highlights not blown (star cores excepted), pleasing but not "+
			"oversaturated colour, and tight stars. Adjust only the given parameters, in small steps, and only "+
			"when the image clearly needs it. Set done=true once further tuning would not clearly help.",
		p.Mode, p.BackgroundLevel)
}

func paramsNote(p composeParams) string {
	return fmt.Sprintf("saturation=%.2f ha_screen=%.2f ha_black_point=%.2f chroma_blur=%.1f crop_frac=%.3f",
		p.Saturation, p.HaScreen, p.HaBlackPoint, p.ChromaBlur, p.CropFrac)
}

const supervisorSystemPrompt = `You are an expert astrophotography image-finishing assistant. You are given a rendered deep-sky image plus objective measurements, and you tune a small fixed set of GIMP-composite parameters to improve it. The expensive linear processing (background extraction, colour calibration, stretch) is already done and fixed — you only adjust these post-stretch composite knobs:
- saturation (0..0.6): overall colour saturation.
- ha_screen (0..0.8): opacity of the red H-alpha layer screened in (only relevant if Ha is present).
- ha_black_point (0..0.3): clips the Ha background to black so its red lifts only bright HII knots, not the whole sky.
- chroma_blur (0..12 px): blurs colour noise in an LRGB composite; the luminance keeps all detail.
- crop_frac (0..0.1): trims ragged stacking-edge bands off each edge.
Judge for: neutral background (no green/magenta cast), shadows not crushed, highlights not blown (except star cores), natural colour, tight stars. Respond with JSON ONLY, no prose:
{"score": <0-10>, "done": <true|false>, "reasoning": "<one short sentence>", "next": {<only the parameters you want to change>}}
Make small, deliberate changes. Set done=true when further tuning would not clearly help.`

func clampf(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func absf(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// asMap renders the tuned params as a flat map for the persisted iteration record (UI-friendly).
func (c composeParams) asMap() map[string]float64 {
	return map[string]float64{
		"saturation": c.Saturation, "ha_screen": c.HaScreen, "ha_black_point": c.HaBlackPoint,
		"chroma_blur": c.ChromaBlur, "crop_frac": c.CropFrac,
	}
}

// persistIteration best-effort writes one supervised iteration to the store, but only for a job run
// (JobID != 0 with a store). Returns the row id (0 when skipped or on failure); never fatal.
func persistIteration(ctx context.Context, opts Options, iter int, p composeParams, m finishMetrics, det, model, combined float64, reasoning string) int64 {
	if opts.FinishIterStore == nil || opts.JobID == 0 {
		return 0
	}
	params, _ := json.Marshal(p)
	metrics, _ := json.Marshal(m)
	id, err := opts.FinishIterStore.CreateFinishIteration(ctx, opts.JobID, iter, params, metrics, det, model, combined, reasoning)
	if err != nil {
		opts.report(Progress{Step: "supervise", Line: "warn: persist iteration failed: " + err.Error()})
		return 0
	}
	return id
}

// markChosen flags the winning iteration's persisted row (best-effort; no-op without a store/job).
func markChosen(ctx context.Context, opts Options, ids []int64, bestIdx int) {
	if opts.FinishIterStore == nil || bestIdx < 0 || bestIdx >= len(ids) || ids[bestIdx] == 0 {
		return
	}
	if err := opts.FinishIterStore.MarkFinishIterationChosen(ctx, ids[bestIdx]); err != nil {
		opts.report(Progress{Step: "supervise", Line: "warn: mark chosen iteration failed: " + err.Error()})
	}
}
