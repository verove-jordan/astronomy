package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/grade"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/mosaic"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// ProcessMosaic is the tiled-panel mosaic mode: the lights are segmented into panels (one per
// pointing), each panel stacks with the full deepsky per-channel machinery, its channels are
// co-registered and the panel is plate-solved ONCE — then every channel's panel masters are
// reprojected onto one north-up TAN canvas, photometrically matched over the overlap graph and
// blended center-weighted (mosaicassemble.go). The assembled per-channel canvases feed the
// UNCHANGED standard finish (finishAligned), written as aligned_<tag>.fits so post-run Refine
// re-enters them like any deepsky run.
func ProcessMosaic(ctx context.Context, opts Options) (*Result, error) {
	if err := opts.Runner.Available(ctx); err != nil {
		return nil, fmt.Errorf("siril unavailable: %w", err)
	}
	// The union-canvas machinery is same-pointing-only (its own guard refuses offset panels); the
	// assembler owns placement and edges, so force it and the combine-time crop off.
	opts.Preset.Mosaic = false
	opts.Preset.CoverageCrop = false

	timer := newStepTimer(nil)
	if inner := opts.OnProgress; inner != nil {
		opts.OnProgress = func(p Progress) { timer.observe(p.Step); inner(p) }
	} else {
		opts.OnProgress = func(p Progress) { timer.observe(p.Step) }
	}
	defer opts.freePulledMasters(ctx)

	scanOpts := inspect.DefaultScanOptions()
	scanOpts.FilterMapping = opts.FilterMapping
	scanOpts.ExcludeSets = opts.ExcludeSets
	inv, err := opts.scanInputs(ctx, scanOpts)
	if err != nil {
		return nil, err
	}
	// Each panel is stacked by the deep-sky machinery, so a one-shot-color mosaic works the same way a
	// colour deep-sky run does: every panel becomes one RGB channel. Reprojection and the feathered
	// blend operate on whole images and never look at the filter. Only a MIXED folder still drops
	// frames — no single canvas can be assembled from panels shot on two different sensors.
	if inv.ColorModel == inspect.ColorOSC {
		markColorPreset(opts.Preset)
	} else if n := inv.ExcludeColor(); n > 0 {
		inv.Warnings = append(inv.Warnings, fmt.Sprintf(
			"%d one-shot-color frame(s) excluded — this folder also holds monochrome panels, and one "+
				"canvas cannot mix the two; assemble the colour panels from their own folder", n))
	}

	// Segment the lights into panels: plan-labeled folders/coordinates, else discovery order.
	fov := mosaicPlanFOVDeg(opts.MosaicPlan)
	source := mosaic.PanelSource(opts.Preset.MosaicPanelSource)
	frames := make([]inspect.Frame, 0, len(inv.Frames))
	for _, f := range inv.Frames {
		frames = append(frames, *f)
	}
	panels, segWarns := mosaic.SegmentPanels(frames, inv.Root, source, fov, opts.MosaicPlan)
	if len(panels) <= 1 {
		opts.report(Progress{Line: "no panel structure detected — processing as a plain deep-sky run"})
		res, perr := Process(ctx, opts)
		if res != nil {
			res.Warnings = append(res.Warnings,
				"mosaic: no panel structure detected (single pointing) — processed as a plain deep-sky run")
		}
		return res, perr
	}

	workAbs, err := filepath.Abs(opts.WorkDir)
	if err != nil {
		return nil, err
	}
	outAbs, err := filepath.Abs(opts.OutputDir)
	if err != nil {
		return nil, err
	}
	runID := time.Now().Format("20060102_150405")
	workRun := filepath.Join(workAbs, "run_"+runID)
	object := mosaicObject(opts, inv)
	outDir := filepath.Join(outAbs, object, runID)
	if err := fsutil.EnsureDir(outDir); err != nil {
		return nil, err
	}

	res := &Result{
		InputDir: opts.InputDir, OutputDir: outDir, Object: object, RunID: runID,
		Inventory: inv, Detection: inv.ChannelDetection,
		Options: runOptionsFrom(opts.Preset),
	}
	opts.PriorObject = object
	res.Warnings = append(res.Warnings, inv.Warnings...)
	res.Warnings = append(res.Warnings, segWarns...)
	for _, w := range segWarns {
		opts.report(Progress{Line: "mosaic: " + w})
	}
	mosaicSeedSolve(&opts, res, object)
	for _, w := range aiToolWarnings(ctx, opts) {
		warnLive(opts, res, w)
	}

	// Per-panel inventories + reuse plans first (cheap, no Siril) so the step bar can be sized.
	// Cross-session reuse is OFF in v1 — each panel folds only its own frames (multi-night within a
	// panel still groups per night with per-night flats + photometric normalization).
	work := buildPanelWork(ctx, opts, inv, object, panels)
	stackSlots := 0
	for _, w := range work {
		stackSlots += len(w.filters)
	}
	finishPlan := finishStepPlan(opts)
	opts.steps = newStepper(opts.report, 1+stackSlots+len(work)+mosaicAssembleSteps(work)+len(finishPlan))
	defer opts.finishSteps()
	progress := opts.beginStep

	masters, mWarn, err := buildRunMasters(ctx, opts, inv, workRun, workAbs, progress)
	if err != nil {
		return nil, err
	}
	res.Masters = masters
	res.Warnings = append(res.Warnings, mWarn...)

	gradeOpts := grade.DefaultOptions()
	if opts.Grade != nil {
		gradeOpts = *opts.Grade
	}
	flats := newFlatCache(nil)
	parity := newParityCache(opts.Runner, opts.Solve)

	solved := stackPanels(ctx, opts, res, work, masters, flats, parity, workRun, outDir, gradeOpts, progress)
	if len(solved) == 0 {
		writeRunJSON(outDir, res)
		return res, fmt.Errorf("mosaic: no panel could be stacked and plate-solved — see the run warnings")
	}

	channels, err := assembleMosaic(ctx, opts, res, solved, workRun, outDir, progress)
	if err != nil {
		writeRunJSON(outDir, res)
		return res, err
	}

	finishAligned(ctx, opts, channels, res, workRun, outDir)
	progress("export")
	if res.Final != nil {
		for _, o := range res.Final.Outputs {
			if strings.HasSuffix(o, ".png") {
				idx, tot := opts.stepPos()
				opts.report(Progress{Step: "final", Index: idx, Total: tot, Preview: o})
				capturePreview(ctx, opts, outDir, ordFinal, stageFinal, "", o, false)
				break
			}
		}
	}
	res.StagePreviews = collectStagePreviews(outDir)
	if merr := writeStageManifest(outDir, opts.Preset, runID); merr != nil {
		res.Warnings = append(res.Warnings, "stage checkpoint not written: "+merr.Error())
	}
	opts.finishSteps()
	res.Timings = timer.finish()
	if line := timingSummary(res.Timings); line != "" {
		opts.report(Progress{Line: line})
	}
	writeRunJSON(outDir, res)
	if res.Final == nil {
		return res, fmt.Errorf("mosaic assembly finished but no final image was produced — see the run warnings")
	}
	return res, nil
}

// panelWork is one panel's stacking input: its inventory subset and per-panel reuse plan.
type panelWork struct {
	panel   mosaic.Panel
	plan    *ReusePlan
	filters []string
}

// solvedPanel is a stacked + co-registered + plate-solved panel ready for assembly.
type solvedPanel struct {
	work    panelWork
	outDir  string            // outDir/panels/<label>
	aligned map[string]string // filter → aligned base name inside outDir (alignChannels contract)
	wcs     fits.WCS
	frames  int // total stacked frames across channels
}

// buildPanelWork subsets the inventory per panel (lights of the panel + every calibration frame)
// and builds each panel's single-session reuse plan.
func buildPanelWork(ctx context.Context, opts Options, inv *inspect.Inventory, object string, panels []mosaic.Panel) []panelWork {
	work := make([]panelWork, 0, len(panels))
	for _, panel := range panels {
		p := panel
		pinv := inspect.Subset(inv, func(f *inspect.Frame) bool {
			return f.Type != inspect.Light || p.Paths[f.Path]
		})
		tq := targetQueryFor(pinv, object, opts.CatalogDir)
		plan, err := buildReusePlan(ctx, ReuseConfig{}, pinv, 0, tq)
		if err != nil || plan == nil {
			continue
		}
		work = append(work, panelWork{panel: p, plan: plan, filters: orderedPlanFilters(plan)})
	}
	return work
}

// stackPanels runs the per-panel deepsky stacking loop SEQUENTIALLY (Siril is the bottleneck and
// already parallel inside; panel-level concurrency would multiply peak memory): per filter
// stackOneChannel → alignChannels (co-register + parity-normalize the panel's channels) → one
// plate-solve of the aligned broadband reference. Failures drop the panel loudly, never the run.
func stackPanels(ctx context.Context, opts Options, res *Result, work []panelWork,
	masters []calib.Master, flats *flatCache, parity *parityCache,
	workRun, outDir string, gradeOpts grade.Options, progress func(string) func(siril.Progress)) []solvedPanel {
	var solved []solvedPanel
	assembly := &MosaicResult{}
	if opts.MosaicPlan != nil {
		assembly.PlanID = opts.MosaicPlan.ID
		assembly.Grid = fmt.Sprintf("%dx%d (plan %q)", opts.MosaicPlan.Cols, opts.MosaicPlan.Rows, opts.MosaicPlan.Name)
	} else {
		assembly.Grid = fmt.Sprintf("auto: %d pointings", len(work))
	}
	res.MosaicAssembly = assembly

	for pi, w := range work {
		label := w.panel.Label
		pworkRun := filepath.Join(workRun, "panel_"+label)
		poutDir := filepath.Join(outDir, "panels", label)
		if err := fsutil.EnsureDir(poutDir); err != nil {
			mosaicDropPanel(opts, res, assembly, label, 0, "cannot create panel output dir: "+err.Error())
			continue
		}

		panelMasters := map[string]string{}
		frames := 0
		for fi, filter := range w.filters {
			stepLabel := "panel " + label + " · " + channelStepLabel(w.plan, res.Object, filter)
			prog := progress(stepLabel)
			idx, tot := opts.stepPos()
			ch := stackOneChannel(ctx, opts, w.plan, res.Object, filter, masters, flats, parity,
				pworkRun, poutDir, gradeOpts, prog, stepRef{Name: stepLabel, Index: idx, Total: tot})
			if ch.Err != "" || ch.OutputPath == "" {
				warnLive(opts, res, fmt.Sprintf("mosaic: panel %s %s failed: %s", label, filter, ch.Err))
				continue
			}
			panelMasters[filter] = ch.OutputPath
			frames += ch.StackedFrames
			if ch.PreviewPath != "" {
				idx, tot := opts.stepPos()
				opts.report(Progress{Step: "preview " + label + " " + filter, Index: idx, Total: tot, Preview: ch.PreviewPath, Session: label})
				captureSessionPreview(ctx, opts, outDir, ordSession+(pi*16+fi)*2, stageStacked, filter, label, ch.PreviewPath, false)
			}
		}
		if len(panelMasters) == 0 {
			mosaicDropPanel(opts, res, assembly, label, frames, "no channel stacked")
			continue
		}
		if frames < opts.Preset.MosaicMinPanelFrames {
			mosaicDropPanel(opts, res, assembly, label, frames, fmt.Sprintf(
				"only %d stacked frame(s) (< min_panel_frames %d)", frames, opts.Preset.MosaicMinPanelFrames))
			continue
		}

		prog := progress("panel " + label + " · co-register + solve")
		aligned := alignChannels(ctx, opts, panelMasters, filepath.Join(pworkRun, "04_aligned"), poutDir, res, prog)
		ref := mosaicReferenceFilter(aligned)
		wcs, serr := solvePanelWCS(ctx, opts, poutDir, aligned[ref], panelSolveHints(opts, w.panel))
		if serr != nil {
			mosaicDropPanel(opts, res, assembly, label, frames, "unsolvable: "+serr.Error())
			continue
		}
		assembly.Panels = append(assembly.Panels, MosaicPanelResult{
			Label: label, Frames: frames,
			SolveRA: wcs.RA0, SolveDec: wcs.Dec0, ScaleArcsecPx: wcs.ScaleArcsecPerPix(),
		})
		solved = append(solved, solvedPanel{work: w, outDir: poutDir, aligned: aligned, wcs: wcs, frames: frames})
	}
	return solved
}

func mosaicDropPanel(opts Options, res *Result, assembly *MosaicResult, label string, frames int, reason string) {
	warnLive(opts, res, fmt.Sprintf("mosaic: panel %s dropped — %s", label, reason))
	assembly.Panels = append(assembly.Panels, MosaicPanelResult{
		Label: label, Frames: frames, Dropped: true, DropReason: reason,
	})
}

// mosaicReferenceFilter picks the panel's solve reference: broadband first (L, then the canonical
// order) — narrowband rarely solves.
func mosaicReferenceFilter(aligned map[string]string) string {
	for _, f := range orderedFilters(aligned) {
		if f == "L" {
			return f
		}
	}
	return orderedFilters(aligned)[0]
}

// mosaicObject names the run: the plan's target, else the header/folder-derived object.
func mosaicObject(opts Options, inv *inspect.Inventory) string {
	if opts.MosaicPlan != nil && opts.MosaicPlan.Target != "" {
		return sanitize(opts.MosaicPlan.Target)
	}
	object := sanitize(dominantObject(inv))
	if object == "session" {
		if base := smartObject(opts.InputDir); base != "session" {
			object = base
		}
	}
	return object
}

// mosaicSeedSolve seeds plate-solving/SPCC with the plan center (authoritative) or the standard
// target ladder.
func mosaicSeedSolve(opts *Options, res *Result, object string) {
	if opts.MosaicPlan != nil {
		opts.Solve.Coords = coordHint(opts.MosaicPlan.CenterRA, opts.MosaicPlan.CenterDec)
		opts.report(Progress{Line: fmt.Sprintf("target position %s (from the mosaic plan)", opts.Solve.Coords)})
		return
	}
	if coords, source := resolveSolveCoords(opts, object); coords != "" {
		opts.Solve.Coords = coords
		opts.report(Progress{Line: fmt.Sprintf("target position %s (from %s)", coords, source)})
	} else if opts.TargetHint != "" {
		warnLive(*opts, res, fmt.Sprintf("target %q not found in the catalogues — panel plate-solving runs without a position seed", opts.TargetHint))
	}
}
