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

// superviseRestackProceed is the confirm-gate answer that authorises the expensive Tier-C re-stack; any
// other answer caps the change to Tier B. Only fresh supervised deep-sky runs reach the gate.
const superviseRestackProceed = "Proceed"

// superviseRestackQuestion is shown to the user before the agent re-stacks every frame from scratch.
const superviseRestackQuestion = "The agent wants to re-stack every frame from scratch (Tier C — the " +
	"most expensive step, usually several minutes). Proceed with the re-stack, or keep the cheaper " +
	"Tier-B finish?"

// superviseRestackOptions builds the confirm-gate choices (proceed / skip).
func superviseRestackOptions() []string {
	return []string{superviseRestackProceed, "Skip (keep Tier B)"}
}

// superviseIter records one rendered candidate for best-pick: the working preset that produced it,
// the render, the prep notes, and its combined score.
type superviseIter struct {
	preset mode.Preset
	result renderResult
	notes  []string
	score  float64
}

// superviseOn reports whether the local-AI-agent finish was requested (opt-in via preset.Supervise) and
// the model server is reachable. Mode call sites pair it with their renderer's own readiness check
// (GIMP for deepsky, Siril for comet/nightscape/planetary).
func superviseOn(ctx context.Context, opts Options) bool {
	return opts.Supervisor != nil && opts.Preset != nil && opts.Preset.Supervise &&
		opts.Supervisor.Available(ctx) == nil
}

// superviseEnabled is the deepsky gate: the agent is on AND the layered-composite tool (GIMP) is ready.
func superviseEnabled(ctx context.Context, opts Options) bool {
	return superviseOn(ctx, opts) && opts.Gimp != nil && opts.Gimp.Available() == nil
}

// superviseFinish iterates render → critique → re-enter, keeping the best-scoring pass, driving a
// mode-specific candidateRenderer. Pass 0 renders the renderer's baseline (deepsky builds the linear
// prep = the standard finish); later passes re-enter at the tier the model's change requires, bounded by
// the tier ceiling, per-tier budgets and the iteration cap. The winner is finalized by the renderer. An
// error makes the caller fall back to the standard finish.
func superviseFinish(ctx context.Context, opts Options, r candidateRenderer, outDir string) (*postprocess.Result, error) {
	if opts.Supervisor == nil {
		return nil, fmt.Errorf("supervised finish requires a model runner")
	}
	ceiling := r.maxTier(opts)
	iters := superviseIters(opts.Preset)

	working := *opts.Preset
	budgetB, budgetC := superviseBudgets(working.Mode)
	renderTier := r.firstTier()
	target := working.SuperviseTargetScore
	if target <= 0 {
		target = superviseQualityTarget
	}

	// Cross-run memory: seed the working preset from the best prior pass of this target (clamped
	// through the shared param brain) and tell the model where that seed already scored.
	warmNote := ""
	if superviseHistoryOn() {
		warmNote = warmStart(ctx, opts, &working)
		if warmNote != "" {
			opts.report(Progress{Step: "supervise", Line: warmNote})
		}
	}

	var best *superviseIter
	bestIdx := -1
	var records []postprocess.IterationRecord
	var history []string
	var outcomes []iterOutcome
	var iterIDs []int64
	guidance := strings.TrimSpace(opts.Goal) // the user's run goal steers the FIRST critique
	noImprove := 0                           // consecutive passes that failed to beat the best (plateau stop)

	for i := 0; i < iters; i++ {
		if ctx.Err() != nil {
			break
		}
		outBase := filepath.Join(outDir, fmt.Sprintf("final_iter%d", i))
		g, notes, err := r.render(ctx, working, renderTier, outBase)
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

		det := scoreFinishMode(working.Mode, m, working.BackgroundLevel, iterZeroMetrics(outcomes, m))
		steered := guidance != "" // this pass's critique is guided by the user's last nudge/goal
		hist := ""
		if superviseHistoryOn() {
			hist = historyBlock(outcomes, bestIdx)
			if warmNote != "" {
				hist = strings.TrimSpace(warmNote + "\n" + hist)
			}
		}
		bestPng := ""
		if best != nil {
			bestPng = best.result.Png
		}
		dec := critique(ctx, opts.Supervisor, r.prompt(working, ceiling), m, g.Png, guidance, hist, bestPng)
		guidance = "" // consumed by this pass
		modelScore := clampf(dec.Score, 0, 10)
		// Deterministic metrics dominate the model's aesthetic vote, so a clipped or cast render can
		// never win on the model's word alone.
		combined := 0.6*det + 0.4*modelScore
		reason := strings.TrimSpace(dec.Assessment)

		rec := postprocess.IterationRecord{
			Index: i, Tier: renderTier.String(), PngPath: g.Png,
			DetScore: det, ModelScore: modelScore, CombinedScore: combined,
			Reasoning: reason, Defects: dec.Defects, Params: r.params(working),
		}
		records = append(records, rec)
		reportIteration(opts, rec)
		iterIDs = append(iterIDs, persistIteration(ctx, opts, rec, m, working))
		history = append(history, fmt.Sprintf("iter %d [%s] score %.1f (metrics %.1f, model %.1f) — %s",
			i+1, renderTier, combined, det, dec.Score, reason))
		opts.report(Progress{Step: "supervise", Line: history[len(history)-1]})
		outcomes = append(outcomes, iterOutcome{
			index: i, tier: renderTier.String(), params: ParamsFor(working),
			det: det, model: modelScore, combined: combined, detail: m.DetailIndex,
			defects: dec.Defects, note: reason,
		})

		improved := best == nil || combined > best.score+0.1 // a real step forward, not a jitter tie
		if best == nil || combined > best.score {
			best, bestIdx = &superviseIter{preset: working, result: g, notes: notes, score: combined}, i
		}
		if improved {
			noImprove = 0
		} else {
			noImprove++
		}

		// User steering: drain nudges/stop emitted while this pass rendered (after it's recorded so the
		// user reacts to what they see). Stop is in-band — keep the best pass and finalize on a live ctx,
		// so the job ends succeeded, not cancelled.
		if opts.Steer != nil {
			msg, stop := opts.Steer()
			if stop {
				break
			}
			guidance = msg
		}

		// Stop policy: honour the model's "done" only when the user isn't actively steering.
		if guidance == "" && !steered && dec.Done && det >= target {
			break
		}
		// Plateau: after three passes, two consecutive non-improving renders mean the loop is circling —
		// keep the best instead of burning budget re-rolling.
		if guidance == "" && i >= 2 && noImprove >= 2 {
			opts.report(Progress{Step: "supervise", Line: "supervise: plateau — keeping the best pass"})
			break
		}
		if dec.Action == nil || len(dec.Action.Patch) == 0 {
			if guidance != "" {
				continue // user asked for a change but the model proposed none → re-critique with the nudge
			}
			break // no proposed change → nothing more to try
		}
		// Cap the proposal to what we can still afford (ceiling + budgets); the renderer applies + clamps
		// the patch and reports the tier its change requires and whether it effectively changed anything.
		aff := affordableTier(ceiling, budgetB, budgetC)
		next, nextTier, changed := r.applyPatch(working, dec.Action.Patch, aff)
		if !changed {
			if guidance != "" {
				continue
			}
			break // no effective change we can afford → converged
		}
		// The re-stack confirmation gate is OPT-IN (preset.SuperviseConfirmRestack): the default is the
		// autonomous loop the budgets already bound; on decline, cap the change to Tier B.
		if nextTier == tierC && opts.Confirm != nil && working.SuperviseConfirmRestack {
			if choice, ok := opts.Confirm(ctx, superviseRestackQuestion, superviseRestackOptions()); ok && choice != superviseRestackProceed {
				next, nextTier, changed = r.applyPatch(working, dec.Action.Patch, tierB)
				if !changed {
					if guidance != "" {
						continue
					}
					break
				}
			}
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
	return r.finalize(ctx, opts, best, records, history, outDir)
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

// promoteResult copies a winning iteration's artifacts onto the canonical final.* basename.
func promoteResult(g renderResult, finalBase string) error {
	for _, p := range [][2]string{
		{g.Xcf, finalBase + ".xcf"},
		{g.Tif, finalBase + ".tif"},
		{g.Png, finalBase + ".png"},
	} {
		if p[0] == "" || p[0] == p[1] { // modes without a layered .xcf leave that source empty
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
		s -= 25 * maxf(0, wc-whiteClipMax) // blown highlights — the highlight cap rolls star cores below white, so residual clipping is a real defect (stars burning)
	}
	s -= 12 * absf(m.GreenCast)               // green/magenta cast (per-channel median spread)
	s -= 20 * maxf(0, m.WarmCast-warmCastMax) // warm/orange SKY cast — one-sided: push a warm sky toward neutral without over-cooling to blue (threshold shared with the every-run warnings)
	s -= 15 * absf(m.SignalCast)              // magenta/pink (or green) cast in the BRIGHT galaxy/star signal — the median misses it (M31 pink)
	s -= 8 * absf(m.Background-targetBg)      // sky off the autostretch target
	return clampf(s, 0, 10)
}

// reportIteration streams one completed pass to the UI (a copy, so later mutation of records — e.g.
// marking the winner chosen — doesn't race the consumer).
func reportIteration(opts Options, rec postprocess.IterationRecord) {
	r := rec
	opts.report(Progress{Step: "supervise", Iteration: &r})
}

// persistIteration best-effort writes one supervised iteration to the store — including the full
// working preset (the warm-start memory) and the render's PNG path — but only for a job run
// (JobID != 0 with a store). Returns the row id (0 when skipped or on failure); never fatal.
func persistIteration(ctx context.Context, opts Options, rec postprocess.IterationRecord, m finishMetrics, working mode.Preset) int64 {
	if opts.FinishIterStore == nil || opts.JobID == 0 {
		return 0
	}
	params, _ := json.Marshal(rec.Params)
	metrics, _ := json.Marshal(m)
	defects, _ := json.Marshal(rec.Defects)
	id, err := opts.FinishIterStore.CreateFinishIteration(ctx, opts.JobID, rec.Index, rec.Tier,
		params, metrics, defects, rec.DetScore, rec.ModelScore, rec.CombinedScore, rec.Reasoning,
		rec.PngPath, presetBlob(working))
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
