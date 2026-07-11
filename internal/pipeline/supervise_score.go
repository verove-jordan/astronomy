// Per-mode deterministic scoring for the supervised finish. Every mode shares the golden objective
// (natural colour, maximum REAL detail, nothing burned) but measures it differently: planetary has
// no sky/star colour to judge and lives or dies on acutance; milkyway must balance a foreground
// against the sky; deepsky/comet care hardest about casts and star colour.
package pipeline

import (
	"os"

	"github.com/verove-jordan/astronomy/internal/mode"
)

// superviseBudgets is each mode's per-tier re-entry allowance on top of the iteration cap: the
// expensive re-runs are bounded per mode so full autonomy can never blow up wall-clock. Deep-sky
// keeps the original B/C budgets; the single-prep modes get their (cheaper) re-stack once or twice.
func superviseBudgets(m mode.Mode) (budgetB, budgetC int) {
	switch m {
	case mode.Comet, mode.Milkyway:
		return 0, 1
	case mode.Planetary:
		return 0, 2
	default:
		return superviseBudgetTierB, superviseBudgetTierC
	}
}

// superviseHistoryOn gates the iteration-memory features (history block, warm start) — the
// ASTRO_SUPERVISE_HISTORY=off kill-switch exists so a mis-behaving model prompt can be bisected
// without a rebuild.
func superviseHistoryOn() bool {
	return os.Getenv("ASTRO_SUPERVISE_HISTORY") != "off"
}

// iterZeroMetrics returns the FIRST pass's metrics as the relative baseline (for detail scoring);
// pass 0 itself baselines against its own metrics (relative gain 1.0).
func iterZeroMetrics(outs []iterOutcome, cur finishMetrics) finishMetrics {
	if len(outs) == 0 {
		return cur
	}
	return finishMetrics{DetailIndex: outs[0].detail}
}

// scoreFinishMode is the mode-weighted deterministic score (0..10). Deepsky/nebula/comet keep the
// full colour guardrails; planetary drops the sky-colour penalties (a mineral moon has no "cast" in
// the deep-sky sense) and rewards REAL detail gained over the first pass; milkyway adds a
// foreground-balance guard so a graded sky can't win by crushing the landscape to black.
func scoreFinishMode(m mode.Mode, fm finishMetrics, targetBg float64, iter0 finishMetrics) float64 {
	switch m {
	case mode.Planetary:
		s := 10.0
		for _, wc := range fm.WhiteClip {
			s -= 25 * maxf(0, wc-whiteClipMax) // blown highlands/craters
		}
		for _, bc := range fm.BlackClip {
			s -= 20 * maxf(0, bc-0.30) // a dark-sky border is normal; only a crushed DISK hurts
		}
		// Detail bonus/penalty RELATIVE to pass 0 (absolute Laplacian variance is scene-dependent):
		// capped at ±2 so acutance can polish a score, never buy back a blown render.
		if iter0.DetailIndex > 0 && fm.DetailIndex > 0 {
			gain := fm.DetailIndex/iter0.DetailIndex - 1
			s += clampf(gain*4, -2, 2)
		}
		return clampf(s, 0, 10)
	case mode.Milkyway:
		s := scoreFinish(fm, targetBg)
		// Foreground balance: the landscape must stay readable — neither crushed to black nor lifted
		// into a glowing HDR look. FgLumaMean is the bottom-rows mean luma.
		if fm.FgLumaMean > 0 {
			s -= 12 * maxf(0, 0.015-fm.FgLumaMean) // crushed foreground
			s -= 8 * maxf(0, fm.FgLumaMean-0.35)   // washed/lifted foreground
		}
		return clampf(s, 0, 10)
	default: // deepsky / nebula / comet / livestack
		s := scoreFinish(fm, targetBg)
		// Star-tint guard: a field whose bright star cores are UNIFORMLY warm is a calibration/finish
		// failure the sky-median metrics cannot see (the "all stars orange" look).
		s -= 10 * maxf(0, fm.StarWarmFrac-0.6)
		// Star-colour-variety guard: real fields mix white/blue/yellow/orange stars, so a bright-core
		// population flattened to one hue (or to grey/white — the burnt look) is a defect. Gated on a
		// positive spread so a frame with no bright cores sampled isn't penalised.
		if fm.StarColorSpread > 0 {
			s -= 12 * maxf(0, starColorSpreadMin-fm.StarColorSpread)
		}
		// Colour-disc guard: bright cores rendered as solid, over-saturated blue/magenta blobs (a dense
		// star field / cluster — the thin RGB base's chroma spread over the L star profile). The sky-median
		// and spread metrics can't see it (it's a per-core saturation, not a cast).
		s -= 10 * maxf(0, fm.StarSatFrac-starSatFracMax)
		// Background-mottle guard: coloured noise in the darkest quarter (shallow colour subs a dark stretch
		// amplifies into purple-green blotches where the sky should be neutral grey).
		s -= 8 * maxf(0, fm.BgChroma-bgChromaMax)
		return clampf(s, 0, 10)
	}
}
