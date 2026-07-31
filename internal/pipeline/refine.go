package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/comet"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/planetary"
	"github.com/verove-jordan/astronomy/internal/postprocess"
)

// RefineExistingRun re-runs ONLY the finish on an already-stacked run whose per-channel masters live
// on disk under runDir (output/<object>/<runID>/: run.json + aligned_*.fits / master_*.fits). Nothing
// is re-stacked: it reconstructs the aligned-channel map from disk and runs finishAligned (the local-
// AI-agent supervisor when opts enables it, else the standard GIMP/Siril finish), refreshing final.*
// and run.json in place. opts carries the finish dependencies (Runner/Gimp/Graxpert/Starnet/Supervisor
// /Preset/Solve/Spcc/WorkDir/OnProgress); its InputDir/OutputDir are ignored in favour of runDir.
func RefineExistingRun(ctx context.Context, opts Options, runDir string) (*postprocess.Result, error) {
	if opts.Runner == nil {
		return nil, fmt.Errorf("refine: siril runner is required")
	}
	if err := opts.Runner.Available(ctx); err != nil {
		return nil, fmt.Errorf("siril unavailable: %w", err)
	}
	outDir, err := filepath.Abs(runDir)
	if err != nil {
		return nil, err
	}
	if opts.PriorObject == "" { // run dirs are output/<object>/<runID> — the warm-start memory key
		opts.PriorObject = filepath.Base(filepath.Dir(outDir))
	}

	// Non-deepsky modes re-finish from their own persisted intermediates in outDir (no channel masters):
	// milkyway re-grades the linear composite, comet re-combines the star/comet masters, planetary re-runs
	// the sharpen/stretch over its stacked masters.
	switch opts.Preset.Mode {
	case mode.Milkyway:
		return superviseFinishNightscape(ctx, opts, outDir)
	case mode.Comet:
		return refineComet(ctx, opts, outDir)
	case mode.Planetary:
		return refinePlanetary(ctx, opts, outDir)
	case mode.Sun:
		return refineSun(ctx, opts, outDir)
	}

	prior, err := readRunJSON(outDir)
	if err != nil {
		return nil, err
	}
	channels := reconstructChannelsFromDisk(outDir, prior.Channels)
	if len(channels) == 0 {
		return nil, fmt.Errorf("refine: no channel masters (aligned_*/master_*) found in %s", outDir)
	}

	workAbs, err := filepath.Abs(opts.WorkDir)
	if err != nil {
		return nil, err
	}
	workRun := filepath.Join(workAbs, "refine_"+time.Now().Format("20060102_150405"))
	if err := fsutil.EnsureDir(workRun); err != nil {
		return nil, err
	}

	prior.OutputDir = outDir
	prior.Final = nil
	// No stepper on a refine: finishAligned's stages stream index-less lines through opts.report.
	finishAligned(ctx, opts, channels, prior, workRun, outDir)
	if prior.Final == nil {
		if n := len(prior.Warnings); n > 0 {
			return nil, fmt.Errorf("refine finish failed: %s", prior.Warnings[n-1])
		}
		return nil, fmt.Errorf("refine finish produced no result")
	}
	writeRunJSON(outDir, prior) // refresh run.json with the new finish (+ iterations)
	return prior.Final, nil
}

// ReadRunResult loads a run's record (run.json) from runDir, so a caller (e.g. the job manager after
// a refine) can surface the full updated run — channels, masters, and the refreshed final + supervised
// iterations — as the job result.
func ReadRunResult(runDir string) (*Result, error) {
	dir, err := filepath.Abs(runDir)
	if err != nil {
		return nil, err
	}
	return readRunJSON(dir)
}

// readRunJSON loads a prior run's record from outDir/run.json.
func readRunJSON(outDir string) (*Result, error) {
	b, err := os.ReadFile(filepath.Join(outDir, "run.json"))
	if err != nil {
		return nil, fmt.Errorf("read run.json: %w", err)
	}
	var res Result
	if err := json.Unmarshal(b, &res); err != nil {
		return nil, fmt.Errorf("parse run.json: %w", err)
	}
	return &res, nil
}

// refineComet re-finishes a comet run by re-combining its persisted per-channel star/comet masters under
// the supervisor. The persisted comet masters are already coma-aligned, so re-alignment is skipped (the
// zero p_mid signals that to combineCometFinish).
func refineComet(ctx context.Context, opts Options, outDir string) (*postprocess.Result, error) {
	prior, err := readRunJSON(outDir)
	if err != nil {
		return nil, err
	}
	starMasters, cometMasters := map[string]string{}, map[string]string{}
	for _, ch := range prior.Channels {
		if ch.Filter == "" {
			continue
		}
		tag := filterTag(ch.Filter)
		if fileExists(filepath.Join(outDir, "star_master_"+tag+".fits")) {
			starMasters[ch.Filter] = "star_master_" + tag
		}
		if fileExists(filepath.Join(outDir, "comet_master_"+tag+".fits")) {
			cometMasters[ch.Filter] = "comet_master_" + tag
		}
	}
	if len(starMasters) == 0 {
		return nil, fmt.Errorf("refine: no comet star masters found in %s", outDir)
	}
	return superviseFinishComet(ctx, opts, starMasters, cometMasters, len(cometMasters) > 0, comet.Point{}, outDir)
}

// refinePlanetary re-finishes a planetary run by re-running the sharpen/stretch over its persisted
// stacked+deconvolved masters under the supervisor (no re-stack, no re-deconvolution).
func refinePlanetary(ctx context.Context, opts Options, outDir string) (*postprocess.Result, error) {
	// ProcessPlanetary writes a run.json into the run dir (current layout). Its absence means this run
	// predates the per-run output directory — its masters/stack live in the shared base dir and can't be
	// refined in place — so surface that legibly instead of globbing stray masters and failing later.
	prior, rerr := readRunJSON(outDir)
	if rerr != nil {
		return nil, fmt.Errorf("refine: this planetary run predates the run-directory layout — re-process it to refine: %w", rerr)
	}
	masters := reconstructPlanetaryMasters(outDir)
	if len(masters) == 0 {
		return nil, fmt.Errorf("refine: no planetary masters (master_*.fits) found in %s", outDir)
	}
	outBase := filepath.Join(outDir, "refined_stack")
	if stacks, _ := filepath.Glob(filepath.Join(outDir, "*_stack.png")); len(stacks) > 0 {
		outBase = strings.TrimSuffix(stacks[0], ".png") // reuse the run's canonical <object>_stack base
	}
	r := &planetary.Result{Masters: masters, Sharpen: opts.Preset.Planetary.Sharpen, OutBase: outBase}

	if opts.Preset != nil && !opts.Preset.Supervise {
		// Deterministic refine (e.g. `astrostack refine -no-supervise`): re-finish once with the
		// requested preset/params, no agent loop — same contract as the deepsky path.
		std, perr := planetary.Refinish(ctx, opts.Runner, outDir, masters, opts.Preset.Planetary.Sharpen,
			opts.Preset.Planetary.Finish, opts.Preset.Planetary.Formats, outBase)
		if perr != nil {
			return nil, fmt.Errorf("refine standard finish failed: %w", perr)
		}
		final := &postprocess.Result{Mode: "planetary", Outputs: std.Outputs, Notes: std.Notes}
		prior.Final = final
		writeRunJSON(outDir, prior)
		return final, nil
	}

	final, err := superviseFinishPlanetary(ctx, opts, r)
	if err != nil {
		// Soft-fall: a refine must never hard-fail because the finish supervisor (local VLM) is unavailable
		// or the agent errored — re-finish once without the agent, matching ProcessPlanetary's contract that a
		// run never fails because of the agent. Only a failure of the plain finish itself is fatal.
		opts.report(Progress{Step: "refine finish", Line: "supervised finish unavailable (" + err.Error() + "), using standard finish"})
		std, perr := planetary.Refinish(ctx, opts.Runner, outDir, masters, opts.Preset.Planetary.Sharpen,
			opts.Preset.Planetary.Finish, opts.Preset.Planetary.Formats, outBase)
		if perr != nil {
			return nil, fmt.Errorf("refine standard finish failed (supervised finish also failed: %v): %w", err, perr)
		}
		final = &postprocess.Result{
			Mode:    "planetary",
			Outputs: std.Outputs,
			Notes:   append([]string{"standard planetary finish (supervisor unavailable: " + err.Error() + ")"}, std.Notes...),
		}
	}
	// Refresh run.json with the refined finish (+ iterations) so a reopened refine shows the new result,
	// mirroring the deepsky RefineExistingRun path. Best-effort; the finish already succeeded.
	prior.Final = final
	writeRunJSON(outDir, prior)
	return final, nil
}

// reconstructPlanetaryMasters maps each persisted master_<label>.fits in outDir to its base path (no ext).
func reconstructPlanetaryMasters(outDir string) map[string]string {
	masters := map[string]string{}
	paths, _ := filepath.Glob(filepath.Join(outDir, "master_*.fits"))
	for _, p := range paths {
		label := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "master_"), ".fits")
		masters[label] = strings.TrimSuffix(p, ".fits")
	}
	return masters
}

// reconstructChannelsFromDisk maps each prior channel filter to its on-disk master basename in outDir,
// preferring the coverage-cropped combine_<tag> (what the original combine actually consumed on a
// multi-night run), then the co-registered aligned_<tag>, then the unaligned master_<tag>. Missing
// files are skipped.
func reconstructChannelsFromDisk(outDir string, prior []ChannelResult) map[string]string {
	channels := map[string]string{}
	for _, ch := range prior {
		if ch.Filter == "" {
			continue
		}
		tag := filterTag(ch.Filter)
		switch {
		case fileExists(filepath.Join(outDir, "combine_"+tag+".fits")):
			channels[ch.Filter] = "combine_" + tag
		case fileExists(filepath.Join(outDir, "aligned_"+tag+".fits")):
			channels[ch.Filter] = "aligned_" + tag
		case fileExists(filepath.Join(outDir, "master_"+tag+".fits")):
			channels[ch.Filter] = "master_" + tag
		}
	}
	return channels
}
