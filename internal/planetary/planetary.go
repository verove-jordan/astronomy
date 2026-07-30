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
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/buildinfo"
	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/comet"
	"github.com/verove-jordan/astronomy/internal/filters"
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
	APWeights   bool                  // per-AP top-K SELECTION: each region stacks only the frames sharpest there
	DoubleStack bool                  // re-align the originals onto the pass-1 master (dense AP grid) and re-stack
	Calibrate   bool                  // apply master dark/flat to the frames (masters from the capture's own cal frames or the library; soft-skips when none match)
	Formats     []string              // output formats: png, tif, fits
	Finish      siril.PlanetaryFinish // stretch/sharpen/contrast/saturation of the finish (tuned by the supervisor)
	// DrizzleScale stacks onto a finer output grid (1 / 1.5 / 2 — snapped): hundreds of
	// sub-pixel-dithered frames genuinely add resolution on the finer raster. Costs ×scale²
	// memory/time through warp, stack, deconv and finish. ≤0 (a zero-value Options) = native.
	DrizzleScale float64
	// AlignPoints overrides the dense alignment-point grid with a TOTAL point count
	// (AutoStakkert-style, user-friendly): per-axis N = clamp(round(√AlignPoints), 10, 48) — up to
	// 48×48 = 2304 points. 0 = auto (min(w,h)/120 px cells capped at 32×32 — today's formula).
	AlignPoints int

	// Richardson-Lucy deconvolution of the luminance (0 → the package defaults 2.8 / 18 / 700).
	// The old constants (FWHM 3, 10 iters, alpha 1800) were so strongly regularized the deconv was
	// nearly a no-op — the stack looked soft even when the master was sharp.
	DeconvFWHM  float64
	DeconvIters int
	DeconvAlpha float64
}

// DefaultOptions returns sensible defaults (drizzle 1.5× — the sharpness the lucky stack
// gathers deserves the finer grid by default; set 1 to stack at native resolution).
func DefaultOptions() Options {
	return Options{BestPercent: 15, Sharpen: true, APAlign: true, APWeights: true, DoubleStack: true,
		Calibrate: true, Formats: []string{"png", "tif"}, Finish: DefaultFinish(), DrizzleScale: 1.5}
}

// DefaultFinish is the original mineral-Moon finish tuning (see siril.DefaultPlanetaryFinish).
func DefaultFinish() siril.PlanetaryFinish { return siril.DefaultPlanetaryFinish() }

// FrameScore is one input frame's lucky-imaging quality record, for the frame report (kept/rejected).
type FrameScore struct {
	Index  int     `json:"index"`            // 1-based position within its channel
	File   string  `json:"file"`             // source frame basename
	Filter string  `json:"filter,omitempty"` // "" for a mono run
	Score  float64 `json:"score"`            // sharpness (noise-corrected band-pass detail); higher = sharper
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
	// BestFrameDetail / MasterDetail are the noise-corrected band-pass detail (quality.go) of
	// the best kept input frame vs the finished detail master — the objective "the stack must
	// out-detail the best single frame" acceptance (pass: master ≥ 1.05× best frame; a miss
	// lands in Notes as a warning; the comparison values are always logged as an info note).
	BestFrameDetail float64 `json:"best_frame_detail,omitempty"`
	MasterDetail    float64 `json:"master_detail,omitempty"`
	Sharpen         bool    `json:"sharpen,omitempty"`  // whether the finish sharpens (from Options.Sharpen)
	OutBase         string  `json:"out_base,omitempty"` // canonical final base path (<outDir>/<object>_stack)
	// Run identity (mirrors pipeline.Result) so the run lives at output/<object>/<run_id> and a post-run
	// refine + full-S3 push can resolve it. json tags match pipeline.Result / job.s3ResultTarget.
	Object    string `json:"object,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	OutputDir string `json:"output_dir,omitempty"`
	Engine    string `json:"engine,omitempty"` // build that produced this run (see internal/buildinfo)
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
// extras (nil-safe) carries the pipeline wiring: the Import multi-select roots (cal-frame folders live
// beside the lights folder), the calibration source (nil → uncalibrated, the historical behavior —
// only consulted for folder inputs, whose frames carry the metadata masters match on), and the
// overall-percent hook for the job bar.
func Process(ctx context.Context, runner *siril.Runner, ffmpegBin, inputPath, workDir, outDir string,
	opts Options, extras *RunExtras, onProgress func(siril.Progress)) (*Result, error) {
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
	res := &Result{Source: inputPath, Object: object, RunID: runID, OutputDir: runDir, Engine: buildinfo.String()}

	// Start from a clean per-object scratch dir: stale aligned frames / Siril .seq files from a prior run
	// of the same target would otherwise collide with `convert`/`stack` (it picks up leftover al_*.fits).
	runRoot := filepath.Join(workAbs, "planetary_"+object)
	_ = os.RemoveAll(runRoot)

	// Live-note sink: every note is both persisted on the Result AND reported to the live journal the
	// moment it happens — a run that skips calibration must say so while it runs, not at the end.
	addNote := func(s string) {
		res.Notes = append(res.Notes, s)
		report(onProgress, s)
	}
	prog := newRunProgress(extras.onPercent())

	// 1. Classify the input into per-channel SOURCE groups (folder) or a converted mono sequence
	// (video/SER). Folder frames materialize one channel at a time below, so scratch peaks at a single
	// channel instead of the whole capture.
	report(onProgress, "preparing frames")
	in, err := sourceChannels(ctx, runner, ffmpegBin, inputPath, extras.roots(inputPath), workAbs, object, onProgress)
	if err != nil {
		return nil, err
	}
	if len(in.order) == 0 {
		return nil, fmt.Errorf("no frames to process in %s", inputPath)
	}
	for _, n := range in.notes {
		addNote(n)
	}

	// 1b. Process only the channels the finish consumes: a full R/G/B trio → the LRGB combine uses
	// L,R,G,B; otherwise the mono finish uses the first canonical channel alone. Skipped channels (e.g.
	// an Ha set beside LRGB) never convert, so they cost no scratch or time.
	used, skipped := usedChannels(in.order)
	for _, f := range skipped {
		addNote(fmt.Sprintf("skipping unused channel %s (%d frames) — not used by the %s finish",
			channelLabel(f), len(in.groups[f]), finishLabel(used)))
	}

	// 1c. Calibration masters, built/reused once for the run (folder inputs only — video frames carry
	// no matchable metadata). Any failure is a note, never a run error.
	calSrc := extras.calib()
	var calMasters []calib.Master
	if calSrc != nil && calSrc.Build != nil && in.inv != nil {
		report(onProgress, "building calibration masters")
		m, warns, berr := calSrc.Build(ctx, in.inv, filepath.Join(runRoot, "calmasters"))
		for _, w := range warns {
			addNote(w)
		}
		switch {
		case berr != nil && ctx.Err() != nil:
			return nil, berr
		case berr != nil:
			addNote("calibration masters unavailable: " + berr.Error())
		default:
			calMasters = m
		}
	}

	// 2. Per channel: materialize → calibrate → rank → keep best % → surface-align → normalized stack →
	// linear master, then drop the channel's frame scratch (only its small master survives to the finish).
	chSpan := (100 - finishWeight) / float64(len(used))
	masters := map[string]string{} // filter → master base path (no extension)
	for _, filter := range used {
		report(onProgress, "processing "+channelLabel(filter))
		prog.phase(chSpan * phaseMaterialize)
		frames, framesDir := in.frames[filter], in.stagedDir[filter]
		if frames == nil {
			frames, framesDir, err = materializeChannel(ctx, runner, inputPath, in.groups[filter], filter, workAbs, object, onProgress)
			if err != nil {
				return nil, err
			}
		}
		chDir := filepath.Join(runRoot, "ch_"+channelLabel(filter))
		prog.phase(chSpan * phaseCalibrate)
		if len(calMasters) > 0 {
			report(onProgress, "calibrating "+channelLabel(filter))
			var cnotes []string
			frames, cnotes, err = calibrateChannel(ctx, frames, filter, in.inv, calMasters, calSrc.Exclude, calSrc.Force, chDir, runRoot, prog.tick)
			if err != nil {
				return nil, err
			}
			for _, n := range cnotes {
				addNote(n)
			}
		}
		master, frameReport, kept, chNotes, serr := stackChannel(ctx, runner, frames, filter, workAbs, object, opts, prog, chSpan, onProgress)
		res.Notes = append(res.Notes, chNotes...)
		if serr != nil {
			return nil, fmt.Errorf("stack %s: %w", channelLabel(filter), serr)
		}
		cleanupChannel(runRoot, chDir, framesDir)
		masters[filter] = master
		res.Frames = append(res.Frames, frameReport...)
		res.FrameCount += len(frames)
		res.StackedFrames += kept
	}
	prog.phase(finishWeight)

	// 3. Finish: co-register channels + colour LRGB combine when R/G/B exist, else the mono master.
	report(onProgress, "finishing")
	outBase := filepath.Join(runDir, object+"_stack")
	r, g, b, l, mono := masters["R"], masters["G"], masters["B"], masters["L"], ""
	if r != "" && g != "" && b != "" {
		coNotes, cerr := coRegisterMasters(masters, refChannel(masters), opts.AlignPoints)
		res.Notes = append(res.Notes, coNotes...)
		if cerr != nil {
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
			chromaRadius := int(math.Round(chromaBlur * SnapDrizzle(opts.DrizzleScale)))
			if serr := smoothChroma(masters, chromaRadius); serr != nil {
				// Mismatched channel canvases (e.g. one channel's double-stack pass rebuilt while
				// another kept its pass-1 master) must degrade to un-smoothed colour, not kill a
				// multi-hour stack at its very last step.
				if !errors.Is(serr, errChromaDimsMismatch) {
					return nil, fmt.Errorf("smooth chroma: %w", serr)
				}
				res.Notes = append(res.Notes, "colour smoothing skipped: "+serr.Error())
			}
		}
		res.Notes = append(res.Notes, "colour LRGB lucky-imaging stack (per-filter surface alignment)")
	} else {
		mono = masters[used[0]]
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
		detailFilter = used[0]
	}
	detailMaster := l
	if detailMaster == "" {
		detailMaster = mono
	}
	if detailMaster != "" {
		for _, fr := range res.Frames {
			if fr.Kept && fr.Filter == detailFilter && fr.Score > res.BestFrameDetail {
				res.BestFrameDetail = fr.Score
			}
		}
		res.MasterDetail = masterDetailNative(detailMaster+".fits", SnapDrizzle(opts.DrizzleScale))
		res.Notes = append(res.Notes, fmt.Sprintf(
			"detail metric: master %.3g vs best single frame %.3g (acceptance gate ≥1.05×)",
			res.MasterDetail, res.BestFrameDetail))
		if res.BestFrameDetail > 0 && res.MasterDetail > 0 && res.MasterDetail < 1.05*res.BestFrameDetail {
			res.Notes = append(res.Notes, fmt.Sprintf(
				"warning: stacked master detail (%.3g) below 1.05x the best single frame (%.3g) — check alignment/selection",
				res.MasterDetail, res.BestFrameDetail))
		}
	}

	finishNotes, ferr := runFinish(ctx, runner, runDir, r, g, b, l, mono, outBase, opts.Sharpen, opts.Finish, opts.Formats, onProgress)
	res.Notes = append(res.Notes, finishNotes...)
	if ferr != nil {
		return nil, ferr
	}
	for _, f := range opts.Formats {
		res.Outputs = append(res.Outputs, outBase+"."+f)
	}
	res.Notes = append(res.Notes,
		fmt.Sprintf("sub-pixel surface alignment; kept best %d%% of frames by sharpness", opts.BestPercent))
	if scale := SnapDrizzle(opts.DrizzleScale); scale != 1 {
		res.Notes = append(res.Notes, fmt.Sprintf("drizzle ×%.1f super-resolution grid", scale))
	}
	prog.finish()
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
	notes, err := runFinish(ctx, runner, outDir, r, g, b, l, mono, outBase, sharpen, fin, formats, nil)
	if err != nil {
		return nil, err
	}
	res := &Result{OutBase: outBase, Sharpen: sharpen, Masters: masters, Notes: notes}
	for _, f := range formats {
		res.Outputs = append(res.Outputs, outBase+"."+f)
	}
	return res, nil
}

// channelInputs is the classified, un-materialized input of a run: per-filter SOURCE frame paths
// (folder runs — converted one channel at a time to bound scratch) or pre-materialized FITS (video/SER),
// plus the scan inventory that calibration matches against (folder runs only).
type channelInputs struct {
	groups    map[string][]string // filter → source paths (folder runs)
	frames    map[string][]string // filter → already-materialized FITS (video/SER)
	stagedDir map[string]string   // filter → scratch dir holding those frames ("" / absent = none yet)
	order     []string            // canonical channel order over every classified group
	inv       *inspect.Inventory  // folder runs only; nil for video/SER
	notes     []string
}

// sourceChannels prepares a run's channel inputs. A folder is classified only (no conversion yet),
// merging every Import-selected root — darks/flats/offsets folders beside the lights feed the masters
// build; a video/SER is extracted+converted immediately into a single pre-materialized mono group.
func sourceChannels(ctx context.Context, runner *siril.Runner, ffmpegBin, inputPath string, roots []string,
	workAbs, object string, onProgress func(siril.Progress)) (channelInputs, error) {
	if info, statErr := os.Stat(inputPath); statErr == nil && info.IsDir() {
		return classifyFolderChannels(ctx, roots)
	}
	frames, err := convertVideo(ctx, runner, ffmpegBin, inputPath, workAbs, object, onProgress)
	if err != nil {
		return channelInputs{}, err
	}
	return channelInputs{
		frames:    map[string][]string{"": frames},
		stagedDir: map[string]string{"": filepath.Join(workAbs, "planetary_"+object, "mono")},
		order:     []string{""},
	}, nil
}

// classifyFolderChannels scans the capture roots (the Import multi-select — one folder or several) and
// groups the light frames per filter — source paths only. A filter wheel (≥2 distinct mono filters)
// yields one group per filter (colour); anything else a single "" mono group. The mono group prefers
// the classified lights (+ still-unknown stills), so a darks/ or flats/ sub-folder is no longer swept
// into the sequence; a folder whose stills never enter the inventory at all (PNG/JPG) leaves the group
// empty and materializeChannel falls back to the old recursive blind walk.
func classifyFolderChannels(ctx context.Context, roots []string) (channelInputs, error) {
	inv, err := inspect.ScanMany(ctx, roots, inspect.ScanOptions{})
	if err != nil {
		return channelInputs{}, err
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
		// Mono (or single-filter): one group of every usable still, in stable order.
		var srcs []string
		excluded := 0
		for _, fr := range inv.Frames {
			if fr.Type == inspect.Light || fr.Type == inspect.Unknown {
				srcs = append(srcs, fr.Path)
			} else {
				excluded++
			}
		}
		sort.Strings(srcs)
		in := channelInputs{groups: map[string][]string{"": srcs}, order: []string{""}, inv: inv}
		if excluded > 0 && len(srcs) > 0 {
			in.notes = append(in.notes, fmt.Sprintf("excluded %d calibration frame(s) from the mono sequence", excluded))
		}
		return in, nil
	}

	groups := map[string][]string{}
	for filter, paths := range byFilter {
		if filter == "" {
			continue // stray unfiltered frames are not a colour channel
		}
		groups[filter] = paths
	}
	return channelInputs{groups: groups, order: channelOrder(groups), inv: inv}, nil
}

// usedChannels splits the classified channel order into the channels the finish consumes and the rest:
// a full R/G/B trio → the LRGB combine uses only L,R,G,B; otherwise the mono finish uses the first
// canonical channel alone (identical to the historical order[0] pick).
func usedChannels(order []string) (used, skipped []string) {
	present := map[string]bool{}
	for _, f := range order {
		present[f] = true
	}
	if present["R"] && present["G"] && present["B"] {
		for _, f := range order {
			if f == "L" || f == "R" || f == "G" || f == "B" {
				used = append(used, f)
			} else {
				skipped = append(skipped, f)
			}
		}
		return used, skipped
	}
	if len(order) > 0 {
		used, skipped = order[:1], order[1:]
	}
	return used, skipped
}

// finishLabel names the finish a skipped-channel note refers to.
func finishLabel(used []string) string {
	if len(used) > 1 {
		return "LRGB"
	}
	if len(used) == 1 {
		return channelLabel(used[0]) + "-only"
	}
	return "mono"
}

// materializeChannel turns one channel's source frames into rankable/alignable FITS, converting only
// when needed: an all-FITS colour group is used in place (no convert, real names in the report);
// anything else is staged and Siril-converted into the channel scratch. Returns the frame paths plus
// the scratch dir that holds them ("" when the sources are used in place).
func materializeChannel(ctx context.Context, runner *siril.Runner, inputPath string, srcs []string,
	filter, workAbs, object string, onProgress func(siril.Progress)) ([]string, string, error) {
	runRoot := filepath.Join(workAbs, "planetary_"+object)
	if filter == "" {
		dir := filepath.Join(runRoot, "mono")
		if err := fsutil.EnsureDir(dir); err != nil {
			return nil, "", err
		}
		var n int
		var err error
		if len(srcs) > 0 {
			n, err = stageFiles(srcs, dir, "frame")
		} else {
			n, err = stageImageFrames(inputPath, dir)
		}
		if err != nil {
			return nil, "", err
		}
		if n == 0 {
			return nil, "", fmt.Errorf("no image frames found in %s (expected FITS/TIFF/PNG/raw stills)", inputPath)
		}
		frames, cerr := convertSeq(ctx, runner, dir, onProgress)
		return frames, dir, cerr
	}
	dir := filepath.Join(runRoot, "ch_"+channelLabel(filter))
	frames, err := framesAsFITS(ctx, runner, srcs, dir, onProgress)
	if err != nil {
		return nil, "", fmt.Errorf("prepare %s frames: %w", filter, err)
	}
	staged := dir
	if len(frames) > 0 && !underDir(frames[0], runRoot) {
		staged = "" // all-FITS group used in place — nothing was staged
	}
	return frames, staged, nil
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
		pixFmt, err := extractFrames(ctx, ffmpegBin, inputPath, dir)
		if err != nil {
			return nil, err
		}
		if pixFmt != "" {
			report(onProgress, "extracting video frames at 16-bit ("+pixFmt+")")
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

// stackChannel runs the lucky-imaging stack for one channel: rank by sharpness, keep the best %,
// surface-align the kept frames to the sharpest, normalize-stack them into a linear master, then —
// with DoubleStack — re-register the originals onto that master (dense AP grid) and re-stack. It
// returns the master base path (no extension), the per-frame report, the number of frames stacked
// and run notes. The ranking, alignment and stack run on bounded parallel workers; prog/chSpan
// drive the run's score/align/stack progress phases.
func stackChannel(ctx context.Context, runner *siril.Runner, frames []string, filter, workAbs, object string,
	opts Options, prog *runProgress, chSpan float64, onProgress func(siril.Progress)) (string, []FrameScore, int, []string, error) {
	if len(frames) == 0 {
		return "", nil, 0, nil, fmt.Errorf("no frames")
	}
	prog.phase(chSpan * phaseScore)
	report(onProgress, "ranking "+channelLabel(filter))
	scores := make([]float64, len(frames))
	if err := forEachFrame(ctx, len(frames), planetaryWorkers(), func(i int) error {
		scores[i] = frameSharpness(frames[i]) // 0 on read failure, same as the serial loop
		prog.tick(i+1, len(frames))
		return nil
	}); err != nil {
		return "", nil, 0, nil, err
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
		return "", frameReport, 0, nil, err
	}
	// Register the kept frames onto the sharpest one and warp each with a SINGLE Catmull-Rom resample.
	// When multi-point alignment is on (and >1 frame) the warp also cancels the local atmospheric
	// distortion a single global shift can't; otherwise it is one global cubic shift. With DoubleStack
	// the channel's align/stack progress budget splits across the two passes (bar shaping only).
	apAlign := opts.APAlign && len(keptPaths) > 1
	doubleStack := apAlign && opts.DoubleStack && len(keptPaths) >= doubleStackMin
	alignSpan, stackSpan := chSpan*phaseAlign, chSpan*phaseStack
	if doubleStack {
		alignSpan, stackSpan = alignSpan*0.5, stackSpan*0.35
	}
	prog.phase(alignSpan)
	if apAlign {
		report(onProgress, "multi-point aligning "+channelLabel(filter))
	} else {
		report(onProgress, "aligning "+channelLabel(filter))
	}
	pass1, err := warpToSharpest(ctx, keptPaths, keptScores, alignDir, "f", apAlign, SnapDrizzle(opts.DrizzleScale), opts.AlignPoints, prog.tick)
	if err != nil {
		return "", frameReport, 0, nil, err
	}
	if len(pass1.paths) == 0 {
		return "", frameReport, 0, nil, fmt.Errorf("no frames survived alignment")
	}
	// Sharpness-weighted stack (Go, not Siril): weight each aligned frame by its own sharpness so the
	// lucky-sharp frames dominate and the master rivals a single frame, instead of the blurry average a
	// plain mean produces — and, with APWeights, each REGION is built from only the frames that were
	// sharpest there (AutoStakkert-style per-AP top-K SELECTION, apSelectionFields). Siril can't weight
	// a starless stack, so this is done in-process (stack.go).
	prog.phase(stackSpan)
	report(onProgress, "stacking "+channelLabel(filter))
	master := filepath.Join(chDir, "master_"+channelLabel(filter)) // no extension; .fits added on write
	if serr := stackAligned(ctx, pass1.paths, pass1.cellSharp, opts, master, prog.tick); serr != nil {
		return "", frameReport, 0, nil, fmt.Errorf("weighted stack %s: %w", channelLabel(filter), serr)
	}
	stacked := len(pass1.paths)
	var notes []string
	if pass1.note != "" {
		notes = append(notes, channelLabel(filter)+": "+pass1.note)
	}
	if pass1.gridNote != "" {
		notes = append(notes, channelLabel(filter)+": "+pass1.gridNote)
	}
	if doubleStack {
		note, n2 := runDoubleStack(ctx, keptPaths, pass1, chDir, alignDir, master, filter, opts, prog,
			chSpan*phaseAlign*0.5, chSpan*phaseStack*0.65, onProgress)
		if note != "" {
			notes = append(notes, note)
		}
		if n2 > 0 {
			stacked = n2
		}
	}
	return master, frameReport, stacked, notes, nil
}

// stackAligned scores the aligned frames, builds the per-AP selection fields and runs the
// sharpness-weighted stack into masterPath — shared by the pass-1 and double-stack stacks.
func stackAligned(ctx context.Context, aligned []string, cellSharp [][]float64, opts Options,
	masterPath string, tick func(done, total int)) error {
	scores := make([]float64, len(aligned))
	if err := forEachFrame(ctx, len(aligned), planetaryWorkers(), func(i int) error {
		scores[i] = frameSharpness(aligned[i])
		return nil
	}); err != nil {
		return err
	}
	var apFields [][]float64
	if opts.APWeights && len(cellSharp) == len(aligned) && len(cellSharp) > 1 {
		apFields = apSelectionFields(cellSharp)
	}
	return stackWeightedFileAP(ctx, aligned, scores, apFields, masterPath, tick)
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

// deconvParams resolves the run's Richardson-Lucy settings (Options overrides, else the
// defaults). The PSF FWHM is a NATIVE-pixel quantity, so it scales with the drizzle grid —
// the same physical seeing blur spans scale× more output pixels.
func deconvParams(opts Options) (fwhm float64, iters int, alpha float64) {
	fwhm, iters, alpha = deconvFWHMDefault, deconvItersDefault, deconvAlphaDefault
	if opts.DeconvFWHM > 0 {
		fwhm = opts.DeconvFWHM
	}
	fwhm *= SnapDrizzle(opts.DrizzleScale)
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

// errChromaDimsMismatch marks R/G/B masters whose canvases disagree — colour smoothing is then
// impossible and the caller degrades to the un-smoothed trio instead of failing the run.
var errChromaDimsMismatch = errors.New("smooth chroma: master dimensions differ")

// chromaBlur is the smoothing radius for the R/G/B COLOUR DIFFERENCES before the LRGB combine. Each
// channel stack aligns to its OWN sharpest frame, so their atmospheric micro-warps disagree coherently
// over ~10–15px regions — on a real full-Moon LRGB that reads as green/magenta mottling across the
// surface, so the radius must cover that scale. Only the differences are smoothed (see smoothChroma):
// a plain per-channel blur at this radius visibly softened the FINAL image (Siril's `rgbcomp -lum`
// leaks RGB detail into the output lightness), while the mean-preserving smooth keeps it crisp.
const chromaBlur = 12

// smoothChroma smooths the COLOUR of the R/G/B masters in place while preserving their per-pixel mean
// exactly: m = (R+G+B)/3, then c' = m + blur(c − m). By linearity blur(ΣΔ)=Σblur(Δ)=0, so (R'+G'+B')/3
// is byte-for-byte m — every detail the combine may take from the RGB trio survives; only the mutual
// channel disagreements (the colour mottle from per-channel seeing warps) are flattened.
func smoothChroma(masters map[string]string, radius int) error {
	ims := map[string]*fits.Image{}
	for _, f := range []string{"R", "G", "B"} {
		base, ok := masters[f]
		if !ok {
			return nil // colour smoothing only makes sense with the full trio
		}
		im, err := fits.ReadImage(base + ".fits")
		if err != nil {
			return err
		}
		ims[f] = im
	}
	r, g, b := ims["R"].Pix[0], ims["G"].Pix[0], ims["B"].Pix[0]
	if len(g) != len(r) || len(b) != len(r) {
		return fmt.Errorf("%w: R %dx%d, G %dx%d, B %dx%d", errChromaDimsMismatch,
			ims["R"].W, ims["R"].H, ims["G"].W, ims["G"].H, ims["B"].W, ims["B"].H)
	}
	mean := make([]float32, len(r))
	for i := range mean {
		mean[i] = (r[i] + g[i] + b[i]) / 3
	}
	for _, f := range []string{"R", "G", "B"} {
		im := ims[f]
		pix := im.Pix[0]
		diff := make([]float32, len(pix))
		for i := range diff {
			diff[i] = pix[i] - mean[i]
		}
		sm := comet.BoxBlur(diff, im.W, im.H, radius)
		for i := range pix {
			pix[i] = mean[i] + sm[i]
		}
		if err := im.WriteFITS(masters[f] + ".fits"); err != nil {
			return err
		}
	}
	return nil
}

// coRegisterMasters aligns every channel master onto the reference channel and overwrites them, so the
// subsequent rgbcomp has no channel-to-channel offset. Each per-filter stack was aligned to its OWN
// sharpest frame (captured at a different instant), so the channels start not just globally shifted but
// with DIFFERENT per-AP atmospheric-warp residuals — a single global translation leaves green/red fringes
// in the corners. So each non-reference master is warped onto the reference with the SAME two-level
// displacement field the per-frame lucky alignment uses (global centroid+coarse+fine ZNCC baseline →
// coarse 10×10 AP field → dense seeded refinement, one Catmull-Rom resample) — the dense level keeps
// the channels registered at the sub-coarse-cell scale the colours are judged at. ZNCC is
// contrast-normalized, so the cross-filter (R/G/B vs L) correlation on the high-SNR surface is reliable.
func coRegisterMasters(masters map[string]string, refFilter string, alignPoints int) ([]string, error) {
	ref, err := fits.ReadImage(masters[refFilter] + ".fits")
	if err != nil {
		return nil, err
	}
	rc := newRefContext(ref)
	rcD := denseContextFrom(ref, rc, alignPoints)
	var notes []string
	for f, base := range masters {
		if f == refFilter {
			continue
		}
		im, err := fits.ReadImage(base + ".fits")
		if err != nil {
			return notes, err
		}
		// Every master must live on the reference raster before the field measurement: the channels
		// stack independently (and soft-fail passes independently), so a master arriving at another
		// canvas is repaired here — the downstream colour smoothing/combine reads the trio blind.
		if im.W != ref.W || im.H != ref.H {
			notes = append(notes, fmt.Sprintf("%s master resampled %dx%d → %dx%d to match the %s reference",
				channelLabel(f), im.W, im.H, ref.W, ref.H, channelLabel(refFilter)))
			im = resamplePlaneTo(im, ref.W, ref.H)
		}
		dxG, dyG := measureTwoLevelField(im, &rc, &rcD)
		aligned := warpByGrid(im, dxG, dyG)
		if err := aligned.WriteFITS(base + ".fits"); err != nil {
			return notes, err
		}
	}
	return notes, nil
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

// CanonicalFilters is the channel preference order shared by channelOrder and the align-points
// estimator's first-frame pick.
var CanonicalFilters = filters.Canonical

// channelOrder returns the channels present, in canonical L,R,G,B,… order (others appended sorted).
func channelOrder(groups map[string][]string) []string {
	canon := CanonicalFilters
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

// frameSharpness ranks a frame at FULL RESOLUTION over the lit disk only, with the
// noise-corrected band-pass detail metric (see quality.go): it isolates crater-scale
// structure and discounts the frame's own noise floor, so "keep the best N%" selects on
// exactly the detail the stack must preserve — a noisier frame can no longer outrank a
// sharper one. One frame in memory at a time.
func frameSharpness(path string) float64 {
	im, err := fits.ReadImage(path)
	if err != nil {
		return 0
	}
	return detailSNR(im)
}

// masterDetailNative scores a (possibly drizzled) master on the frames' NATIVE raster: the
// band-pass metric is frequency-selective, so the scaled master must come back to native
// before its score is compared against per-frame scores in the acceptance gate.
func masterDetailNative(path string, scale float64) float64 {
	im, err := fits.ReadImage(path)
	if err != nil {
		return 0
	}
	if scale != 1 {
		im = resamplePlane(im, 1/scale)
	}
	return detailSNR(im)
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

// extractFrames explodes a video into PNG frames, at 16 bits when the stream carries more
// than 8 (videoprobe.go). Returns the requested pix_fmt ("" = ffmpeg's 8-bit default).
func extractFrames(ctx context.Context, ffmpegBin, video, destDir string) (string, error) {
	if ffmpegBin == "" {
		ffmpegBin = "ffmpeg"
	}
	pixFmt := pngPixFmtFor(videoPixFmt(ctx, ffprobeBinFor(ffmpegBin), video))
	args := []string{"-y", "-i", video}
	if pixFmt != "" {
		args = append(args, "-pix_fmt", pixFmt)
	}
	args = append(args, filepath.Join(destDir, "f_%05d.png"))
	cmd := exec.CommandContext(ctx, ffmpegBin, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return pixFmt, fmt.Errorf("ffmpeg extract: %w\n%s", err, lastLines(string(out), 5))
	}
	return pixFmt, nil
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
