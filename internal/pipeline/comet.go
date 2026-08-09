package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/comet"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/grade"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/starnet"
)

const cometHdr = "requires 1.2.0\nsetext fits\nset32bits\n"

// ProcessComet stacks a moving comet twice over the SAME calibrated frames — once star-aligned and once
// comet-aligned — then recomposites them so the final image has a sharp comet AND sharp pinpoint stars.
// All frames register to ONE global middle-frame reference, so the comet's per-frame position is a single
// linear track and the per-channel star/comet masters share one coordinate system (no re-alignment). The
// per-channel masters are combined into a colour star image and a colour comet image; StarNet then lifts
// the stars as a layer and screens them back over the starless comet.
func ProcessComet(ctx context.Context, opts Options) (*Result, error) {
	if err := opts.Runner.Available(ctx); err != nil {
		return nil, fmt.Errorf("siril unavailable: %w", err)
	}
	scanOpts := inspect.DefaultScanOptions()
	scanOpts.FilterMapping = opts.FilterMapping
	inv, err := inspect.ScanMany(ctx, opts.scanRoots(), scanOpts)
	if err != nil {
		return nil, err
	}
	// Comet mode keeps every light, mono or colour. It never needed the deep-sky path's Bayer veto:
	// the spurious-BAYERPAT case (an older ASICAP capture of a MONO camera behind a filter wheel) is
	// resolved during inspection, so a pattern that survives to here is real. A one-shot-color comet
	// stacks as a single RGB channel — the dual star/comet stack and the motion track work on whole
	// frames and never look at the filter — so it needs nothing beyond CFA-aware calibration below.
	if inv.ColorModel == inspect.ColorOSC {
		markColorPreset(opts.Preset)
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
	object := cometObject(inv, opts.InputDir)
	outDir := filepath.Join(outAbs, object, runID)
	if err := fsutil.EnsureDir(outDir); err != nil {
		return nil, err
	}
	res := &Result{
		InputDir: opts.InputDir, OutputDir: outDir, Object: object, RunID: runID,
		Inventory: inv, Detection: inv.ChannelDetection,
	}
	opts.PriorObject = object // key for the supervisor's cross-run memory (warm start)
	res.Warnings = append(res.Warnings, inv.Warnings...)
	res.Warnings = append(res.Warnings, aiToolWarnings(ctx, opts)...)

	opts.report(Progress{Step: "building master calibration frames", Index: 1, Total: 4})
	masters, mWarn, err := calib.BuildMasters(ctx, opts.Runner, inv, filepath.Join(workRun, "masters"), workRun,
		opts.masterStacks(), opts.sirilLines("masters"))
	if err != nil {
		return nil, err
	}
	res.Masters = masters
	res.Warnings = append(res.Warnings, mWarn...)

	gradeOpts := grade.DefaultOptions()
	if opts.Grade != nil {
		gradeOpts = *opts.Grade
	}

	// 1. Calibrate each channel, merge all calibrated frames into one sequence.
	mergedDir, mframes, cWarn := calibrateAndMergeComet(ctx, opts, inv, masters, workRun)
	res.Warnings = append(res.Warnings, cWarn...)
	if len(mframes) == 0 {
		return nil, fmt.Errorf("comet: no calibratable light frames found")
	}

	// 2. Global star-align to the session-middle frame.
	opts.report(Progress{Step: "star-aligning all frames", Index: 2, Total: 4})
	times := datesOf(mframes)
	midIdx := comet.MiddleFrameIndex(times) + 1 // setref is 1-based
	if _, err := opts.Runner.Run(ctx, mergedDir, siril.CalibrateStarAlignToRefScript("light", siril.CalibMasters{}, midIdx), opts.sirilLines("star-aligning all frames")); err != nil {
		return nil, fmt.Errorf("comet star alignment: %w", err)
	}
	metrics := gradeMergedComet(mergedDir, mframes, gradeOpts, res)
	// Milestone: a representative star-aligned frame (before stacking).
	if aligned, _ := filepath.Glob(filepath.Join(mergedDir, "r_light_*.fits")); len(aligned) > 0 {
		capturePreview(ctx, opts, outDir, ordAligned, stageAligned, "", aligned[len(aligned)/2], true)
	}

	// 3. Locate the comet and build its linear track across the common coordinate system.
	track, haveTrack := cometTrack(opts, mergedDir, mframes, metrics, res)
	pMid := track.At(comet.MidTime(times))

	// 4. Per channel: a star stack and a comet stack from the same globally-aligned frames.
	opts.report(Progress{Step: "stacking star + comet per channel", Index: 3, Total: 4})
	starMasters, cometMasters := stackChannelsDual(ctx, opts, mergedDir, mframes, metrics, track, pMid, haveTrack, workRun, outDir, res)
	// Milestone: each channel's stacked star master (stable order by filter).
	captureStackedMasters(ctx, opts, outDir, starMasters)

	// 5. Combine each side into a colour image, then StarNet-separate and screen the stars back. When the
	// finish supervisor is opted in, it re-tunes the colour composite instead; soft-fall to the standard finish.
	opts.report(Progress{Step: "compositing comet + stars", Index: 4, Total: 4})
	if superviseOn(ctx, opts) {
		if final, err := superviseFinishComet(ctx, opts, starMasters, cometMasters, haveTrack, pMid, outDir); err != nil {
			res.Warnings = append(res.Warnings, "supervised comet finish failed, using standard finish: "+err.Error())
			finishComet(ctx, opts, res, starMasters, cometMasters, haveTrack, pMid, outDir)
		} else {
			res.Final = final
		}
	} else {
		finishComet(ctx, opts, res, starMasters, cometMasters, haveTrack, pMid, outDir)
	}

	stampFinishQuality(res) // objective colour/clipping guardrails on every run
	if res.Final != nil {
		for _, o := range res.Final.Outputs {
			if filepath.Ext(o) == ".png" {
				opts.report(Progress{Step: "final", Index: 4, Total: 4, Preview: o})
				capturePreview(ctx, opts, outDir, ordFinal, stageFinal, "", o, false) // milestone: the final image
				break
			}
		}
	}
	res.StagePreviews = collectStagePreviews(outDir) // persist the milestone timeline for reload
	writeRunJSON(outDir, res)
	return res, nil
}

func cometObject(inv *inspect.Inventory, inputDir string) string {
	if o := sanitize(dominantObject(inv)); o != "session" {
		return o
	}
	if base := smartObject(inputDir); base != "session" && base != "" {
		return base
	}
	return "comet"
}

func countFilter(frames []*inspect.Frame, filter string) int {
	n := 0
	for _, f := range frames {
		if f.Filter == filter {
			n++
		}
	}
	return n
}

func datesOf(frames []*inspect.Frame) []int64 {
	out := make([]int64, len(frames))
	for i, f := range frames {
		out[i] = f.DateObsMs
	}
	return out
}

// regCometPath is the registered (star-aligned) frame for the merged-sequence index i (0-based).
func regCometPath(mergedDir string, i int) string {
	return filepath.Join(mergedDir, fmt.Sprintf("r_light_%05d.fits", i+1))
}

func regPaths(mergedDir string, idxs []int) []string {
	out := make([]string, len(idxs))
	for k, i := range idxs {
		out[k] = regCometPath(mergedDir, i)
	}
	return out
}

// calibrateAndMergeComet calibrates each light channel with its matched masters and symlinks every
// calibrated frame into one merged sequence dir, returning the dir and the frames in merge order (their
// filter + DATE-OBS drive the per-channel split and the comet track).
func calibrateAndMergeComet(ctx context.Context, opts Options, inv *inspect.Inventory, masters []calib.Master,
	workRun string) (mergedDir string, mframes []*inspect.Frame, warnings []string) {
	mergedDir = filepath.Join(workRun, "01_merged")
	var calibrated []string
	for _, set := range inv.SetsOfType(inspect.Light) {
		sel := calib.MatchForLightExcluding(set.Key, masters, opts.CalibExclude, opts.ForceCalibration)
		dark, flat, bias := sel.Masters()
		cm := siril.CalibMasters{Dark: dark, Flat: flat, Bias: bias, BadPixelMap: calib.DefectsListFor(dark),
			CFA: needsDebayer(set.Frames)}
		// The night token keeps two per-night sets of one filter+exposure (multi-night scan) in
		// separate dirs — without it their calibrated frames would silently mix in one sequence.
		setDir := filepath.Join(workRun, "cal_"+sanitize(set.Key.Filter)+"_"+fmt.Sprint(set.Key.ExposureMs)+nightToken(set.Key.Session))
		if _, err := fsutil.LinkFrames(setDir, framePaths(set.Frames)); err != nil {
			warnings = append(warnings, "comet: link "+set.Key.Filter+": "+err.Error())
			continue
		}
		if _, err := opts.Runner.Run(ctx, setDir, siril.CalibrateOnlyScriptWith("light", cm, seqIngest(set.Frames)), nil); err != nil {
			warnings = append(warnings, "comet: calibrate "+set.Key.Filter+": "+err.Error())
			continue
		}
		paths, _ := filepath.Glob(filepath.Join(setDir, siril.CalibratedSeq("light", cm)+"_*.fits"))
		sort.Strings(paths)
		for i, p := range paths {
			if i < len(set.Frames) {
				mframes = append(mframes, set.Frames[i])
				calibrated = append(calibrated, p)
			}
		}
	}
	if _, err := fsutil.LinkFrames(mergedDir, calibrated); err != nil {
		warnings = append(warnings, "comet: merge frames: "+err.Error())
	}
	return mergedDir, mframes, warnings
}

// gradeMergedComet grades the globally-registered sequence (soft-fail: on any error nothing is rejected).
func gradeMergedComet(mergedDir string, mframes []*inspect.Frame, gradeOpts grade.Options, res *Result) []grade.Metric {
	metrics, _, _, err := gradeChannel(mergedDir, "light", mframes, gradeOpts, false, nil, nil)
	if err != nil || len(metrics) != len(mframes) {
		if err != nil {
			res.Warnings = append(res.Warnings, "comet: grading skipped (stacking all registered frames): "+err.Error())
		}
		metrics = make([]grade.Metric, len(mframes))
	}
	return metrics
}

// cometMaxDetect caps how many frames are centroided for the track fit (time-spread); ~40 is plenty to
// average out single-frame coma-centroid noise while keeping detection a small fraction of the run.
const cometMaxDetect = 40

// cometTrack builds the comet's motion track. It prefers the preset's manual 2-point override; otherwise
// it auto-detects the coma centroid in many time-spread registered frames and **robustly fits** the motion
// line. A single diffuse coma is too noisy to centroid reliably, so a 2-point track misregisters the comet
// per channel (the visible R/G/B colour separation) — averaging many detections is what fixes it.
func cometTrack(opts Options, mergedDir string, mframes []*inspect.Frame, metrics []grade.Metric, res *Result) (comet.Tracker, bool) {
	order := survivorsByTime(mergedDir, mframes, metrics)
	if len(order) < 2 {
		res.Warnings = append(res.Warnings, "comet: no timestamped registered frames — comet alignment skipped")
		return comet.Track{}, false
	}
	if tr, ok := manualTrack(opts, mframes, order); ok {
		return tr, true
	}
	obs := detectComet(mergedDir, mframes, order)
	tr, kept, ok := comet.FitBestTrack(obs)
	if !ok {
		res.Warnings = append(res.Warnings, "comet: could not detect the comet in enough frames (provide a manual position) — comet alignment skipped")
		return comet.Track{}, false
	}
	// Consistency acceptance: the detection threshold is deliberately permissive (4σ picks up a
	// faint coma), so a track is trusted only when the per-frame detections AGREE — at least 4
	// survivors AND two-thirds of the detections on the fitted motion. A "track" through scattered
	// noise hits would smear the comet stack worse than star-aligned-only.
	if kept < 4 || kept*3 < len(obs)*2 {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"comet: detections too inconsistent for a trustworthy track (%d/%d on the fit) — comet alignment skipped (provide a manual position)",
			kept, len(obs)))
		return comet.Track{}, false
	}
	if _, quad := tr.(comet.QuadTrack); quad {
		res.Warnings = append(res.Warnings, fmt.Sprintf("comet: curved (quadratic) motion track fitted from %d/%d detections", kept, len(obs)))
	} else {
		res.Warnings = append(res.Warnings, fmt.Sprintf("comet: motion track fitted from %d/%d detections", kept, len(obs)))
	}
	return tr, true
}

// survivorsByTime returns registered, graded-in, timestamped frame indices in chronological order.
func survivorsByTime(mergedDir string, mframes []*inspect.Frame, metrics []grade.Metric) []int {
	var idx []int
	for i := range mframes {
		if metrics[i].Rejected || mframes[i].DateObsMs == 0 || !fileExists(regCometPath(mergedDir, i)) {
			continue
		}
		idx = append(idx, i)
	}
	sort.Slice(idx, func(a, b int) bool { return mframes[idx[a]].DateObsMs < mframes[idx[b]].DateObsMs })
	return idx
}

// detectComet auto-detects the coma centroid in up to cometMaxDetect time-spread frames (only confident
// detections are returned; faint frames where nothing stands out are skipped, not added as bad points).
func detectComet(mergedDir string, mframes []*inspect.Frame, order []int) []comet.Obs {
	step := 1
	if len(order) > cometMaxDetect {
		step = len(order) / cometMaxDetect
	}
	var obs []comet.Obs
	for k := 0; k < len(order); k += step {
		i := order[k]
		// blurRadius 0 → multi-scale detection (compact bright coma AND large diffuse coma both lock).
		if p, ok, err := comet.DetectFile(regCometPath(mergedDir, i), 0, comet.DefaultWindow); err == nil && ok {
			obs = append(obs, comet.Obs{T: mframes[i].DateObsMs, P: p})
		}
	}
	return obs
}

// manualTrack builds a 2-point track from the preset's manual comet positions (registered-frame pixel
// coords) anchored at the earliest and latest survivor times. ok=false when not fully specified.
func manualTrack(opts Options, mframes []*inspect.Frame, order []int) (comet.Track, bool) {
	if opts.Preset == nil || opts.Preset.CometX1 <= 0 || opts.Preset.CometY1 <= 0 || opts.Preset.CometX2 <= 0 || opts.Preset.CometY2 <= 0 {
		return comet.Track{}, false
	}
	first, last := order[0], order[len(order)-1]
	tr, err := comet.NewTrack(
		comet.Point{X: opts.Preset.CometX1, Y: opts.Preset.CometY1}, mframes[first].DateObsMs,
		comet.Point{X: opts.Preset.CometX2, Y: opts.Preset.CometY2}, mframes[last].DateObsMs)
	return tr, err == nil
}

// stackChannelsDual produces, per filter, a star-aligned master and (when a comet track is available) a
// comet-aligned master. Returns filter→basename maps (basenames live in outDir).
func stackChannelsDual(ctx context.Context, opts Options, mergedDir string, mframes []*inspect.Frame,
	metrics []grade.Metric, track comet.Tracker, pMid comet.Point, haveTrack bool, workRun, outDir string, res *Result) (map[string]string, map[string]string) {
	byFilter := map[string][]int{}
	for i := range mframes {
		if metrics[i].Rejected || mframes[i].Filter == "" || !fileExists(regCometPath(mergedDir, i)) {
			continue
		}
		byFilter[mframes[i].Filter] = append(byFilter[mframes[i].Filter], i)
	}
	starMasters, cometMasters := map[string]string{}, map[string]string{}
	for filter, idxs := range byFilter {
		starBase := "star_master_" + filterTag(filter)
		if stackAligned(ctx, opts, regPaths(mergedDir, idxs), filepath.Join(workRun, "star_"+sanitize(filter)), filepath.Join(outDir, starBase), res, "star "+filter) {
			starMasters[filter] = starBase
			res.Channels = append(res.Channels, ChannelResult{
				Filter: filter, InputFrames: countFilter(mframes, filter), StackedFrames: len(idxs),
				OutputPath: filepath.Join(outDir, starBase+".fits"),
			})
		}
		if !haveTrack {
			continue
		}
		cometDir := filepath.Join(workRun, "comet_"+sanitize(filter))
		if err := translateChannel(cometDir, mergedDir, idxs, mframes, track, pMid); err != nil {
			res.Warnings = append(res.Warnings, "comet: translate "+filter+": "+err.Error())
			continue
		}
		// Opt-in maximum-cleanliness path: de-star every comet-aligned frame BEFORE the stack (no
		// trail residuals at all, at one StarNet pass per frame). Soft-fail to the normal stack.
		if opts.Preset != nil && opts.Preset.CometPerFrameStarnet && opts.Starnet != nil && opts.Starnet.Available(ctx) == nil {
			if err := starlessCometFrames(ctx, opts, cometDir); err != nil {
				res.Warnings = append(res.Warnings, "comet: per-frame star removal failed ("+err.Error()+") — stacking with stars")
			} else {
				res.Warnings = append(res.Warnings, "comet: per-frame StarNet star removal before the "+filter+" comet stack")
			}
		}
		cometBase := "comet_master_" + filterTag(filter)
		// The comet-aligned stack uses ASYMMETRIC rejection: the coma is consistent frame-to-frame
		// (a tight high clip never rejects it) while the trailing stars are bright one-or-two-frame
		// HIGH outliers at any pixel — σ-high 1.8 erases them; σ-low 4 protects the faint tail.
		if stackAlignedDirScript(ctx, opts, cometDir,
			siril.StackCometScript("s", filepath.Join(outDir, cometBase), len(idxs), opts.cometStack()), res, "comet "+filter) {
			cometMasters[filter] = cometBase
		}
	}
	return starMasters, cometMasters
}

// starlessCometFrames removes the stars from every comet-aligned frame in cometDir in place — the
// cleanest possible comet layer (nothing left for the stack's rejection to miss). Two batched Siril
// scripts do the FITS↔TIFF conversion around one StarNet pass per frame.
func starlessCometFrames(ctx context.Context, opts Options, cometDir string) error {
	frames, err := filepath.Glob(filepath.Join(cometDir, "c_*.fits"))
	if err != nil || len(frames) == 0 {
		return fmt.Errorf("no comet frames to de-star")
	}
	sort.Strings(frames)
	var toTif strings.Builder
	toTif.WriteString(cometHdr)
	for _, f := range frames {
		base := strings.TrimSuffix(filepath.Base(f), ".fits")
		fmt.Fprintf(&toTif, "load %s\nsavetif %s_t\n", base, base)
	}
	if _, err := opts.Runner.Run(ctx, cometDir, toTif.String(), nil); err != nil {
		return fmt.Errorf("frames to tif: %w", err)
	}
	var back strings.Builder
	back.WriteString(cometHdr)
	for _, f := range frames {
		base := strings.TrimSuffix(filepath.Base(f), ".fits")
		if err := opts.Starnet.RemoveStars(ctx, filepath.Join(cometDir, base+"_t.tif"),
			filepath.Join(cometDir, base+"_s.tif"), starnet.Options{}, nil); err != nil {
			return fmt.Errorf("starnet %s: %w", base, err)
		}
		fmt.Fprintf(&back, "load %s_s\nsave %s\n", base, base)
	}
	if _, err := opts.Runner.Run(ctx, cometDir, back.String(), nil); err != nil {
		return fmt.Errorf("starless back to fits: %w", err)
	}
	for _, f := range frames { // scratch TIFFs are per-frame-sized; don't leave GBs behind
		base := strings.TrimSuffix(f, ".fits")
		_ = os.Remove(base + "_t.tif")
		_ = os.Remove(base + "_s.tif")
	}
	return nil
}

// translateChannel writes each registered frame, shifted so the comet lands at pMid, into cometDir.
func translateChannel(cometDir, mergedDir string, idxs []int, mframes []*inspect.Frame, track comet.Tracker, pMid comet.Point) error {
	if err := fsutil.EnsureDir(cometDir); err != nil {
		return err
	}
	for k, i := range idxs {
		dx, dy := track.Shift(mframes[i].DateObsMs, pMid)
		if err := comet.TranslateFile(regCometPath(mergedDir, i), filepath.Join(cometDir, fmt.Sprintf("c_%05d.fits", k+1)), dx, dy); err != nil {
			return err
		}
	}
	return nil
}

func stackAligned(ctx context.Context, opts Options, paths []string, seqDir, outBase string, res *Result, label string) bool {
	if _, err := fsutil.LinkFrames(seqDir, paths); err != nil {
		res.Warnings = append(res.Warnings, "comet: link "+label+": "+err.Error())
		return false
	}
	return stackAlignedDir(ctx, opts, seqDir, outBase, len(paths), res, label)
}

func stackAlignedDir(ctx context.Context, opts Options, seqDir, outBase string, frames int, res *Result, label string) bool {
	return stackAlignedDirScript(ctx, opts, seqDir,
		siril.StackAlignedScript("s", outBase, frames, opts.lightStack(opts.stackWeight())), res, label)
}

func stackAlignedDirScript(ctx context.Context, opts Options, seqDir, script string, res *Result, label string) bool {
	if _, err := opts.Runner.Run(ctx, seqDir, script, nil); err != nil {
		res.Warnings = append(res.Warnings, "comet: stack "+label+": "+err.Error())
		return false
	}
	return true
}

// finishComet combines each side into a stretched colour image then composites the comet and stars,
// writing the canonical comet_final.* and setting res.Final (the standard, non-supervised finish).
func finishComet(ctx context.Context, opts Options, res *Result, starMasters, cometMasters map[string]string, haveTrack bool, pMid comet.Point, outDir string) {
	res.Final = combineCometFinish(ctx, opts, res, starMasters, cometMasters, haveTrack, pMid, outDir, "comet_final")
}

// combineCometFinish re-combines the star + comet colour stacks with the working preset (background
// level/degree/saturation) and composites the sharp comet with the star layer, writing the result to
// finalBase.{fits,tif,png} in outDir. Shared by the standard finish (finalBase "comet_final") and the
// supervised finish (a per-iteration basename), so the re-finish loop reuses the exact combine path.
// Returns nil only when no star image could be combined.
func combineCometFinish(ctx context.Context, opts Options, res *Result, starMasters, cometMasters map[string]string, haveTrack bool, pMid comet.Point, outDir, finalBase string) *postprocess.Result {
	deg := backgroundDegree(ctx, opts)
	bg := 0.06
	if opts.Preset != nil && opts.Preset.BackgroundLevel > 0 {
		bg = opts.Preset.BackgroundLevel
	}
	star := combineComet(ctx, opts, starMasters, outDir, "star_color", deg, bg, res)
	if !star {
		res.Warnings = append(res.Warnings, "comet: no star image could be combined")
		return nil
	}
	if haveTrack && (pMid.X > 0 || pMid.Y > 0) {
		// Cross-register the channels on the coma at p_mid. Skipped when p_mid is unset (a post-run refine:
		// the persisted comet masters are already coma-aligned, so re-aligning would shift them).
		alignCometMasters(cometMasters, pMid, outDir, res)
	}
	cometOK := haveTrack && combineComet(ctx, opts, cometMasters, outDir, "comet_color", deg, bg, res)
	if !cometOK {
		return cometResult(outDir, "star_color", "star-aligned only (no comet track)")
	}

	if opts.Starnet == nil || opts.Starnet.Available(ctx) != nil {
		if _, err := opts.Runner.Run(ctx, outDir, siril.PixelMathScript("max($comet_color$, $star_color$)", finalBase), nil); err != nil {
			res.Warnings = append(res.Warnings, "comet: composite failed: "+err.Error())
			return cometResult(outDir, "comet_color", "comet-aligned only (composite failed)")
		}
		res.Warnings = append(res.Warnings, "StarNet++ unavailable — composited the rejection-cleaned stacks (faint residuals may remain)")
		return cometExport(ctx, opts, outDir, finalBase, "comet + stars (rejection composite)", res)
	}
	return compositeWithStarnet(ctx, opts, outDir, finalBase, res)
}

// compositeWithStarnet removes stars from both colour stacks, isolates the star layer (star − starless),
// and screens it over the starless comet, writing the composite to finalBase.
func compositeWithStarnet(ctx context.Context, opts Options, outDir, finalBase string, res *Result) *postprocess.Result {
	if err := starnetToFits(ctx, opts, outDir, "comet_color", "comet_starless"); err != nil {
		res.Warnings = append(res.Warnings, "comet: StarNet on comet failed: "+err.Error())
		return cometExport(ctx, opts, outDir, "comet_color", "comet-aligned (StarNet failed)", res)
	}
	if err := starnetToFits(ctx, opts, outDir, "star_color", "star_starless"); err != nil {
		res.Warnings = append(res.Warnings, "comet: StarNet on stars failed: "+err.Error())
		return cometExport(ctx, opts, outDir, "comet_starless", "comet-aligned, starless (star layer failed)", res)
	}
	if _, err := opts.Runner.Run(ctx, outDir, siril.PixelMathScript("$star_color$ - $star_starless$", "star_layer"), nil); err != nil {
		res.Warnings = append(res.Warnings, "comet: star-layer extraction failed: "+err.Error())
		return cometExport(ctx, opts, outDir, "comet_starless", "comet-aligned, starless", res)
	}
	// ADD (clipped), not max(): the star layer is stars-over-≈0 after the subtraction, so adding
	// preserves the faint tail UNDER star halos where max() would replace it with the star pixel.
	if _, err := opts.Runner.Run(ctx, outDir, siril.PixelMathScript("min(1, $comet_starless$ + $star_layer$)", finalBase), nil); err != nil {
		res.Warnings = append(res.Warnings, "comet: composite failed: "+err.Error())
		return cometExport(ctx, opts, outDir, "comet_starless", "comet-aligned, starless", res)
	}
	return cometExport(ctx, opts, outDir, finalBase, "sharp comet + star layer (StarNet)", res)
}

// starnetToFits removes stars from inBase.tif with StarNet and loads the result back to outBase.fits.
func starnetToFits(ctx context.Context, opts Options, outDir, inBase, outBase string) error {
	if err := opts.Starnet.RemoveStars(ctx, filepath.Join(outDir, inBase+".tif"), filepath.Join(outDir, outBase+".tif"), starnet.Options{}, nil); err != nil {
		return err
	}
	_, err := opts.Runner.Run(ctx, outDir, cometHdr+fmt.Sprintf("load %s\nsave %s\n", outBase, outBase), nil)
	return err
}

// Cross-channel coma alignment tuning: a window a little larger than the coma, and a small search range
// (the residual per-channel offset left after the track fit is only a few pixels).
const (
	comaAlignRadius = 70
	comaMaxShift    = 16.0
)

// alignCometMasters cross-registers each per-channel comet master onto a reference channel using the coma
// itself — spatial phase/cross-correlation over a window centred on the (now high-SNR) stacked coma —
// removing the residual channel-to-channel offset the linear track leaves (the coma's shape differs by
// filter, biasing its per-frame centroid). The masters are overwritten in place with the aligned pixels,
// so the subsequent rgbcomp yields a single, well-registered colour comet.
func alignCometMasters(cometMasters map[string]string, center comet.Point, outDir string, res *Result) {
	ref := firstFilter(cometMasters) // L-first canonical order → broadband reference when present
	if ref == "" || len(cometMasters) < 2 {
		return
	}
	refImg, err := fits.ReadImage(filepath.Join(outDir, cometMasters[ref]+".fits"))
	if err != nil {
		return
	}
	// center is p_mid — the position every frame's coma was shifted to — so the correlation window sits on
	// the comet, NOT on whatever bright star a blob detector would otherwise lock onto.
	for filter, base := range cometMasters {
		if filter == ref {
			continue
		}
		path := filepath.Join(outDir, base+".fits")
		tgt, err := fits.ReadImage(path)
		if err != nil {
			continue
		}
		dx, dy := comet.AlignToReference(refImg, tgt, center, comaAlignRadius, comaMaxShift)
		if dx == 0 && dy == 0 {
			continue
		}
		if err := comet.Translate(tgt, dx, dy).WriteFITS(path); err != nil {
			res.Warnings = append(res.Warnings, "comet: align "+filter+": "+err.Error())
			continue
		}
		res.Warnings = append(res.Warnings, fmt.Sprintf("comet: cross-aligned %s coma to %s by (%.2f, %.2f) px", filter, ref, dx, dy))
	}
}

// combineComet assembles per-channel masters into one stretched colour image saved as outBase.fits/.tif/
// .png in outDir. Returns false if no channels were available.
func combineComet(ctx context.Context, opts Options, channels map[string]string, outDir, outBase string, deg int, bg float64, res *Result) bool {
	if len(channels) == 0 {
		return false
	}
	has := func(f string) bool { _, ok := channels[f]; return ok }
	script := cometHdr
	if has("R") && has("G") && has("B") {
		lum := ""
		if has("L") {
			lum = " -lum=" + channels["L"]
		}
		script += fmt.Sprintf("rgbcomp %s %s %s%s -out=%s_lin\nload %s_lin\n", channels["R"], channels["G"], channels["B"], lum, outBase, outBase)
		script += siril.SubskyCmd(deg) + siril.AutostretchCmd(true, bg) + "\n"
	} else {
		var one string
		for _, b := range channels {
			one = b
			break
		}
		script += fmt.Sprintf("load %s\n", one) + siril.SubskyCmd(deg) + siril.AutostretchCmd(false, bg) + "\n"
	}
	if opts.Preset != nil && opts.Preset.Saturation > 0 {
		script += fmt.Sprintf("satu %.3f 0\n", opts.Preset.Saturation) // colour boost (0 = none; raised by the supervisor)
	}
	script += fmt.Sprintf("save %s\nsavetif %s\nsavepng %s\n", outBase, outBase, outBase)
	if _, err := opts.Runner.Run(ctx, outDir, script, nil); err != nil {
		res.Warnings = append(res.Warnings, "comet: combine "+outBase+": "+err.Error())
		return false
	}
	return true
}

// cometResult points the run result at an already-saved colour image (the soft-fail outputs).
func cometResult(outDir, base, note string) *postprocess.Result {
	return &postprocess.Result{
		Mode:    "comet",
		Outputs: []string{filepath.Join(outDir, base+".tif"), filepath.Join(outDir, base+".png")},
		Notes:   []string{note},
	}
}

// cometExport writes the final FITS to TIFF + PNG and returns the run result.
func cometExport(ctx context.Context, opts Options, outDir, base, note string, res *Result) *postprocess.Result {
	if _, err := opts.Runner.Run(ctx, outDir, cometHdr+fmt.Sprintf("load %s\nsavetif %s\nsavepng %s\n", base, base, base), nil); err != nil {
		res.Warnings = append(res.Warnings, "comet: export failed: "+err.Error())
	}
	return cometResult(outDir, base, note)
}
