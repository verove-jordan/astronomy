package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/gimp"
	"github.com/verove-jordan/astronomy/internal/graxpert"
	"github.com/verove-jordan/astronomy/internal/llm"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/pipeline"
	"github.com/verove-jordan/astronomy/internal/planetary"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/report"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/starnet"
	"github.com/verove-jordan/astronomy/internal/store"
	"github.com/verove-jordan/astronomy/internal/videoout"
)

func runProcess(args []string) error {
	fs := flag.NewFlagSet("process", flag.ContinueOnError)
	out := fs.String("out", "", "output directory (default $ASTRO_OUTPUT_DIR)")
	work := fs.String("work", "", "scratch directory (default $ASTRO_WORK_DIR)")
	asJSON := fs.Bool("json", false, "emit the run result as JSON")
	verbose := fs.Bool("v", false, "stream Siril log lines")
	noDB := fs.Bool("no-db", false, "disable the calibration library (no database)")
	noAI := fs.Bool("no-ai", false, "skip optional AI tools (GraXpert background extraction, StarNet++ star removal)")
	look := fs.String("look", "", "milkyway render look: natural|iphone|deepsky (default natural)")
	foreground := fs.String("foreground", "", "milkyway: dedicated foreground frame (raw path); default auto-picks the reference frame")
	orientation := fs.String("orientation", "", "milkyway final orientation: auto|none|cw|ccw|180 (optionally +\"-flip\")")
	brightness := fs.String("brightness", "", "milkyway sky brightness: darker|balanced|brighter (or a 0..0.5 target); default balanced")
	darks := fs.String("darks", "", "milkyway: folder of dark calibration frames (optional)")
	flats := fs.String("flats", "", "milkyway: folder of flat calibration frames (optional)")
	bias := fs.String("bias", "", "milkyway: folder of bias/offset calibration frames (optional)")
	supervise := fs.Bool("supervise", false, "opt-in: drive a local AI agent (host model server) to auto-tune the finish; needs ASTRO_LLM_URL")
	noCrop := fs.Bool("no-crop", false, "export the full frame — skip the automatic ragged-edge crop so you can crop it yourself (the layered .xcf is always full-frame)")
	earthshine := fs.Float64("earthshine", 0, "planetary: reveal the Moon's unlit side (earthshine) in the final render; 0 = off, 1 = natural, up to 2")
	drizzle := fs.Float64("drizzle", 0, "planetary: super-resolution output grid (1, 1.5 or 2 — snapped); 0 keeps the preset default (1.5)")
	alignPoints := fs.Int("align-points", 0, "planetary: total stacking reference points for the distortion grid (100..2304, snapped to N×N); 0 = auto")
	target := fs.String("target", "", "imaging target for plate-solve/SPCC seeding — a catalogue name (\"M66\") or \"RA,Dec\" — when headers/folders can't identify it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 3 {
		return fmt.Errorf("usage: astrostack process [flags] <mode> <format> <path>\n" +
			"  modes: deepsky nebula milkyway nightpano planetary comet mosaic sun eclipse livestack\n" +
			"  formats: image video both")
	}
	m, err := mode.ParseMode(fs.Arg(0))
	if err != nil {
		return err
	}
	format, err := mode.ParseFormat(fs.Arg(1))
	if err != nil {
		return err
	}
	path := fs.Arg(2)

	preset := mode.For(m)
	if *look != "" {
		preset.Look = *look
	}
	if *foreground != "" {
		preset.ForegroundFrame = *foreground
	}
	if *orientation != "" {
		preset.Orientation = *orientation
	}
	if bg, ok := mode.BrightnessTarget(*brightness); ok {
		preset.BackgroundLevel = bg
	} else if *brightness != "" {
		return fmt.Errorf("invalid -brightness %q (want: darker|balanced|brighter or a 0..0.5 number)", *brightness)
	}
	if *noCrop {
		preset.CropFrac = 0 // export full-frame; the user crops the ragged stacking edges themselves
	}
	if *earthshine > 0 {
		preset.Planetary.Finish.EarthshineGain = *earthshine
	}
	if *drizzle > 0 {
		preset.Planetary.DrizzleScale = planetary.SnapDrizzle(*drizzle)
	}
	if *alignPoints > 0 {
		preset.Planetary.AlignPoints = planetary.SnapAlignPoints(*alignPoints)
	}
	cfg := config.Load()
	outDir := pick(*out, cfg.OutputDir)
	workDir := pick(*work, cfg.WorkDir)
	ctx := context.Background()
	runner := siril.New(cfg.SirilBin, sirilLimits(cfg))
	solve, spcc := postprocess.SolveSpccFromConfig(cfg)
	// Optional astro-AI host tools (skipped with -no-ai or when the binary is absent).
	var graxRunner *graxpert.Runner
	var starRunner *starnet.Runner
	if !*noAI {
		graxRunner = graxpert.New(cfg.GraxpertBin, cfg.GraxpertURL).SetDefaults(cfg.GraxpertGPU, cfg.GraxpertBatch)
		starRunner = starnet.New(cfg.StarnetBin)
	}
	// Optional local-AI-agent finish supervisor (opt-in via -supervise; nil → standard finish).
	var superRunner *llm.Runner
	if *supervise {
		superRunner = llm.New(cfg.LLMBaseURL, cfg.LLMModel, cfg.LLMImageFormat).WithTimeout(cfg.LLMTimeout)
		preset.Supervise = true
	}

	// The calibration-master library (Postgres-indexed) serves the deepsky path AND planetary frame
	// calibration, so it is resolved before the per-mode branches. -no-db (or an unreachable DB) simply
	// disables persistence: planetary falls back to in-run scratch masters, deepsky to no reuse.
	var library calib.MasterStore
	var catalog pipeline.Catalog
	var rawCalib calib.RawCalibProvider
	var deep calib.DeepOptions
	var reuse pipeline.ReuseConfig
	if !*noDB {
		if st, err := store.New(ctx, cfg.DatabaseURL); err != nil {
			fmt.Fprintf(os.Stderr, "note: calibration library disabled (%v)\n", err)
		} else {
			defer st.Close()
			library = st
			catalog = st // record the run so its frames become reusable
			if cfg.ReuseEnabled {
				rawCalib = st // pool raw bias/darks across sessions into deep masters
				deep = calib.DeepOptions{TempTolC: cfg.ReuseTempTolC, DarkSinceMs: cfg.DarkSinceMs()}
				reuse = pipeline.ReuseConfig{Provider: st, ConeDeg: cfg.ReuseConeDeg} // fold in all matching prior lights
			}
		}
	}

	if m == mode.Planetary {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("planetary input not found: %s", path)
		} // a video file, a SER, or a folder of frames are all accepted by planetary.Process
		res, err := pipeline.ProcessPlanetary(ctx, pipeline.Options{
			InputDir: path, OutputDir: outDir, WorkDir: workDir, Runner: runner, FfmpegBin: cfg.FfmpegBin,
			Preset: &preset, Supervisor: superRunner, Library: library, LibraryDir: cfg.LibraryDir,
			OnProgress: pipelineProgress(*verbose),
		})
		if err != nil {
			return err
		}
		fmt.Printf("\nPlanetary stack: %s\nFrames: %d total, %d stacked\n", res.Source, res.FrameCount, res.StackedFrames)
		for _, o := range res.Outputs {
			fmt.Printf("  → %s\n", o)
		}
		maybeRenderVideo(ctx, cfg, format, res.Outputs)
		return nil
	}
	if m == mode.Comet {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			return fmt.Errorf("comet mode expects a directory of FITS frames: %s", path)
		}
		grd := preset.Grade
		res, err := pipeline.ProcessComet(ctx, pipeline.Options{
			InputDir: path, OutputDir: outDir, WorkDir: workDir, Runner: runner,
			Grade: &grd, Preset: &preset, Gimp: gimp.New(cfg.GimpBin, cfg.GimpHost, cfg.GimpPort),
			Graxpert: graxRunner, Starnet: starRunner, Solve: solve, Spcc: spcc, Supervisor: superRunner,
			CatalogDir: cfg.SirilCatalogDir, OnProgress: pipelineProgress(*verbose),
		})
		if err != nil {
			return err
		}
		fmt.Print(report.RunText(res))
		if res.Final != nil {
			maybeRenderVideo(ctx, cfg, format, res.Final.Outputs)
		}
		return nil
	}
	if m == mode.Mosaic {
		// Tiled-panel mosaic: panels under path (p01/… folders, or clustered by pointing headers)
		// stack individually then assemble onto one canvas. Plan-referenced runs go through the API
		// (POST /api/jobs with mosaic_plan_id) — the CLI always auto-detects.
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			return fmt.Errorf("mosaic mode expects a directory of panel FITS frames: %s", path)
		}
		grd := preset.Grade
		res, err := pipeline.ProcessMosaic(ctx, pipeline.Options{
			InputDir: path, OutputDir: outDir, WorkDir: workDir, Runner: runner,
			Grade: &grd, Preset: &preset, Gimp: gimp.New(cfg.GimpBin, cfg.GimpHost, cfg.GimpPort),
			Graxpert: graxRunner, Starnet: starRunner, Supervisor: superRunner,
			Library: library, LibraryDir: cfg.LibraryDir, RawCalib: rawCalib, Deep: deep,
			Solve: solve, Spcc: spcc, TargetHint: *target, CatalogDir: cfg.SirilCatalogDir,
			OnProgress: pipelineProgress(*verbose),
		})
		if err != nil {
			return err
		}
		fmt.Print(report.RunText(res))
		if res.Final != nil {
			maybeRenderVideo(ctx, cfg, format, res.Final.Outputs)
		}
		return nil
	}

	// Milky-Way nightscapes take the dedicated one-shot-color recipe (develop → sky stack → foreground
	// composite), which is a genuinely different pipeline rather than a filter variant.
	//
	// Every OTHER mode now stacks colour through its own pipeline, which detects one-shot color from
	// the inventory and runs it as a single RGB channel. This used to divert a colour deepsky/nebula
	// folder here via inspect.IsOSCDir, which meant the CLI and the web UI ran different code for the
	// same input — and the CLI's diversion skipped the calibration library, plate-solving, SPCC and the
	// layered finish that the deep-sky path applies.
	if m == mode.Milkyway {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			return fmt.Errorf("%s expects a directory of color images (OSC FITS, or iPhone DNG/HEIC/jpg/png/tif): %s", m, path)
		}
		grd := preset.Grade
		res, err := pipeline.ProcessOSC(ctx, pipeline.Options{
			InputDir:   path,
			OutputDir:  outDir,
			WorkDir:    workDir,
			Runner:     runner,
			Grade:      &grd,
			Preset:     &preset,
			Gimp:       gimp.New(cfg.GimpBin, cfg.GimpHost, cfg.GimpPort),
			Graxpert:   graxRunner,
			Starnet:    starRunner,
			Supervisor: superRunner,
			Solve:      solve,
			Spcc:       spcc,
			TargetHint: *target,
			DarkDir:    *darks,
			FlatDir:    *flats,
			BiasDir:    *bias,
			CatalogDir: cfg.SirilCatalogDir,
			OnProgress: pipelineProgress(*verbose),
		})
		if err != nil {
			return err
		}
		fmt.Print(report.RunText(res))
		if res.Final != nil {
			maybeRenderVideo(ctx, cfg, format, res.Final.Outputs)
		}
		return nil
	}

	// deepsky / nebula: a directory of mono FITS frames.
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return fmt.Errorf("%s mode expects a directory of FITS frames: %s", m, path)
	}
	grd := preset.Grade
	res, err := pipeline.Process(ctx, pipeline.Options{
		InputDir:   path,
		OutputDir:  outDir,
		WorkDir:    workDir,
		Runner:     runner,
		Grade:      &grd,
		Preset:     &preset,
		Gimp:       gimp.New(cfg.GimpBin, cfg.GimpHost, cfg.GimpPort),
		Graxpert:   graxRunner,
		Starnet:    starRunner,
		Supervisor: superRunner,
		Library:    library,
		LibraryDir: cfg.LibraryDir,
		Catalog:    catalog,
		RawCalib:   rawCalib,
		Deep:       deep,
		Reuse:      reuse,
		Solve:      solve,
		Spcc:       spcc,
		TargetHint: *target,
		CatalogDir: cfg.SirilCatalogDir,
		OnProgress: pipelineProgress(*verbose),
	})
	if err != nil {
		return err
	}
	if *asJSON {
		b, err := report.RunJSON(res)
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	fmt.Print(report.RunText(res))
	if res.Final != nil {
		maybeRenderVideo(ctx, cfg, format, res.Final.Outputs)
	}
	return nil
}

// maybeRenderVideo renders a Ken-Burns MP4 from the final PNG when the format requests video.
func maybeRenderVideo(ctx context.Context, cfg *config.Config, format mode.Format, outputs []string) {
	if !format.WantsVideo() {
		return
	}
	var png string
	for _, o := range outputs {
		if strings.HasSuffix(o, ".png") {
			png = o
			break
		}
	}
	if png == "" {
		return
	}
	mp4 := strings.TrimSuffix(png, ".png") + ".mp4"
	if err := videoout.Render(ctx, cfg.FfmpegBin, png, mp4, videoout.DefaultOptions()); err != nil {
		fmt.Fprintf(os.Stderr, "note: video render failed: %v\n", err)
		return
	}
	fmt.Printf("  → %s\n", mp4)
}

func pipelineProgress(verbose bool) func(pipeline.Progress) {
	lastStep := ""
	return func(p pipeline.Progress) {
		if p.Line != "" {
			if verbose {
				fmt.Fprintf(os.Stderr, "    %s\n", p.Line)
			}
			return
		}
		if p.Step != lastStep {
			lastStep = p.Step
			fmt.Fprintf(os.Stderr, "[%d/%d] %s\n", p.Index, p.Total, p.Step)
		}
	}
}

func pick(v, def string) string {
	if v != "" {
		return v
	}
	return def
}
