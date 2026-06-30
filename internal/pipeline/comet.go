package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
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

const cometHdr = "requires 1.2.0\nsetext fits\n"

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
	// NB: comet mode keeps every filtered light — unlike the deep-sky path it does NOT ExcludeBayer,
	// because older ASICAP mono captures carry a spurious BAYERPAT yet are shot through a filter wheel
	// (mono-per-filter, not one-shot-colour). A genuine OSC comet is out of scope for v1.

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
	res.Warnings = append(res.Warnings, inv.Warnings...)
	res.Warnings = append(res.Warnings, aiToolWarnings(ctx, opts)...)

	opts.report(Progress{Step: "building master calibration frames", Index: 1, Total: 4})
	masters, mWarn, err := calib.BuildMasters(ctx, opts.Runner, inv, filepath.Join(workRun, "masters"), workRun, opts.sirilLines("masters"))
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

	// 3. Locate the comet and build its linear track across the common coordinate system.
	track, haveTrack := cometTrack(opts, mergedDir, mframes, metrics, res)
	pMid := track.At(comet.MidTime(times))

	// 4. Per channel: a star stack and a comet stack from the same globally-aligned frames.
	opts.report(Progress{Step: "stacking star + comet per channel", Index: 3, Total: 4})
	starMasters, cometMasters := stackChannelsDual(ctx, opts, mergedDir, mframes, metrics, track, pMid, haveTrack, workRun, outDir, res)

	// 5. Combine each side into a colour image, then StarNet-separate and screen the stars back.
	opts.report(Progress{Step: "compositing comet + stars", Index: 4, Total: 4})
	finishComet(ctx, opts, res, starMasters, cometMasters, haveTrack, pMid, outDir)

	if res.Final != nil {
		for _, o := range res.Final.Outputs {
			if filepath.Ext(o) == ".png" {
				opts.report(Progress{Step: "final", Index: 4, Total: 4, Preview: o})
				break
			}
		}
	}
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
		sel := calib.MatchForLight(set.Key, masters)
		dark, flat, bias := sel.Masters()
		cm := siril.CalibMasters{Dark: dark, Flat: flat, Bias: bias}
		setDir := filepath.Join(workRun, "cal_"+sanitize(set.Key.Filter)+"_"+fmt.Sprint(set.Key.ExposureMs))
		if _, err := fsutil.LinkFrames(setDir, framePaths(set.Frames)); err != nil {
			warnings = append(warnings, "comet: link "+set.Key.Filter+": "+err.Error())
			continue
		}
		if _, err := opts.Runner.Run(ctx, setDir, siril.CalibrateOnlyScript("light", cm), nil); err != nil {
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
	metrics, _, _, err := gradeChannel(mergedDir, "light", mframes, gradeOpts, false)
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
func cometTrack(opts Options, mergedDir string, mframes []*inspect.Frame, metrics []grade.Metric, res *Result) (comet.Track, bool) {
	order := survivorsByTime(mergedDir, mframes, metrics)
	if len(order) < 2 {
		res.Warnings = append(res.Warnings, "comet: no timestamped registered frames — comet alignment skipped")
		return comet.Track{}, false
	}
	if tr, ok := manualTrack(opts, mframes, order); ok {
		return tr, true
	}
	obs := detectComet(mergedDir, mframes, order)
	tr, ok := comet.FitTrack(obs)
	if !ok {
		res.Warnings = append(res.Warnings, "comet: could not detect the comet in enough frames (provide a manual position) — comet alignment skipped")
		return comet.Track{}, false
	}
	res.Warnings = append(res.Warnings, fmt.Sprintf("comet: motion track fitted from %d/%d detections", len(obs), len(order)))
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
		if p, ok, err := comet.DetectFile(regCometPath(mergedDir, i), comet.DefaultBlurRadius, comet.DefaultWindow); err == nil && ok {
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
	metrics []grade.Metric, track comet.Track, pMid comet.Point, haveTrack bool, workRun, outDir string, res *Result) (map[string]string, map[string]string) {
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
		cometBase := "comet_master_" + filterTag(filter)
		if stackAlignedDir(ctx, opts, cometDir, filepath.Join(outDir, cometBase), res, "comet "+filter) {
			cometMasters[filter] = cometBase
		}
	}
	return starMasters, cometMasters
}

// translateChannel writes each registered frame, shifted so the comet lands at pMid, into cometDir.
func translateChannel(cometDir, mergedDir string, idxs []int, mframes []*inspect.Frame, track comet.Track, pMid comet.Point) error {
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
	return stackAlignedDir(ctx, opts, seqDir, outBase, res, label)
}

func stackAlignedDir(ctx context.Context, opts Options, seqDir, outBase string, res *Result, label string) bool {
	if _, err := opts.Runner.Run(ctx, seqDir, siril.StackAlignedScript("s", outBase), nil); err != nil {
		res.Warnings = append(res.Warnings, "comet: stack "+label+": "+err.Error())
		return false
	}
	return true
}

// finishComet combines each side into a stretched colour image then composites the comet and stars.
func finishComet(ctx context.Context, opts Options, res *Result, starMasters, cometMasters map[string]string, haveTrack bool, pMid comet.Point, outDir string) {
	deg := backgroundDegree(ctx, opts)
	bg := 0.06
	if opts.Preset != nil && opts.Preset.BackgroundLevel > 0 {
		bg = opts.Preset.BackgroundLevel
	}
	star := combineComet(ctx, opts, starMasters, outDir, "star_color", deg, bg, res)
	if !star {
		res.Warnings = append(res.Warnings, "comet: no star image could be combined")
		return
	}
	if haveTrack {
		alignCometMasters(cometMasters, pMid, outDir, res) // cross-register the channels on the coma at p_mid
	}
	cometOK := haveTrack && combineComet(ctx, opts, cometMasters, outDir, "comet_color", deg, bg, res)
	if !cometOK {
		res.Final = cometResult(outDir, "star_color", "star-aligned only (no comet track)")
		return
	}

	if opts.Starnet == nil || opts.Starnet.Available(ctx) != nil {
		if _, err := opts.Runner.Run(ctx, outDir, siril.PixelMathScript("max($comet_color$, $star_color$)", "comet_final"), nil); err != nil {
			res.Warnings = append(res.Warnings, "comet: composite failed: "+err.Error())
			res.Final = cometResult(outDir, "comet_color", "comet-aligned only (composite failed)")
			return
		}
		res.Warnings = append(res.Warnings, "StarNet++ unavailable — composited the rejection-cleaned stacks (faint residuals may remain)")
		res.Final = cometExport(ctx, opts, outDir, "comet_final", "comet + stars (rejection composite)", res)
		return
	}
	res.Final = compositeWithStarnet(ctx, opts, outDir, res)
}

// compositeWithStarnet removes stars from both colour stacks, isolates the star layer (star − starless),
// and screens it over the starless comet.
func compositeWithStarnet(ctx context.Context, opts Options, outDir string, res *Result) *postprocess.Result {
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
	if _, err := opts.Runner.Run(ctx, outDir, siril.PixelMathScript("max($comet_starless$, $star_layer$)", "comet_final"), nil); err != nil {
		res.Warnings = append(res.Warnings, "comet: composite failed: "+err.Error())
		return cometExport(ctx, opts, outDir, "comet_starless", "comet-aligned, starless", res)
	}
	return cometExport(ctx, opts, outDir, "comet_final", "sharp comet + star layer (StarNet)", res)
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
