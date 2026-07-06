// Package planetary processes lunar/planetary captures with lucky imaging: source the frames (a folder
// of stills, a video via ffmpeg, or a SER via Siril), rank them by sharpness in Go, keep the best, then
// — per filter — surface-register the kept frames by sub-pixel cross-correlation (Moon/planets have no
// stars, so Siril's star registration is unusable), normalize-stack the aligned frames, and either
// combine L/R/G/B into a colour image or finish the single mono master. Reuses the comet module's
// starless aligner (ZNCC + bilinear translate). High-precision multi-point (AP) alignment is layered on
// top in apalign.go.
package planetary

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/comet"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// Options tunes the lucky-imaging run.
type Options struct {
	BestPercent int                   // keep this percent of the sharpest frames (default 15 — real lucky imaging)
	Sharpen     bool                  // apply wavelet sharpening + CLAHE to the final image
	APAlign     bool                  // run multi-point (alignment-point) warping to correct atmospheric distortion
	APWeights   bool                  // multi-point QUALITY weighting: each region dominated by the frames sharpest there
	Formats     []string              // output formats: png, tif, fits
	Finish      siril.PlanetaryFinish // stretch/sharpen/contrast/saturation of the finish (tuned by the supervisor)

	// Richardson-Lucy deconvolution of the luminance (0 → the package defaults 2.8 / 18 / 700).
	// The old constants (FWHM 3, 10 iters, alpha 1800) were so strongly regularized the deconv was
	// nearly a no-op — the stack looked soft even when the master was sharp.
	DeconvFWHM  float64
	DeconvIters int
	DeconvAlpha float64
}

// DefaultOptions returns sensible defaults.
func DefaultOptions() Options {
	return Options{BestPercent: 15, Sharpen: true, APAlign: true, APWeights: true, Formats: []string{"png", "tif"}, Finish: DefaultFinish()}
}

// DefaultFinish is the original mineral-Moon finish tuning (see siril.DefaultPlanetaryFinish).
func DefaultFinish() siril.PlanetaryFinish { return siril.DefaultPlanetaryFinish() }

// FrameScore is one input frame's lucky-imaging quality record, for the frame report (kept/rejected).
type FrameScore struct {
	Index  int     `json:"index"`            // 1-based position within its channel
	File   string  `json:"file"`             // source frame basename
	Filter string  `json:"filter,omitempty"` // "" for a mono run
	Score  float64 `json:"score"`            // sharpness (Laplacian variance); higher = sharper
	Kept   bool    `json:"kept"`             // included in the stack
}

// Result summarizes a planetary run.
type Result struct {
	Source        string       `json:"source"`
	FrameCount    int          `json:"frame_count"`
	StackedFrames int          `json:"stacked_frames"`
	Outputs       []string     `json:"outputs"`
	Frames        []FrameScore `json:"frames,omitempty"`
	Notes         []string     `json:"notes,omitempty"`

	// Masters maps each channel label (R/G/B/L or "mono") to its persisted, stacked+deconvolved master
	// base path (no extension) in the output dir — the re-finish inputs the supervised finish and a
	// post-run refine re-run the finish over (no re-stack, no re-deconvolution).
	Masters map[string]string `json:"masters,omitempty"`
	// BestFrameLapVar / MasterLapVar are the scale-invariant disk sharpness of the best kept input
	// frame vs the finished detail master — the objective "the stack must out-detail the best single
	// frame" acceptance (pass: master ≥ 1.05× best frame; a miss lands in Notes as a warning).
	BestFrameLapVar float64 `json:"best_frame_lapvar,omitempty"`
	MasterLapVar    float64 `json:"master_lapvar,omitempty"`
	Sharpen         bool    `json:"sharpen,omitempty"`  // whether the finish sharpens (from Options.Sharpen)
	OutBase         string  `json:"out_base,omitempty"` // canonical final base path (<outDir>/<object>_stack)
	// Run identity (mirrors pipeline.Result) so the run lives at output/<object>/<run_id> and a post-run
	// refine + full-S3 push can resolve it. json tags match pipeline.Result / job.s3ResultTarget.
	Object    string `json:"object,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	OutputDir string `json:"output_dir,omitempty"`
	// Iterations records each supervised-finish pass (empty for a standard run) for the UI timeline.
	Iterations []postprocess.IterationRecord `json:"iterations,omitempty"`
	// StagePreviews are the saved milestone preview PNGs (stacked masters + final) for the UI timeline.
	StagePreviews []postprocess.StagePreview `json:"stage_previews,omitempty"`
}

var videoExts = map[string]bool{".mp4": true, ".mov": true, ".mkv": true, ".m4v": true, ".avi": true}

// imageExts are the still-frame formats a Moon/planetary capture uses when shot as an image series
// instead of a video — FITS plus the processed/raw stills Siril converts into a sequence.
var imageExts = map[string]bool{
	".fits": true, ".fit": true, ".fts": true,
	".tif": true, ".tiff": true, ".png": true, ".jpg": true, ".jpeg": true,
	".cr2": true, ".cr3": true, ".nef": true, ".arw": true, ".raf": true, ".dng": true,
}

var fitsExts = map[string]bool{".fits": true, ".fit": true, ".fts": true}

// Process runs the full lucky-imaging pipeline on a folder of frames, a video file, or a SER.
func Process(ctx context.Context, runner *siril.Runner, ffmpegBin, inputPath, workDir, outDir string,
	opts Options, onProgress func(siril.Progress)) (*Result, error) {
	if opts.BestPercent <= 0 || opts.BestPercent > 100 {
		opts.BestPercent = 50
	}
	if len(opts.Formats) == 0 {
		opts.Formats = []string{"png", "tif"}
	}
	outAbs, err := filepath.Abs(outDir)
	if err != nil {
		return nil, err
	}
	if err := fsutil.EnsureDir(outAbs); err != nil {
		return nil, err
	}
	workAbs, err := filepath.Abs(workDir)
	if err != nil {
		return nil, err
	}
	object := objectName(inputPath)
	// Every run gets its own output/<object>/<runID> directory (consistent with deepsky/OSC/comet), so
	// runs never overwrite each other, the base output dir stays clean, and a refine/run.json resolves here.
	runID := time.Now().Format("20060102_150405")
	runDir := filepath.Join(outAbs, object, runID)
	if err := fsutil.EnsureDir(runDir); err != nil {
		return nil, err
	}
	res := &Result{Source: inputPath, Object: object, RunID: runID, OutputDir: runDir}

	// Start from a clean per-object scratch dir: stale aligned frames / Siril .seq files from a prior run
	// of the same target would otherwise collide with `convert`/`stack` (it picks up leftover al_*.fits).
	runRoot := filepath.Join(workAbs, "planetary_"+object)
	_ = os.RemoveAll(runRoot)

	// 1. Source frames into per-channel groups of FITS paths (filter "" = a single mono group).
	report(onProgress, "preparing frames")
	groups, order, err := sourceChannels(ctx, runner, ffmpegBin, inputPath, workAbs, object, onProgress)
	if err != nil {
		return nil, err
	}
	if len(order) == 0 {
		return nil, fmt.Errorf("no frames to process in %s", inputPath)
	}

	// 2. Per channel: rank → keep best % → surface-align → normalized stack → linear master.
	masters := map[string]string{} // filter → master base path (no extension)
	for _, filter := range order {
		report(onProgress, "stacking "+channelLabel(filter))
		master, frames, kept, serr := stackChannel(ctx, runner, groups[filter], filter, workAbs, object, opts, onProgress)
		if serr != nil {
			return nil, fmt.Errorf("stack %s: %w", channelLabel(filter), serr)
		}
		masters[filter] = master
		res.Frames = append(res.Frames, frames...)
		res.FrameCount += len(groups[filter])
		res.StackedFrames += kept
	}

	// 3. Finish: co-register channels + colour LRGB combine when R/G/B exist, else the mono master.
	report(onProgress, "finishing")
	outBase := filepath.Join(runDir, object+"_stack")
	r, g, b, l, mono := masters["R"], masters["G"], masters["B"], masters["L"], ""
	if r != "" && g != "" && b != "" {
		if cerr := coRegisterMasters(masters, refChannel(masters)); cerr != nil {
			return nil, fmt.Errorf("co-register channels: %w", cerr)
		}
		// Deconvolve the LUMINANCE only (best SNR): it carries the surface detail. Then soften the colour
		// channels (chroma) — the sharp L drives the detail, so blurring R/G/B removes the colour speckle
		// that sharpening + saturation would otherwise amplify in the fewer-frame colour stacks (standard
		// LRGB: sharp luminance, smooth chroma).
		if opts.Sharpen && l != "" {
			report(onProgress, "deconvolving luminance")
			if derr := deconvolveMaster(ctx, runner, l, opts, onProgress); derr != nil {
				return nil, fmt.Errorf("deconvolve luminance: %w", derr)
			}
			if serr := smoothChroma(masters, chromaBlur); serr != nil {
				return nil, fmt.Errorf("smooth chroma: %w", serr)
			}
		}
		res.Notes = append(res.Notes, "colour LRGB lucky-imaging stack (per-filter surface alignment)")
	} else {
		mono = masters[order[0]]
		if opts.Sharpen && mono != "" {
			report(onProgress, "deconvolving")
			if derr := deconvolveMaster(ctx, runner, mono, opts, onProgress); derr != nil {
				return nil, fmt.Errorf("deconvolve: %w", derr)
			}
		}
		res.Notes = append(res.Notes, "mono lucky-imaging stack")
	}
	// Persist the finalized (stacked + deconvolved) masters to the output dir + record them, so the
	// supervised finish and a post-run refine can re-run the finish over them without re-stacking.
	finishMasters := map[string]string{}
	if mono != "" {
		finishMasters["mono"] = mono
	} else {
		finishMasters["R"], finishMasters["G"], finishMasters["B"] = r, g, b
		if l != "" {
			finishMasters["L"] = l
		}
	}
	if persisted, perr := persistPlanetaryMasters(runDir, finishMasters); perr != nil {
		res.Notes = append(res.Notes, "warn: "+perr.Error())
	} else {
		res.Masters = persisted
	}
	res.Sharpen, res.OutBase = opts.Sharpen, outBase

	// Objective acceptance: the detail master must out-resolve the best single kept frame — the whole
	// point of lucky imaging. Both sides use the same scale-invariant disk metric, so the comparison
	// is meaningful across the master's normalization.
	detailFilter := ""
	if l != "" {
		detailFilter = "L"
	} else if mono != "" {
		detailFilter = order[0]
	}
	detailMaster := l
	if detailMaster == "" {
		detailMaster = mono
	}
	if detailMaster != "" {
		for _, fr := range res.Frames {
			if fr.Kept && fr.Filter == detailFilter && fr.Score > res.BestFrameLapVar {
				res.BestFrameLapVar = fr.Score
			}
		}
		res.MasterLapVar = frameSharpness(detailMaster + ".fits")
		if res.BestFrameLapVar > 0 && res.MasterLapVar > 0 && res.MasterLapVar < 1.05*res.BestFrameLapVar {
			res.Notes = append(res.Notes, fmt.Sprintf(
				"warning: stacked master sharpness (%.3g) below 1.05x the best single frame (%.3g) — check alignment/selection",
				res.MasterLapVar, res.BestFrameLapVar))
		}
	}

	script := siril.PlanetaryFinishScript(r, g, b, l, mono, outBase, opts.Sharpen, opts.Finish, opts.Formats)
	if _, err := runner.Run(ctx, runDir, script, onProgress); err != nil {
		return nil, err
	}
	for _, f := range opts.Formats {
		res.Outputs = append(res.Outputs, outBase+"."+f)
	}
	res.Notes = append(res.Notes,
		fmt.Sprintf("sub-pixel surface alignment; kept best %d%% of frames by sharpness", opts.BestPercent))
	return res, nil
}

// persistPlanetaryMasters copies each finalized master FITS into outDir as master_<label>.fits and
// returns a label→base-path (no extension) map — the re-finish inputs for the supervised finish/refine.
func persistPlanetaryMasters(outDir string, work map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for label, base := range work {
		if base == "" {
			continue
		}
		dstBase := filepath.Join(outDir, "master_"+label)
		if err := fsutil.CopyFile(base+".fits", dstBase+".fits"); err != nil {
			return nil, fmt.Errorf("persist master %s: %w", label, err)
		}
		out[label] = dstBase
	}
	return out, nil
}

// Refinish re-runs only the planetary finish (stretch / wavelet sharpen / local contrast / saturation)
// over already-stacked+deconvolved masters with a tuned Finish, writing outBase.<fmt>. It backs the
// supervised finish and a post-run refine: no re-stack, and NO re-deconvolution (the masters are already
// deconvolved, so re-finishing them avoids double-deconv). masters maps R/G/B/L or "mono" → base path.
func Refinish(ctx context.Context, runner *siril.Runner, outDir string, masters map[string]string,
	sharpen bool, fin siril.PlanetaryFinish, formats []string, outBase string) (*Result, error) {
	r, g, b, l := masters["R"], masters["G"], masters["B"], masters["L"]
	mono := ""
	if !(r != "" && g != "" && b != "") {
		mono = masters["mono"]
		if mono == "" {
			for _, base := range masters { // any single master (defensive)
				mono = base
				break
			}
		}
	}
	if r == "" && g == "" && b == "" && mono == "" {
		return nil, fmt.Errorf("planetary refinish: no masters")
	}
	script := siril.PlanetaryFinishScript(r, g, b, l, mono, outBase, sharpen, fin, formats)
	if _, err := runner.Run(ctx, outDir, script, nil); err != nil {
		return nil, err
	}
	res := &Result{OutBase: outBase, Sharpen: sharpen, Masters: masters}
	for _, f := range formats {
		res.Outputs = append(res.Outputs, outBase+"."+f)
	}
	return res, nil
}

// sourceChannels prepares the frames as one or more channels of ranked-and-alignable FITS paths. A
// folder with a filter wheel (≥2 mono filters) yields one group per filter (colour); anything else —
// a mono folder, a single-filter folder, a video, or a SER — yields a single "" mono group.
func sourceChannels(ctx context.Context, runner *siril.Runner, ffmpegBin, inputPath, workAbs, object string,
	onProgress func(siril.Progress)) (map[string][]string, []string, error) {
	if info, statErr := os.Stat(inputPath); statErr == nil && info.IsDir() {
		return sourceFolderChannels(ctx, runner, inputPath, workAbs, object, onProgress)
	}
	frames, err := convertVideo(ctx, runner, ffmpegBin, inputPath, workAbs, object, onProgress)
	if err != nil {
		return nil, nil, err
	}
	return map[string][]string{"": frames}, []string{""}, nil
}

// sourceFolderChannels classifies a capture folder with inspect. If it used a filter wheel (≥2 distinct
// mono filters), it returns one group of FITS frames per filter (for a colour combine); otherwise it
// stages every still as one mono group. FITS frames are used in place (no Siril convert needed).
func sourceFolderChannels(ctx context.Context, runner *siril.Runner, inputPath, workAbs, object string,
	onProgress func(siril.Progress)) (map[string][]string, []string, error) {
	inv, err := inspect.ScanWithOptions(ctx, inputPath, inspect.ScanOptions{})
	if err != nil {
		return nil, nil, err
	}
	byFilter := map[string][]string{}
	for _, fr := range inv.Frames {
		if fr.Type == inspect.Light {
			byFilter[fr.Filter] = append(byFilter[fr.Filter], fr.Path)
		}
	}
	var distinct []string
	for f := range byFilter {
		if f != "" {
			distinct = append(distinct, f)
		}
	}
	if len(distinct) < 2 {
		// Mono (or unclassified): stage every still in the folder as one sequence.
		dir := filepath.Join(workAbs, "planetary_"+object, "mono")
		if err := fsutil.EnsureDir(dir); err != nil {
			return nil, nil, err
		}
		n, err := stageImageFrames(inputPath, dir)
		if err != nil {
			return nil, nil, err
		}
		if n == 0 {
			return nil, nil, fmt.Errorf("no image frames found in %s (expected FITS/TIFF/PNG/raw stills)", inputPath)
		}
		frames, cerr := convertSeq(ctx, runner, dir, onProgress)
		if cerr != nil {
			return nil, nil, cerr
		}
		return map[string][]string{"": frames}, []string{""}, nil
	}

	groups := map[string][]string{}
	for filter, paths := range byFilter {
		if filter == "" {
			continue // stray unfiltered frames are not a colour channel
		}
		fitsFrames, err := framesAsFITS(ctx, runner, paths, filepath.Join(workAbs, "planetary_"+object, "ch_"+channelLabel(filter)), onProgress)
		if err != nil {
			return nil, nil, fmt.Errorf("prepare %s frames: %w", filter, err)
		}
		groups[filter] = fitsFrames
	}
	return groups, channelOrder(groups), nil
}

// framesAsFITS returns FITS paths for the given source frames: when they are already FITS they are used
// in place (their real names flow into the report and no Siril convert runs); otherwise they are staged
// and Siril-converted into the work dir.
func framesAsFITS(ctx context.Context, runner *siril.Runner, paths []string, dir string,
	onProgress func(siril.Progress)) ([]string, error) {
	allFITS := true
	for _, p := range paths {
		if !fitsExts[strings.ToLower(filepath.Ext(p))] {
			allFITS = false
			break
		}
	}
	if allFITS {
		out := append([]string(nil), paths...)
		sort.Strings(out)
		return out, nil
	}
	if err := fsutil.EnsureDir(dir); err != nil {
		return nil, err
	}
	if _, err := stageFiles(paths, dir, "frame"); err != nil {
		return nil, err
	}
	return convertSeq(ctx, runner, dir, onProgress)
}

// convertVideo extracts a video (ffmpeg) or links a SER, then Siril-converts it into a FITS sequence.
func convertVideo(ctx context.Context, runner *siril.Runner, ffmpegBin, inputPath, workAbs, object string,
	onProgress func(siril.Progress)) ([]string, error) {
	dir := filepath.Join(workAbs, "planetary_"+object, "mono")
	if err := fsutil.EnsureDir(dir); err != nil {
		return nil, err
	}
	switch ext := strings.ToLower(filepath.Ext(inputPath)); {
	case videoExts[ext]:
		if err := extractFrames(ctx, ffmpegBin, inputPath, dir); err != nil {
			return nil, err
		}
	case ext == ".ser":
		abs, _ := filepath.Abs(inputPath)
		_ = os.Remove(filepath.Join(dir, "vid.ser"))
		if err := os.Symlink(abs, filepath.Join(dir, "vid.ser")); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported planetary input %q — use a video (mp4/mov/mkv/avi), a SER, or a folder of frames", inputPath)
	}
	return convertSeq(ctx, runner, dir, onProgress)
}

// convertSeq runs Siril `convert vid` in dir and returns the resulting vid_*.fits, sorted.
func convertSeq(ctx context.Context, runner *siril.Runner, dir string, onProgress func(siril.Progress)) ([]string, error) {
	report(onProgress, "converting to FITS sequence")
	if _, err := runner.Run(ctx, dir, siril.ConvertScript("vid"), onProgress); err != nil {
		return nil, err
	}
	frames, err := filepath.Glob(filepath.Join(dir, "vid_*.fits"))
	if err != nil || len(frames) == 0 {
		return nil, fmt.Errorf("no frames after conversion in %s", dir)
	}
	sort.Strings(frames)
	return frames, nil
}

// stackChannel runs the lucky-imaging stack for one channel: rank by sharpness, keep the best %, surface-
// align the kept frames to the sharpest, and normalize-stack them into a linear master. It returns the
// master base path (no extension), the per-frame report, and the number of frames stacked.
func stackChannel(ctx context.Context, runner *siril.Runner, frames []string, filter, workAbs, object string,
	opts Options, onProgress func(siril.Progress)) (string, []FrameScore, int, error) {
	if len(frames) == 0 {
		return "", nil, 0, fmt.Errorf("no frames")
	}
	scores := make([]float64, len(frames))
	for i, f := range frames {
		scores[i] = frameSharpness(f)
	}
	rejected := rejectLeastSharp(scores, opts.BestPercent)
	rej := map[int]bool{}
	for _, idx := range rejected {
		rej[idx] = true
	}
	var keptPaths []string
	var keptScores []float64
	frameReport := make([]FrameScore, 0, len(frames))
	for i, f := range frames {
		kept := !rej[i+1]
		frameReport = append(frameReport, FrameScore{Index: i + 1, File: filepath.Base(f), Filter: filter, Score: scores[i], Kept: kept})
		if kept {
			keptPaths = append(keptPaths, f)
			keptScores = append(keptScores, scores[i])
		}
	}

	chDir := filepath.Join(workAbs, "planetary_"+object, "ch_"+channelLabel(filter))
	alignDir := filepath.Join(chDir, "aligned")
	if err := fsutil.EnsureDir(alignDir); err != nil {
		return "", frameReport, 0, err
	}
	// Register the kept frames onto the sharpest one and warp each with a SINGLE Catmull-Rom resample.
	// When multi-point alignment is on (and >1 frame) the warp also cancels the local atmospheric
	// distortion a single global shift can't; otherwise it is one global cubic shift.
	apAlign := opts.APAlign && len(keptPaths) > 1
	if apAlign {
		report(onProgress, "multi-point aligning "+channelLabel(filter))
	}
	aligned, _, cellSharp, err := warpToSharpest(keptPaths, keptScores, alignDir, "f", apAlign)
	if err != nil {
		return "", frameReport, 0, err
	}
	if len(aligned) == 0 {
		return "", frameReport, 0, fmt.Errorf("no frames survived alignment")
	}
	// Sharpness-weighted stack (Go, not Siril): weight each aligned frame by its own sharpness so the
	// lucky-sharp frames dominate and the master rivals a single frame, instead of the blurry average a
	// plain mean produces — and, with APWeights, each REGION is dominated by the frames sharpest there
	// (AutoStakkert-style multi-point quality). Siril can't weight a starless stack, so this is done
	// in-process (stack.go).
	master := filepath.Join(chDir, "master_"+channelLabel(filter)) // no extension; .fits added on write
	alignedScores := make([]float64, len(aligned))
	for i, a := range aligned {
		alignedScores[i] = frameSharpness(a)
	}
	var apFields [][]float64
	if opts.APWeights && len(cellSharp) == len(aligned) && len(cellSharp) > 1 {
		apFields = apWeightFields(cellSharp)
	}
	if serr := stackWeightedFileAP(aligned, alignedScores, apFields, master); serr != nil {
		return "", frameReport, 0, fmt.Errorf("weighted stack %s: %w", channelLabel(filter), serr)
	}
	return master, frameReport, len(aligned), nil
}

// Richardson-Lucy deconvolution defaults: a small Gaussian PSF approximating the seeing/optics blur,
// enough iterations to actually recover detail, and moderate total-variation regularization (higher
// alpha = gentler). The previous 10 iters / alpha 1800 were so regularized the deconv barely moved a
// pixel — the finish then had nothing to reveal and the stack read soft.
const (
	deconvFWHMDefault  = 2.8
	deconvItersDefault = 18
	deconvAlphaDefault = 700
)

// deconvParams resolves the run's Richardson-Lucy settings (Options overrides, else the defaults).
func deconvParams(opts Options) (fwhm float64, iters int, alpha float64) {
	fwhm, iters, alpha = deconvFWHMDefault, deconvItersDefault, deconvAlphaDefault
	if opts.DeconvFWHM > 0 {
		fwhm = opts.DeconvFWHM
	}
	if opts.DeconvIters > 0 {
		iters = opts.DeconvIters
	}
	if opts.DeconvAlpha > 0 {
		alpha = opts.DeconvAlpha
	}
	return fwhm, iters, alpha
}

// deconvolveMaster sharpens a linear master (its .fits) in place via Siril Richardson-Lucy deconvolution.
func deconvolveMaster(ctx context.Context, runner *siril.Runner, master string, opts Options, onProgress func(siril.Progress)) error {
	fwhm, iters, alpha := deconvParams(opts)
	_, err := runner.Run(ctx, filepath.Dir(master), siril.DeconvolveLuminanceScript(master, fwhm, iters, int(alpha)), onProgress)
	return err
}

// chromaBlur is the box-blur radius applied to the R/G/B (chroma) masters before the LRGB combine.
const chromaBlur = 3

// smoothChroma box-blurs the colour channels (R/G/B) in place. The luminance (L) supplies the detail,
// so softening chroma removes colour speckle without losing sharpness — the classic LRGB trade.
func smoothChroma(masters map[string]string, radius int) error {
	for _, f := range []string{"R", "G", "B"} {
		base, ok := masters[f]
		if !ok {
			continue
		}
		im, err := fits.ReadImage(base + ".fits")
		if err != nil {
			return err
		}
		if err := blurPlane(im, radius).WriteFITS(base + ".fits"); err != nil {
			return err
		}
	}
	return nil
}

// coRegisterMasters aligns every channel master onto the reference channel (sub-pixel centroid + ZNCC)
// and overwrites them, so the subsequent rgbcomp has no channel-to-channel offset. The per-filter stacks
// were each aligned to their own sharpest frame, captured at different times, so they start misaligned.
func coRegisterMasters(masters map[string]string, refFilter string) error {
	ref, err := fits.ReadImage(masters[refFilter] + ".fits")
	if err != nil {
		return err
	}
	refX, refY := brightCentroid(ref)
	refBlur := blurPlane(ref, warpBlur)
	win := centerPoint(refX, refY)
	radius := min(ref.W, ref.H) * alignWinFrac / 100
	for f, base := range masters {
		if f == refFilter {
			continue
		}
		im, err := fits.ReadImage(base + ".fits")
		if err != nil {
			return err
		}
		icx, icy := brightCentroid(im)
		dx, dy := comet.AlignSeeded(refBlur, blurPlane(im, warpBlur), win, radius, surfaceMaxShift, 0, refX-icx, refY-icy)
		if err := cubicShift(im, dx, dy).WriteFITS(base + ".fits"); err != nil {
			return err
		}
	}
	return nil
}

// refChannel picks the luminance channel as the colour-combine reference when present, else any channel.
func refChannel(masters map[string]string) string {
	if _, ok := masters["L"]; ok {
		return "L"
	}
	for _, f := range []string{"G", "R", "B"} {
		if _, ok := masters[f]; ok {
			return f
		}
	}
	for f := range masters {
		return f
	}
	return ""
}

// channelOrder returns the channels present, in canonical L,R,G,B,… order (others appended sorted).
func channelOrder(groups map[string][]string) []string {
	canon := []string{"L", "R", "G", "B", "Ha", "OIII", "SII"}
	var order []string
	seen := map[string]bool{}
	for _, f := range canon {
		if _, ok := groups[f]; ok {
			order = append(order, f)
			seen[f] = true
		}
	}
	var rest []string
	for f := range groups {
		if !seen[f] {
			rest = append(rest, f)
		}
	}
	sort.Strings(rest)
	return append(order, rest...)
}

// channelLabel names a channel for files/UI: the filter, or "mono" for an unfiltered run.
func channelLabel(filter string) string {
	if filter == "" {
		return "mono"
	}
	return filter
}

var dateLikeRe = regexp.MustCompile(`^\d{1,4}[-_.]\d{1,2}[-_.]\d{1,4}$|^\d{6,8}$|^\d{4}-\d{2}-\d{2}`)

// genericCaptureDir holds leaf folder names that carry no target identity (capture-software buckets), so
// objectName walks past them — e.g. input/moon/autorun → "moon", not "autorun".
var genericCaptureDir = map[string]bool{
	"autorun": true, "autosave": true, "capture": true, "captures": true, "capobj": true,
	"data": true, "ser": true, "avi": true, "frames": true, "frame": true, "video": true,
	"videos": true, "lights": true, "light": true, "input": true, "raw": true, "fits": true,
}

// objectName derives a stable, meaningful output name from the input path: a file's base (minus
// extension), else the first path segment from the leaf up that is neither a capture-software bucket nor
// a date. So input/moon/autorun → "moon"; a video moon.ser → "moon".
func objectName(path string) string {
	clean := filepath.Clean(path)
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return sanitize(strings.TrimSuffix(filepath.Base(clean), filepath.Ext(clean)))
	}
	segs := strings.Split(clean, string(filepath.Separator))
	for i := len(segs) - 1; i >= 0; i-- {
		s := strings.TrimSpace(segs[i])
		if s == "" || s == "." || s == ".." {
			continue
		}
		if genericCaptureDir[strings.ToLower(s)] || dateLikeRe.MatchString(s) {
			continue
		}
		return sanitize(s)
	}
	return sanitize(filepath.Base(clean))
}

// stageImageFrames symlinks every still frame under dir (recursively — capture programs nest frames in
// per-run/timestamp subfolders) into seqDir as a flat, numbered set, so Siril's convert builds one
// sequence from a folder of pre-captured images. Hidden dirs are skipped. Returns the number staged.
func stageImageFrames(dir, seqDir string) (int, error) {
	var srcs []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if p != dir && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if imageExts[strings.ToLower(filepath.Ext(p))] {
			srcs = append(srcs, p)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	sort.Strings(srcs)
	return stageFiles(srcs, seqDir, "frame")
}

// stageFiles symlinks the given source files into dir as <prefix>_00001.ext, … (cross-device → copy).
func stageFiles(paths []string, dir, prefix string) (int, error) {
	n := 0
	for _, src := range paths {
		abs, err := filepath.Abs(src)
		if err != nil {
			return n, err
		}
		link := filepath.Join(dir, fmt.Sprintf("%s_%05d%s", prefix, n+1, strings.ToLower(filepath.Ext(src))))
		_ = os.Remove(link)
		if err := os.Symlink(abs, link); err != nil {
			if cerr := fsutil.CopyFile(abs, link); cerr != nil { // cross-device fallback
				return n, fmt.Errorf("stage %s: %w", src, cerr)
			}
		}
		n++
	}
	return n, nil
}

// rejectLeastSharp keeps the top bestPercent of frames by score and returns the sorted 1-based indices
// of the rest (always keeping at least one).
func rejectLeastSharp(scores []float64, bestPercent int) []int {
	type scored struct {
		index int
		score float64
	}
	ranked := make([]scored, len(scores))
	for i, s := range scores {
		ranked[i] = scored{index: i + 1, score: s}
	}
	sort.Slice(ranked, func(a, b int) bool { return ranked[a].score > ranked[b].score })

	keep := len(scores) * bestPercent / 100
	if keep < 1 {
		keep = 1
	}
	var rejected []int
	for i := keep; i < len(ranked); i++ {
		rejected = append(rejected, ranked[i].index)
	}
	sort.Ints(rejected)
	return rejected
}

// frameSharpness ranks a frame by FULL-RESOLUTION Laplacian variance over the lit disk only,
// normalized by the disk's own dynamic range (scale-invariant). The previous ranking measured a
// 512-px downsample — at that scale crater-level detail is gone, so "keep the best N%" selected on
// coarse contrast, nearly noise with respect to seeing. Full-res on-disk variance ranks by exactly
// the detail the stack must preserve; one frame in memory at a time.
func frameSharpness(path string) float64 {
	im, err := fits.ReadImage(path)
	if err != nil {
		return 0
	}
	return diskSharpness(im)
}

// diskSharpness is the scale-invariant Laplacian variance over the lit disk of a frame's first
// plane (off-disk sky/limb pixels contribute nothing but noise to the metric).
func diskSharpness(im *fits.Image) float64 {
	p := im.Pix[0]
	w, h := im.W, im.H
	if w < 3 || h < 3 {
		return 0
	}
	bg := lowPercentile(p, 0.2)
	pk := lowPercentile(p, 0.999)
	if pk-bg <= 1e-9 {
		return 0
	}
	thr := float32(bg + apDiskFrac*(pk-bg))
	var sum, sum2 float64
	n := 0
	for y := 1; y < h-1; y++ {
		row := y * w
		for x := 1; x < w-1; x++ {
			c := p[row+x]
			if c <= thr {
				continue
			}
			lap := float64(4*c - p[row+x-1] - p[row+x+1] - p[row-w+x] - p[row+w+x])
			sum += lap
			sum2 += lap * lap
			n++
		}
	}
	if n < 100 {
		return 0
	}
	mean := sum / float64(n)
	v := sum2/float64(n) - mean*mean
	return v / ((pk - bg) * (pk - bg))
}

// laplacianVariance is a standard focus/sharpness metric: higher means sharper.
func laplacianVariance(grid []float64, w, h int) float64 {
	if w < 3 || h < 3 {
		return 0
	}
	var sum, sum2 float64
	n := 0
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			c := grid[y*w+x]
			lap := 4*c - grid[y*w+x-1] - grid[y*w+x+1] - grid[(y-1)*w+x] - grid[(y+1)*w+x]
			sum += lap
			sum2 += lap * lap
			n++
		}
	}
	if n == 0 {
		return 0
	}
	mean := sum / float64(n)
	return sum2/float64(n) - mean*mean
}

func extractFrames(ctx context.Context, ffmpegBin, video, destDir string) error {
	if ffmpegBin == "" {
		ffmpegBin = "ffmpeg"
	}
	cmd := exec.CommandContext(ctx, ffmpegBin, "-y", "-i", video,
		filepath.Join(destDir, "f_%05d.png"))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg extract: %w\n%s", err, lastLines(string(out), 5))
	}
	return nil
}

func report(onProgress func(siril.Progress), step string) {
	if onProgress != nil {
		onProgress(siril.Progress{Line: "", Percent: -1})
		onProgress(siril.Progress{Line: step})
	}
}

func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		case r == ' ' || r == '.':
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "video"
	}
	return string(out)
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
