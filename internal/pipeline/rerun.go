// Durable, manually-driven "restart from a step with edited params" — the processing-view counterpart
// of the supervised finish's cost-aware tiered re-entry (supervise_reentry.go), but non-supervised and
// checkpoint-backed. An edit to a whitelisted knob is applied to the run's baseline preset (the stage
// checkpoint, checkpoint.go); the cheapest re-entry tier that reflects it is computed (tierOf,
// supervise_params.go); and the pipeline re-enters at that stage — reusing the on-disk artifacts for
// every earlier stage and overwriting the run in place. See job.Manager.Rerun for the job wiring.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// tierForStage maps a timeline stage the user restarts from to the pipeline re-entry tier that
// reproduces it and everything downstream: the "stacked" cards re-stack (C); "aligned"/"combined"/
// "colorcal" rebuild the linear prep (B); the composite milestones (deconv/starless/final) re-render
// the GIMP composite only (A). Unknown / empty → the cheapest tier (A).
func tierForStage(stage string) tier {
	switch stage {
	case stageStacked:
		return tierC
	case stageAligned, stageCombined, stageColorCal:
		return tierB
	default: // deconv, starless, final, "" or unknown
		return tierA
	}
}

// rerunModeSupported reports whether RerunFromStage handles a mode. The deepsky family (deepsky /
// nebula / livestack) uses the full A/B/C tier model and the LRGB composite where the editable knobs
// (incl. lum_opacity) live; the other modes re-finish through their own Refine path instead.
func rerunModeSupported(m mode.Mode) bool {
	return m == mode.Deepsky || m == mode.Nebula || m == mode.Livestack
}

// RerunFromStage re-runs an already-stacked deepsky/nebula run from the stage a parameter edit
// requires, in place. patch is a JSON object of knob overrides (validated + clamped by ApplyParamPatch);
// floorStage optionally forces re-entry no cheaper than a stage the user picked on the timeline. It
// reuses the on-disk artifacts below the re-entry tier — the persisted linear prep for a Tier-A
// composite tweak (seconds), the channel masters for a Tier-B prep rebuild, the raw frames for a
// Tier-C re-stack — overwrites final.* (keeping the previous as final_prev.png) and refreshes run.json
// + the stage checkpoint. opts carries the finish dependencies and the mode preset the job resolved.
func RerunFromStage(ctx context.Context, opts Options, runDir string, patch json.RawMessage, floorStage string) (*Result, error) {
	if opts.Runner == nil {
		return nil, fmt.Errorf("rerun: siril runner is required")
	}
	if opts.Preset == nil {
		return nil, fmt.Errorf("rerun: preset is required")
	}
	if err := opts.Runner.Available(ctx); err != nil {
		return nil, fmt.Errorf("siril unavailable: %w", err)
	}
	// A rerun re-renders the layered GIMP composite (where lum_opacity/saturation and the other editable
	// knobs live), so GIMP is required — there is no Siril fallback that would honour the composite edits.
	if opts.Gimp == nil || opts.Gimp.Available() != nil {
		return nil, fmt.Errorf("rerun: GIMP is required to re-render the composite")
	}
	if !rerunModeSupported(opts.Preset.Mode) {
		return nil, fmt.Errorf("rerun: editable per-stage rerun is available for deep-sky/nebula runs; use Refine for %s", opts.Preset.Mode)
	}
	outDir, err := filepath.Abs(runDir)
	if err != nil {
		return nil, err
	}
	if opts.PriorObject == "" { // run dirs are output/<object>/<runID> — lets starClusterTarget resolve the object (mirrors refine.go)
		opts.PriorObject = filepath.Base(filepath.Dir(outDir))
	}
	prior, err := readRunJSON(outDir)
	if err != nil {
		return nil, err
	}

	// Baseline = the preset behind the current on-disk artifacts (the stage checkpoint), falling back to
	// the preset the job resolved from its stored params when no manifest was written (older runs).
	baseline := *opts.Preset
	if m, ok := readStageManifest(outDir); ok {
		baseline = m.Preset
	}
	next := baseline
	patchRes, err := ApplyParamPatch(&next, patch)
	if err != nil {
		return nil, err
	}
	// A star cluster re-derives its gentle finish profile here (patch-preserving) so a rerun reproduces a
	// fresh run's cluster finish — the profile is a finish-time derivation, never persisted in the preset
	// (see finishAligned). On an older run whose manifest is the stock preset this bumps the re-entry to
	// Tier B, which correctly rebuilds the prep at the cluster headroom.
	if starClusterTarget(opts) {
		next = applyClusterProfile(next)
	}
	// Re-enter at the higher (more expensive) of the param's own tier and the stage the user chose.
	t := tierOf(baseline, next)
	if floor := tierForStage(floorStage); floor > t {
		t = floor
	}
	opts.Preset = &next
	opts.report(Progress{Step: "rerun", Line: fmt.Sprintf(
		"re-entering at tier %s (changed: %s)", t, changedSummary(patchRes))})

	workRun := filepath.Join(filepath.Dir(outDir), ".rerun_"+filepath.Base(outDir))
	if err := fsutil.EnsureDir(workRun); err != nil {
		return nil, err
	}
	// Keep the current final for A/B comparison and as a safety net (a failed rerun leaves it intact).
	backupFinal(outDir)

	final, channels, err := rerunFinish(ctx, opts, t, prior, workRun, outDir)
	if err != nil {
		return nil, err
	}
	if final == nil {
		return nil, fmt.Errorf("rerun produced no result")
	}

	// Milestone: the new final (Process captures this after combine(); a rerun must too).
	captureFinalPreview(ctx, opts, outDir, final)

	prevFinal := prior.Final
	prior.Final = final
	// Mono side-outputs (final_luminance / final_monostack): a Tier-B/C rerun rebuilt the prep they
	// derive from, so re-render them with the new knobs; a Tier-A composite tweak leaves the files
	// valid but the fresh Final record starts empty and would silently drop them from run.json and
	// the results UI — carry the previous listing forward instead.
	if t >= tierB {
		emitMonoOutputs(ctx, opts, channels, prior, workRun, outDir)
	} else {
		carryMonoOutputs(prevFinal, prior.Final)
	}
	prior.Options = runOptionsFrom(&next)
	if len(channels) > 0 {
		prior.Channels = filterChannelRecords(prior.Channels, channels)
	}
	prior.Warnings = append(prior.Warnings, fmt.Sprintf("rerun from tier %s (%s)", t, changedSummary(patchRes)))
	prior.StagePreviews = collectStagePreviews(outDir)
	writeRunJSON(outDir, prior)
	if merr := writeStageManifest(outDir, &next, prior.RunID); merr != nil {
		prior.Warnings = append(prior.Warnings, "stage checkpoint not refreshed: "+merr.Error())
	}
	return prior, nil
}

// rerunFinish re-enters the finish at tier t, reusing on-disk artifacts below it, and returns the new
// final plus the channel-master map it composited. Tier A reuses the persisted linear prep; Tier B
// rebuilds the prep from the channel masters; Tier C re-stacks from the raw frames first.
func rerunFinish(ctx context.Context, opts Options, t tier, prior *Result, workRun, outDir string) (*postprocess.Result, map[string]string, error) {
	if t >= tierC {
		sc, err := reconstructStackContext(ctx, opts, outDir, workRun)
		if err != nil {
			return nil, nil, fmt.Errorf("re-stack setup: %w", err)
		}
		channels, err := reStack(ctx, opts, opts.Preset, sc.inv, sc.plan, sc.masters, sc.flats, sc.parity, sc.workRun, outDir, sc.object)
		if err != nil {
			return nil, nil, fmt.Errorf("re-stack: %w", err)
		}
		recaptureStackedPreviews(ctx, opts, outDir, channels)
		final, _, err := finishWithGimp(ctx, opts, channels, workRun, outDir)
		return final, channels, err
	}

	channels := reconstructChannelsFromDisk(outDir, prior.Channels)
	if len(channels) == 0 {
		return nil, nil, fmt.Errorf("no channel masters (aligned_*/master_*) found in %s", outDir)
	}
	if t == tierA {
		if in, notes, ok := loadLinearPrep(outDir); ok {
			final, err := finishComposite(ctx, opts, in, notes, channels, outDir)
			return final, channels, err
		}
		// No persisted prep (an older run) → rebuild it (Tier B); still correct, just not instant.
		opts.report(Progress{Step: "rerun", Line: "no persisted linear prep — rebuilding it for this composite tweak"})
	}
	final, _, err := finishWithGimp(ctx, opts, channels, workRun, outDir)
	return final, channels, err
}

// stackContext bundles what reStack needs to re-stack an existing run from its raw frames: the scanned
// inventory, the cross-session reuse plan, the calibration masters and per-run caches, and the object
// name. Rebuilt from the run's inputs so a Tier-C rerun re-enters the stack exactly as the run did.
type stackContext struct {
	inv     *inspect.Inventory
	plan    *ReusePlan
	masters []calib.Master
	flats   *flatCache
	parity  *parityCache
	object  string
	workRun string
}

// reconstructStackContext rebuilds the stacking inputs for an existing run so a Tier-C rerun can
// re-stack from the raw frames. It rescans the run's inputs, rebuilds/reuses the calibration masters
// (reusing the .sig-guarded library masters, unchanged), and reconstructs the cross-session reuse
// plan. Passing currentSession=0 is faithful: the current frames come from the scan and priors are
// path-deduped against them (addPriorGroups), so nothing is double-counted.
func reconstructStackContext(ctx context.Context, opts Options, outDir, workRun string) (*stackContext, error) {
	scanOpts := inspect.DefaultScanOptions()
	scanOpts.FilterMapping = opts.FilterMapping
	inv, err := inspect.ScanMany(ctx, opts.scanRoots(), scanOpts)
	if err != nil {
		return nil, fmt.Errorf("scan inputs: %w", err)
	}
	inv.ExcludeBayer()

	workAbs, err := filepath.Abs(opts.WorkDir)
	if err != nil {
		return nil, err
	}
	object := sanitize(dominantObject(inv))
	if object == "session" {
		if base := smartObject(opts.InputDir); base != "session" {
			object = base
		}
	}

	silent := func(string) func(siril.Progress) { return func(siril.Progress) {} }
	masters, _, err := buildRunMasters(ctx, opts, inv, workRun, workAbs, silent)
	if err != nil {
		return nil, fmt.Errorf("calibration masters: %w", err)
	}
	plan, _ := buildReusePlan(ctx, opts.Reuse, inv, 0, targetQueryFor(inv, dominantObject(inv), opts.CatalogDir))
	return &stackContext{
		inv:     inv,
		plan:    plan,
		masters: masters,
		flats:   newFlatCache(opts.Reuse.Provider),
		parity:  newParityCache(opts.Runner, opts.Solve),
		object:  object,
		workRun: workRun,
	}, nil
}

// backupFinal copies the run's current final.png to final_prev.png so a rerun keeps the previous image
// for an A/B comparison and as a safety net. Best-effort — a run with no final yet just skips it.
func backupFinal(outDir string) {
	cur := filepath.Join(outDir, "final.png")
	if fileExists(cur) {
		_ = fsutil.CopyFile(cur, filepath.Join(outDir, "final_prev.png"))
	}
}

// captureFinalPreview stamps the final-milestone timeline preview from the run's new final PNG,
// mirroring Process after combine() (finishComposite captures only the star-reduced variant).
func captureFinalPreview(ctx context.Context, opts Options, outDir string, final *postprocess.Result) {
	if final == nil {
		return
	}
	for _, o := range final.Outputs {
		if strings.HasSuffix(o, ".png") {
			capturePreview(ctx, opts, outDir, ordFinal, stageFinal, "", o, false)
			return
		}
	}
}

// recaptureStackedPreviews refreshes the per-channel "stacked" timeline previews from the master
// preview PNGs a Tier-C re-stack just rewrote, in the canonical channel order so they sort stably.
func recaptureStackedPreviews(ctx context.Context, opts Options, outDir string, channels map[string]string) {
	i := 0
	for _, filter := range filterOrder {
		if _, ok := channels[filter]; !ok {
			continue
		}
		preview := filepath.Join(outDir, "master_"+filterTag(filter)+"_preview.png")
		if fileExists(preview) {
			capturePreview(ctx, opts, outDir, ordStacked+i, stageStacked, filter, preview, false)
		}
		i++
	}
}

// filterChannelRecords keeps only the prior channel records whose filter is still in the freshly
// composited set (a re-stack may drop a channel that now fails grading), so the run record matches
// what was actually combined.
func filterChannelRecords(prior []ChannelResult, channels map[string]string) []ChannelResult {
	kept := make([]ChannelResult, 0, len(prior))
	for _, ch := range prior {
		if _, ok := channels[ch.Filter]; ok {
			kept = append(kept, ch)
		}
	}
	if len(kept) == 0 {
		return prior // never blank the record if the map keys diverged (e.g. OIII-as-B)
	}
	return kept
}

// changedSummary renders a patch result's changed knobs (and any ignored keys) for the rerun log.
func changedSummary(r ParamPatchResult) string {
	s := strings.Join(r.Changed, ", ")
	if s == "" {
		s = "no param change"
	}
	if len(r.Ignored) > 0 {
		s += "; ignored: " + strings.Join(r.Ignored, ", ")
	}
	return s
}
