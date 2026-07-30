// Gated, deterministic post-finish star repair. After the standard deep-sky finish, autoFixStars measures
// the exported image for burnt / colour-flattened / uniformly-warm star cores and — ONLY when it finds
// fixable ones — re-enters the finish at the cheapest phase that can fix them (Tier B re-stretch with more
// highlight headroom, or a cheap Tier-A colour pass), keeping the best-scoring pass. A clean finish is a
// no-op, so a good run pays nothing. It needs no vision model (distinct from the opt-in Supervise loop);
// it reuses the same staged re-entry (supervise_reentry.go), param whitelist and deterministic score.
package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/siril"
)

const (
	starFixDefaultIters  = 2    // repair passes when the preset doesn't set a cap
	starFixMaxIters      = 4    // hard ceiling on repair passes
	starFixMinGain       = 0.15 // a pass must beat the current best score by this to be adopted
	starFixHeadroomStep  = 0.07 // how much more headroom a burnt-core pass adds
	starFixHeadroomFloor = 0.75 // never cap linear highlights below this
	starFixCeilStep      = 0.02 // how much the composite highlight ceiling is pulled down per pass
	starFixSatStep       = 0.03 // saturation nudge for a colour pass
	starFixSatCap        = 0.20 // never push saturation past this chasing star colour
	starFixDefaultCeil   = 0.95 // assumed composite ceiling when the preset leaves it unset (0)
	starFixDesatStep     = 0.20 // star-core desaturation added per colour-disc pass
	starFixChromaStep    = 2.0  // px of chroma blur added per background-mottle pass
	starFixChromaCap     = 8.0  // never blur colour past this chasing mottle
)

// autoFixStars runs the gated repair and mutates res in place (promotes a better final, appends notes and
// iteration records). Soft-fail: any error leaves res.Final as the standard finish produced it.
func autoFixStars(ctx context.Context, opts Options, channels map[string]string, workRun, outDir string, res *Result) {
	if !autoFixStarsEnabled(ctx, opts, res) {
		return
	}
	finalPng := finalPNG(res)
	rgbBase := filepath.Join(outDir, "rgb_base.fits")
	if finalPng == "" || !fileExists(rgbBase) {
		return
	}
	m, err := measureFinish(finalPng)
	if err != nil {
		return
	}
	report, err := analyzeStars(rgbBase, m)
	if err != nil || !report.needsFix() {
		return // clean (or cannot judge) → zero extra cost
	}
	fixProg := opts.beginStep("star quality check")
	fixProg(siril.Progress{Line: fmt.Sprintf(
		"star-fix: %d stars, burnt %.2f%%, colour spread %.3f — repairing", report.Detected, report.FinalBurnt*100, report.FinalSpread)})
	runStarFix(ctx, opts, channels, workRun, outDir, res, m, report)
}

// autoFixStarsEnabled gates the repair: opted in, a deep-sky-like composite mode, GIMP ready, and NOT the
// supervised path (the VLM loop already converges on stars, and it returns before we are called).
func autoFixStarsEnabled(ctx context.Context, opts Options, res *Result) bool {
	if opts.Preset == nil || !opts.Preset.AutoFixStars || res == nil || res.Final == nil {
		return false
	}
	if !isStarFixMode(opts.Preset.Mode) || superviseOn(ctx, opts) {
		return false
	}
	return opts.Gimp != nil && opts.Gimp.Available() == nil
}

// runStarFix iterates propose → re-render → score, keeping the best pass and promoting it to final.*.
func runStarFix(ctx context.Context, opts Options, channels map[string]string, workRun, outDir string, res *Result, m0 finishMetrics, report0 starReport) {
	re, err := newReentry(opts, channels, workRun, outDir)
	if err != nil {
		return
	}
	if base, _, ok := loadLinearPrep(outDir); ok {
		re.base = &base // seed the Tier-A cache so a colour-only pass re-renders in seconds
	}
	working := *opts.Preset
	bestScore := scoreFinishMode(working.Mode, m0, working.BackgroundLevel, finishMetrics{})
	rgbBase := filepath.Join(outDir, "rgb_base.fits")
	report := report0
	var records []postprocess.IterationRecord
	var best *renderResult

	for i := 0; i < starFixIters(opts.Preset); i++ {
		if ctx.Err() != nil {
			break
		}
		patch, ok := proposeStarFix(report, working)
		if !ok {
			break
		}
		next := clampPreset(patch.apply(working))
		g, err := re.render(ctx, tierOf(working, next), next, filepath.Join(outDir, fmt.Sprintf("final_star%d", i)))
		if err != nil {
			break
		}
		m, err := measureFinish(g.Png)
		if err != nil {
			break
		}
		rep2, err := analyzeStars(rgbBase, m)
		if err != nil {
			break
		}
		score := scoreFinishMode(next.Mode, m, next.BackgroundLevel, finishMetrics{})
		records = append(records, starFixRecord(i, tierOf(working, next), g.Png, score, next))
		opts.report(Progress{Step: fmt.Sprintf("star-fix %d/%d", i+1, starFixIters(opts.Preset)), Preview: g.Png})
		if score <= bestScore+starFixMinGain {
			break // no real improvement → keep what we already have
		}
		rr := renderResult{Png: g.Png, Tif: g.Tif, Xcf: g.Xcf}
		best, bestScore, working, report = &rr, score, next, rep2
		if !report.needsFix() {
			break // converged
		}
	}
	if best != nil {
		finalizeStarFix(ctx, opts, outDir, res, working, *best, records, report0, report)
	}
}

// proposeStarFix picks the next deterministic parameter change for the measured star defects, and the
// cheapest phase that can fix it: burnt cores → more stretch headroom (Tier B); a warm or flattened
// colour → a Tier-A composite tweak. Returns false when nothing actionable remains.
func proposeStarFix(r starReport, p mode.Preset) (supervisePatch, bool) {
	if r.FinalBurnt > whiteClipMax { // blown cores: only a re-stretch with more headroom un-burns them
		hr := currentHeadroom(p) - starFixHeadroomStep
		if hr < starFixHeadroomFloor {
			hr = starFixHeadroomFloor
		}
		if hr < currentHeadroom(p)-1e-9 {
			ceil := composeCeil(p) - starFixCeilStep
			return supervisePatch{StretchHeadroom: &hr, HighlightCeil: &ceil}, true
		}
	}
	if r.FinalSatFrac > starSatFracMax && r.TrueSat < 0.5*r.FinalSatFrac { // over-saturated colour discs
		sd := clampf(p.StarDesat+starFixDesatStep, 0, 1)
		if sd > p.StarDesat+1e-9 { // desaturate the bright cores toward white + trim saturation (both Tier A)
			sat := clampf(p.Saturation-starFixSatStep, 0, starFixSatCap)
			return supervisePatch{StarDesat: &sd, Saturation: &sat}, true
		}
	}
	if r.FinalBgChroma > bgChromaMax { // purple-green background mottle → blur the colour base more (Tier A)
		cb := clampf(p.ChromaBlur+starFixChromaStep, 0, starFixChromaCap)
		if cb > p.ChromaBlur+1e-9 {
			return supervisePatch{ChromaBlur: &cb}, true
		}
	}
	if r.FinalWarm > starWarmFracMax && r.TrueSpread > starTrueSpreadFloor { // uniformly warm cores
		exclude := true
		sat := clampf(p.Saturation-starFixSatStep, 0, starFixSatCap)
		return supervisePatch{HaExcludeStars: &exclude, Saturation: &sat}, true
	}
	if r.TrueSat > starTrueSatFloor && r.FinalSpread > 0 && r.FinalSpread < starColorSpreadMin { // flattened colour
		sat := clampf(p.Saturation+starFixSatStep, 0, starFixSatCap)
		ceil := composeCeil(p) - starFixCeilStep
		if sat > p.Saturation+1e-9 || ceil < composeCeil(p)-1e-9 {
			return supervisePatch{Saturation: &sat, HighlightCeil: &ceil}, true
		}
	}
	return supervisePatch{}, false
}

// currentHeadroom reads the working stretch headroom, treating "off" (0 or ≥1) as 1.0 so a first burnt
// pass turns it on.
func currentHeadroom(p mode.Preset) float64 {
	if p.StretchHeadroom <= 0 || p.StretchHeadroom > 1 {
		return 1.0
	}
	return p.StretchHeadroom
}

// composeCeil is the working composite highlight ceiling, defaulting when unset (0 = shoulder off).
func composeCeil(p mode.Preset) float64 {
	if p.HighlightCeil <= 0 {
		return starFixDefaultCeil
	}
	return p.HighlightCeil
}

// finalizeStarFix promotes the winning render to final.*, regenerates StarNet on it when configured, and
// records the repair on the result (notes + iterations).
func finalizeStarFix(ctx context.Context, opts Options, outDir string, res *Result, working mode.Preset, best renderResult, records []postprocess.IterationRecord, before, after starReport) {
	if res.Final == nil {
		return
	}
	if err := promoteResult(best, filepath.Join(outDir, "final")); err != nil {
		opts.report(Progress{Step: "star-fix", Line: "warn: promote repaired finish failed: " + err.Error()})
		return
	}
	star := opts
	star.Preset = &working
	if aiStars(ctx, star) { // regenerate final_reduced.* on the promoted winner (paths already in Outputs)
		reduceStarsAI(ctx, star, filepath.Join(outDir, "final.tif"), outDir, nil)
	}
	res.Final.Notes = append(res.Final.Notes, starFixNote(before, after, len(records)))
	res.Final.Iterations = append(res.Final.Iterations, records...)
	opts.report(Progress{Step: "star-fix", Line: starFixNote(before, after, len(records))})
}

func starFixRecord(i int, t tier, png string, score float64, p mode.Preset) postprocess.IterationRecord {
	return postprocess.IterationRecord{
		Index: i, Tier: t.String(), PngPath: png,
		DetScore: score, CombinedScore: score,
		Reasoning: "deterministic star repair", Params: paramsMap(p),
	}
}

func starFixNote(before, after starReport, passes int) string {
	return fmt.Sprintf("auto star-fix: %d pass(es) — burnt %.2f%%→%.2f%%, colour spread %.3f→%.3f",
		passes, before.FinalBurnt*100, after.FinalBurnt*100, before.FinalSpread, after.FinalSpread)
}

func starFixIters(p *mode.Preset) int {
	n := p.StarFixMaxIters
	if n <= 0 {
		n = starFixDefaultIters
	}
	if n > starFixMaxIters {
		n = starFixMaxIters
	}
	return n
}

// finalPNG returns the composite final PNG (the first .png in the result outputs — before any StarNet
// star-reduced variant).
func finalPNG(res *Result) string {
	if res == nil || res.Final == nil {
		return ""
	}
	for _, o := range res.Final.Outputs {
		if strings.HasSuffix(o, ".png") {
			return o
		}
	}
	return ""
}
