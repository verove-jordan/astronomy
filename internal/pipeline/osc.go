package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/gimp"
	"github.com/verove-jordan/astronomy/internal/grade"
	"github.com/verove-jordan/astronomy/internal/graxpert"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/nightscape"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/rawconv"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// ProcessOSC runs the one-shot-color pipeline (milkyway / iPhone ProRAW/HEIC): convert+debayer →
// register → grade → stack → background-extract + stretch → GIMP curves. No per-filter channels
// and no calibration library (phone captures rarely have darks/flats).
func ProcessOSC(ctx context.Context, opts Options) (*Result, error) {
	if err := opts.Runner.Available(ctx); err != nil {
		return nil, fmt.Errorf("siril unavailable: %w", err)
	}
	defer opts.freePulledMasters(ctx) // discard any phone masters pulled from the S3 library mirror this run
	// One-shot-color source: raw stills (iPhone/DSLR), or — for older OSC captures — Bayer CFA FITS,
	// which Siril demosaics with `convert -debayer`.
	roots := opts.scanRoots()
	frames, err := inspect.ListRawFramesMany(roots)
	if err != nil {
		return nil, err
	}
	debayer := false
	if len(frames) == 0 {
		frames, err = inspect.ListFITSFramesMany(roots)
		if err != nil {
			return nil, err
		}
		debayer = true
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("no color images found in %s", opts.InputDir)
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
	// Name the run after the target (walking past generic "Sorted_DNG"/date folders) so every night of
	// a target lands under output/<target>/<runID>, consistent with the deep-sky path.
	object := smartObject(opts.InputDir)
	if object == "" || object == "." || object == string(filepath.Separator) {
		object = "osc"
	}
	workRun := filepath.Join(workAbs, object, "run_"+runID)
	outDir := filepath.Join(outAbs, object, runID)
	if err := fsutil.EnsureDir(outDir); err != nil {
		return nil, err
	}
	res := &Result{InputDir: opts.InputDir, OutputDir: outDir, Object: object, RunID: runID}
	opts.PriorObject = object // key for the supervisor's cross-run memory (warm start)

	// Milky-Way nightscapes use the dedicated foreground-composite recipe (star-aligned sky stack +
	// single clean foreground), not the generic OSC stack. Bayer-FITS OSC captures (no raws to
	// develop) fall through to the generic path.
	if opts.Preset != nil && opts.Preset.Mode == mode.Milkyway && !debayer {
		return processNightscape(ctx, opts, res, frames, workRun, outDir)
	}

	seqDir := filepath.Join(workRun, "02_osc")
	opts.report(Progress{Step: "convert + register", Index: 1, Total: 3})
	if debayer {
		// Bayer FITS: link the frames into the sequence dir; `convert -debayer` demosaics them.
		if _, err := fsutil.LinkFrames(seqDir, frames); err != nil {
			return nil, fmt.Errorf("link OSC frames: %w", err)
		}
	} else {
		// iPhone/processed DNGs are frequently undecodable by Siril's bundled libraw (convert writes its
		// plan file but no FITS); transcode raws to TIFF — which Siril ingests natively — first.
		_, prepWarn, perr := rawconv.PrepareTIFF(ctx, frames, seqDir, func(i, n int, name string) {
			opts.report(Progress{Step: "convert + register", Index: 1, Total: 3, Line: fmt.Sprintf("prepared %d/%d %s", i, n, name)})
		})
		if perr != nil {
			return nil, fmt.Errorf("prepare OSC frames: %w", perr)
		}
		res.Warnings = append(res.Warnings, prepWarn...)
	}

	convert := "convert osc -out=.\n"
	if debayer {
		convert = "convert osc -debayer -out=.\n"
	}
	convReg := "requires 1.2.0\nsetext fits\nset32bits\n" + convert + "register osc\n"
	if cr, err := opts.Runner.Run(ctx, seqDir, convReg, opts.sirilLines("convert + register")); err != nil {
		return nil, fmt.Errorf("convert+register: %w\n%s", err, sirilTail(cr))
	}

	gradeOpts := grade.DefaultOptions()
	if opts.Grade != nil {
		gradeOpts = *opts.Grade
	}
	pseudo := make([]*inspect.Frame, len(frames))
	for i, p := range frames {
		pseudo[i] = &inspect.Frame{Path: p}
	}
	dropTransition := opts.Preset != nil && opts.Preset.DropFilterWheelTransition
	metrics, rejectedReg, regCount, err := gradeChannel(seqDir, "osc", pseudo, gradeOpts, dropTransition, nil, nil)
	if err != nil {
		return nil, err
	}
	if regCount == 0 {
		return nil, fmt.Errorf("no frames could be registered")
	}
	masterBase := filepath.Join(outDir, "osc_master")
	res.Channels = []ChannelResult{{
		Filter: "RGB", InputFrames: len(frames), StackedFrames: regCount - len(rejectedReg),
		Metrics: metrics, OutputPath: masterBase + ".fits",
	}}
	gradeWarnings(opts, &res.Channels[0], "RGB", metrics, regCount, regCount-len(rejectedReg))
	res.Warnings = append(res.Warnings, res.Channels[0].Warnings...)
	// Pointing-pattern diagnosis (dithered / drift / static) from the registration offsets — the
	// walking-noise risk evidence and its dithering advice, same as the mono channel path.
	if d := ditherReport(metrics); d != nil {
		res.Channels[0].Dither = d
		res.Channels[0].Selection.Notes = append(res.Channels[0].Selection.Notes, "capture offsets: "+d.Note)
	}
	appendDitherAdvice(res)

	// Line-aware satellite/aircraft-trail masking on the registered subs before stacking (no-op unless
	// the preset enables it — milkyway leaves it off). Soft-fail: stack as-is on any error.
	if opts.Preset != nil && opts.Preset.TrailMaskK > 0 {
		if summary, note, err := maskChannelTrails(seqDir, "r_osc", opts.Preset.TrailMaskK, nil); err != nil {
			res.Warnings = append(res.Warnings, "trail mask skipped: "+err.Error())
		} else if summary != nil {
			res.Channels[0].TrailMask = summary
			res.Warnings = append(res.Warnings, note)
		}
	}

	opts.report(Progress{Step: "stacking", Index: 2, Total: 3})
	st, stackNote, err := stackSelectedOrCopy(ctx, opts.Runner, seqDir, "r_osc", regCount, rejectedReg, masterBase, opts.lightStack(opts.stackWeight()), opts.sirilLines("stacking"))
	if err != nil {
		return nil, fmt.Errorf("stacking: %w\n%s", err, sirilTail(st))
	}
	if stackNote != "" {
		warnLive(opts, res, "RGB: "+stackNote)
	}

	// AI background extraction (GraXpert) on the linear OSC master — replaces Siril subsky at
	// finish when available; soft-fail leaves the master untouched.
	if aiBackground(ctx, opts) {
		if note := extractBackgroundAI(ctx, opts, masterBase+".fits", opts.sirilLines("background extraction")); note != "" {
			res.Warnings = append(res.Warnings, note)
		}
	}

	// Measure + denoise the linear OSC master (starlet when enabled, else Siril) before finishing.
	denoiseLinearMaster(ctx, opts, &res.Channels[0], "osc_master", outDir, "RGB", opts.sirilLines("denoise"))
	if opts.Preset != nil && opts.Preset.Previews {
		if _, err := opts.Runner.Run(ctx, outDir, siril.PreviewScript("osc_master.fits", "osc_master_preview", 0.5), nil); err == nil {
			res.Channels[0].PreviewPath = filepath.Join(outDir, "osc_master_preview.png")
			opts.report(Progress{Step: "preview", Index: 2, Total: 3, Preview: res.Channels[0].PreviewPath})
			capturePreview(ctx, opts, outDir, ordStacked, stageStacked, "", res.Channels[0].PreviewPath, false) // milestone
		}
	}

	opts.report(Progress{Step: "finishing (GIMP)", Index: 3, Total: 3})
	finishOSC(ctx, opts, res, masterBase+".fits", workRun, outDir)
	captureFinalPNG(ctx, opts, outDir, res.Final)    // milestone: the final image
	res.StagePreviews = collectStagePreviews(outDir) // persist the milestone timeline for reload
	stampFinishQuality(res)                          // objective colour/clipping guardrails on every run
	writeRunJSON(outDir, res)                        // durable, reopenable record
	return res, nil
}

func finishOSC(ctx context.Context, opts Options, res *Result, masterPath, workRun, outDir string) {
	stretchDir := filepath.Join(workRun, "05_stretched")
	if err := fsutil.EnsureDir(stretchDir); err != nil {
		res.Warnings = append(res.Warnings, "stretch dir: "+err.Error())
		return
	}
	deg := backgroundDegree(ctx, opts) // always [1,4]; gentle 1 after GraXpert, else the preset degree
	bgLevel := 0.0
	if opts.Preset != nil {
		bgLevel = opts.Preset.BackgroundLevel
	}
	base := filepath.Join(stretchDir, "base")
	// Linked + dark target background keeps the wide-field sky neutral and dark (not Siril's washed 0.25).
	script := "requires 1.2.0\nsetext fits\nset32bits\n" + fmt.Sprintf("load %s\n", masterPath) +
		siril.SubskyCmd(deg) + siril.AutostretchCmd(true, bgLevel) + fmt.Sprintf("\nsavetif %s\n", base)
	if st, err := opts.Runner.Run(ctx, stretchDir, script, opts.sirilLines("finishing (GIMP)")); err != nil {
		res.Warnings = append(res.Warnings, "background extraction/stretch failed: "+err.Error()+"\n"+sirilTail(st))
		return
	}

	curve, sat := []float64(nil), 0.10
	if opts.Preset != nil {
		curve, sat = opts.Preset.Curve, opts.Preset.Saturation
	}
	if opts.Gimp != nil {
		if err := opts.Gimp.Available(); err == nil {
			if g, gerr := gimp.BuildImage(opts.Gimp, gimp.Inputs{Base: base + ".tif", Color: true}, curve, 0, sat, filepath.Join(outDir, "final")); gerr == nil {
				res.Final = &postprocess.Result{Mode: "OSC-RGB", Channels: []string{"RGB"}, Outputs: []string{g.Xcf, g.Tif, g.Png}, Notes: []string{"one-shot-color + curves (GIMP)"}}
				return
			} else {
				if ctx.Err() != nil {
					res.Warnings = append(res.Warnings, "run cancelled — finishing skipped")
				} else {
					res.Warnings = append(res.Warnings, "GIMP finishing failed, keeping Siril stretch: "+gerr.Error())
				}
			}
		}
	}
	res.Final = &postprocess.Result{Mode: "OSC-RGB", Channels: []string{"RGB"}, Outputs: []string{base + ".tif"}, Notes: []string{"Siril stretch (GIMP unavailable)"}}
}

// processNightscape runs the Milky-Way nightscape recipe (foreground/background composite) and maps
// its result onto the standard Result/run.json contract so the UI and durable record are unchanged.
func processNightscape(ctx context.Context, opts Options, res *Result, frames []string, workRun, outDir string) (*Result, error) {
	opts.report(Progress{Step: "nightscape: register + composite", Index: 1, Total: 1})
	// Separate lights from any dark/flat/bias DNGs mixed into the input (auto-classified) so cal frames
	// calibrate the stack rather than being stacked as sky.
	lights, darkFrames, flatFrames, biasFrames := splitCalibrationFrames(ctx, frames)
	workAbs, _ := filepath.Abs(opts.WorkDir)
	libDir, _ := libraryDir(opts, workAbs)
	// GraXpert gradient removal + chroma denoise on the sky stack, honouring the preset toggle (and
	// --no-ai, which nils the runner). nil → the auto-levels still balance the sky without it.
	var grax *graxpert.Runner
	if opts.Preset.BackgroundAI {
		grax = opts.Graxpert
	}
	nopts := nightscape.Options{
		Siril:                 opts.Runner,
		Graxpert:              grax,
		Frames:                lights,
		WorkDir:               workRun,
		OutDir:                outDir,
		Look:                  nightscape.LookByName(opts.Preset.Look),
		Brightness:            opts.Preset.BackgroundLevel,
		SaturationScale:       opts.Preset.Saturation,
		HighlightCeilOverride: opts.Preset.HighlightCeil,
		ColorCalibration:      opts.Preset.ColorCalibration,
		Solve:                 opts.Solve,
		Spcc:                  opts.Spcc,
		Focal35mm:             nightscape.ReadFocal35mm(lights),
		DarkDir:               opts.DarkDir,
		FlatDir:               opts.FlatDir,
		BiasDir:               opts.BiasDir,
		DarkFrames:            darkFrames,
		FlatFrames:            flatFrames,
		BiasFrames:            biasFrames,
		PhoneCalib:            opts.PhoneCalib,
		LibraryDir:            libDir,
		LibraryMirror:         opts.LibraryMirror, // pull matched phone masters from S3 when absent locally
		ForegroundFrame:       opts.Preset.ForegroundFrame,
		Orientation:           opts.Preset.Orientation,
		OnProgress:            opts.sirilLines("nightscape: register + composite"),
	}
	nres, err := nightscape.Process(ctx, nopts)
	if err != nil {
		return nil, fmt.Errorf("nightscape: %w", err)
	}
	res.Warnings = append(res.Warnings, nres.Warnings...)
	res.Channels = []ChannelResult{{
		Filter: "RGB", InputFrames: nres.InputFrames, StackedFrames: nres.StackedFrames,
		OutputPath: nres.CompositeFITS, PreviewPath: nres.PreviewPNG,
	}}
	res.Final = &postprocess.Result{
		Mode: "OSC-RGB nightscape", Channels: []string{"RGB"},
		Outputs: []string{nres.FinalPNG, nres.CompositeFITS, nres.SkyFITS, nres.ForegroundFITS},
		Notes:   []string{fmt.Sprintf("foreground composite, %s look (%dx%d)", nopts.Look.Name, nres.Width, nres.Height)},
	}
	// Optional supervised finish (opt-in): re-tune the grade (look + sky brightness) from the persisted
	// pre-grade linear inputs, keeping the best pass. Soft-fall to the standard finish above on any error.
	if superviseOn(ctx, opts) {
		if final, serr := superviseFinishNightscape(ctx, opts, outDir); serr != nil {
			res.Warnings = append(res.Warnings, "supervised milkyway finish failed, using standard finish: "+serr.Error())
		} else {
			res.Final = final
		}
	}
	if nres.SkyFITS != "" { // milestone: the stacked sky master (linear → autostretched)
		capturePreview(ctx, opts, outDir, ordStacked, stageStacked, "", nres.SkyFITS, true)
	}
	if nres.PreviewPNG != "" {
		opts.report(Progress{Step: "nightscape: register + composite", Index: 1, Total: 1, Preview: nres.PreviewPNG})
		capturePreview(ctx, opts, outDir, ordFinal, stageFinal, "", nres.PreviewPNG, false) // milestone: the final
	}
	res.StagePreviews = collectStagePreviews(outDir) // persist the milestone timeline for reload
	stampFinishQuality(res)                          // objective colour/clipping guardrails on every run
	writeRunJSON(outDir, res)
	return res, nil
}

// splitCalibrationFrames classifies raw stills and separates lights from auto-detected dark/flat/bias
// calibration frames, so cal DNGs mixed into the input dir calibrate the stack instead of being stacked
// as sky. If classification would leave no lights (e.g. everything mislabeled), it treats every frame
// as a light — the proven behavior — rather than starving the stack.
func splitCalibrationFrames(ctx context.Context, frames []string) (lights, darks, flats, bias []string) {
	for _, fr := range inspect.ClassifyRawStills(ctx, frames) {
		switch fr.Type {
		case inspect.Dark:
			darks = append(darks, fr.Path)
		case inspect.Flat:
			flats = append(flats, fr.Path)
		case inspect.Bias, inspect.DarkFlat:
			bias = append(bias, fr.Path)
		default:
			lights = append(lights, fr.Path)
		}
	}
	if len(lights) == 0 {
		return frames, nil, nil, nil
	}
	return lights, darks, flats, bias
}
