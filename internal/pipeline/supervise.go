// Optional local-AI-agent finish supervisor. When opted in (preset.Supervise, set by the run request
// / --supervise / refine) and a host model server is reachable, the engine iterates: it renders the
// image, asks a local vision model to diagnose defects and propose a parameter change, then re-enters
// the pipeline at the cheapest tier that reflects the change (A = GIMP composite, B = linear finish
// prep, C = re-stack from raws — see supervise_reentry.go), scoring each pass and keeping the best.
// The param model + tier classifier live in supervise_params.go; the structured critique in
// supervise_critique.go. Everything is soft-fail: any error makes finishAligned fall back to the
// standard single-pass finish, so a run never fails because of the agent.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/gimp"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/postprocess"
)

const (
	superviseDefaultIters = 4    // iterations when the preset does not set a cap
	superviseHardMaxIters = 8    // never loop more than this, whatever the preset asks
	superviseThumbDim     = 1024 // long-side px of the whole-frame JPEG sent to the vision model
	superviseThumbQuality = 85
	// superviseQualityTarget is the deterministic-score floor (0..10) the render must clear before the
	// model's "done" is honoured — so the agent can never declare a clipped/cast image finished.
	superviseQualityTarget = 7.0
)

// superviseIter records one rendered candidate for best-pick: the working preset that produced it,
// the render, the prep notes, and its combined score.
type superviseIter struct {
	preset mode.Preset
	result *gimp.Result
	notes  []string
	score  float64
}

// superviseEnabled reports whether the optional local-AI-agent finish should run: it is opt-in
// (preset.Supervise), needs both GIMP and the model server reachable.
func superviseEnabled(ctx context.Context, opts Options) bool {
	return opts.Supervisor != nil && opts.Preset != nil && opts.Preset.Supervise &&
		opts.Gimp != nil && opts.Gimp.Available() == nil && opts.Supervisor.Available(ctx) == nil
}

// superviseFinish iterates render → critique → re-enter, keeping the best-scoring pass. The first pass
// establishes the linear prep (the standard finish); later passes re-enter at the tier the model's
// change requires, bounded by the tier ceiling, per-tier budgets and the iteration cap. StarNet++ star
// reduction is applied once, to the winning composite. An error makes finishAligned fall back.
func superviseFinish(ctx context.Context, opts Options, channels map[string]string, workRun, outDir string, res *Result) (*postprocess.Result, error) {
	re, err := newReentry(opts, channels, workRun, outDir)
	if err != nil {
		return nil, err
	}
	ceiling := allowedMaxTier(opts)
	iters := superviseIters(opts.Preset)
	objective := objectiveText(opts.Preset)

	working := clampPreset(*opts.Preset)
	budgetB, budgetC := superviseBudgetTierB, superviseBudgetTierC
	renderTier := tierB // pass 0 builds the linear prep = the standard-finish baseline

	var best *superviseIter
	bestIdx := -1
	var records []postprocess.IterationRecord
	var history []string
	var iterIDs []int64

	for i := 0; i < iters; i++ {
		if ctx.Err() != nil {
			break
		}
		outBase := filepath.Join(outDir, fmt.Sprintf("final_iter%d", i))
		g, err := re.render(ctx, renderTier, working, outBase)
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

		det := scoreFinish(m, working.BackgroundLevel)
		dec := critique(ctx, opts.Supervisor, objective, working, ceiling, m, g.Png)
		modelScore := clampf(dec.Score, 0, 10)
		// Deterministic metrics dominate the model's aesthetic vote, so a clipped or cast render can
		// never win on the model's word alone.
		combined := 0.6*det + 0.4*modelScore
		reason := strings.TrimSpace(dec.Assessment)

		rec := postprocess.IterationRecord{
			Index: i, Tier: renderTier.String(), PngPath: g.Png,
			DetScore: det, ModelScore: modelScore, CombinedScore: combined,
			Reasoning: reason, Defects: dec.Defects, Params: paramsMap(working),
		}
		records = append(records, rec)
		reportIteration(opts, rec)
		iterIDs = append(iterIDs, persistIteration(ctx, opts, rec, m))
		history = append(history, fmt.Sprintf("iter %d [%s] score %.1f (metrics %.1f, model %.1f) — %s",
			i+1, renderTier, combined, det, dec.Score, reason))
		opts.report(Progress{Step: "supervise", Line: history[len(history)-1]})

		if best == nil || combined > best.score {
			best, bestIdx = &superviseIter{preset: working, result: g, notes: re.notes, score: combined}, i
		}

		// Stop policy: the model is done AND the deterministic guardrail is satisfied.
		if dec.Done && det >= superviseQualityTarget {
			break
		}
		if dec.Action == nil || dec.Action.Patch == nil {
			break // no proposed change → nothing more to try
		}
		// Cap the proposal to what we can still afford (ceiling + budgets), then re-derive the real tier.
		aff := affordableTier(ceiling, budgetB, budgetC)
		next := clampPreset(capToTier(working, dec.Action.Patch.apply(working), aff))
		nextTier := tierOf(working, next)
		if nextTier == tierA && !composeChanged(working, next) {
			break // no effective change we can afford → converged
		}
		switch nextTier {
		case tierC:
			budgetC--
		case tierB:
			budgetB--
		}
		working, renderTier = next, nextTier
	}

	if best == nil {
		return nil, fmt.Errorf("supervised finish produced no render")
	}
	if bestIdx >= 0 && bestIdx < len(records) {
		records[bestIdx].Chosen = true
		reportIteration(opts, records[bestIdx]) // re-emit the winner so the UI can mark it chosen
	}
	markChosen(ctx, opts, iterIDs, bestIdx)
	return finalizeSupervised(ctx, opts, re, best, records, history, outDir)
}

// finalizeSupervised promotes the winning iteration to final.*, applies StarNet++ once with the
// winner's preset, and assembles the result record.
func finalizeSupervised(ctx context.Context, opts Options, re *reentry, best *superviseIter,
	records []postprocess.IterationRecord, history []string, outDir string) (*postprocess.Result, error) {
	finalBase := filepath.Join(outDir, "final")
	if err := promoteResult(best.result, finalBase); err != nil {
		return nil, err
	}
	out := &postprocess.Result{
		Mode:     compMode(re.channels),
		Channels: filterList(re.channels),
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

// superviseIters resolves the loop cap from the preset (0 → default), never above the hard max.
func superviseIters(p *mode.Preset) int {
	iters := p.SuperviseMaxIters
	if iters <= 0 {
		iters = superviseDefaultIters
	}
	if iters > superviseHardMaxIters {
		iters = superviseHardMaxIters
	}
	return iters
}

// allowedMaxTier is the highest tier the agent may use: the preset ceiling (SuperviseTier), further
// capped to Tier B when no raw frames are available to re-stack (Options.Reprocess is nil).
func allowedMaxTier(opts Options) tier {
	ceiling := tierC
	if s := strings.TrimSpace(opts.Preset.SuperviseTier); s != "" {
		ceiling = parseTier(s)
	}
	if opts.Reprocess == nil && ceiling > tierB {
		ceiling = tierB
	}
	return ceiling
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

// reportIteration streams one completed pass to the UI (a copy, so later mutation of records — e.g.
// marking the winner chosen — doesn't race the consumer).
func reportIteration(opts Options, rec postprocess.IterationRecord) {
	r := rec
	opts.report(Progress{Step: "supervise", Iteration: &r})
}

// persistIteration best-effort writes one supervised iteration to the store, but only for a job run
// (JobID != 0 with a store). Returns the row id (0 when skipped or on failure); never fatal.
func persistIteration(ctx context.Context, opts Options, rec postprocess.IterationRecord, m finishMetrics) int64 {
	if opts.FinishIterStore == nil || opts.JobID == 0 {
		return 0
	}
	params, _ := json.Marshal(rec.Params)
	metrics, _ := json.Marshal(m)
	defects, _ := json.Marshal(rec.Defects)
	id, err := opts.FinishIterStore.CreateFinishIteration(ctx, opts.JobID, rec.Index, rec.Tier,
		params, metrics, defects, rec.DetScore, rec.ModelScore, rec.CombinedScore, rec.Reasoning)
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
