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
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/llm"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/pipeline"
	"github.com/verove-jordan/astronomy/internal/planetary"
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 3 {
		return fmt.Errorf("usage: astrostack process [flags] <mode> <format> <path>\n" +
			"  modes: deepsky nebula milkyway planetary comet    formats: image video both")
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
	cfg := config.Load()
	outDir := pick(*out, cfg.OutputDir)
	workDir := pick(*work, cfg.WorkDir)
	ctx := context.Background()
	runner := siril.New(cfg.SirilBin, sirilLimits(cfg))
	solve := siril.SolveOptions{FocalMM: cfg.FocalLenMM, PixelUm: cfg.PixelSizeUm, Catalog: cfg.PlateSolveCatalog}
	spcc := siril.SpccOptions{
		MonoSensor: cfg.SpccMonoSensor, OSCSensor: cfg.NightscapeOSCSensor,
		RFilter: cfg.SpccRFilter, GFilter: cfg.SpccGFilter,
		BFilter: cfg.SpccBFilter, WhiteRef: cfg.SpccWhiteRef,
	}
	// Optional astro-AI host tools (skipped with -no-ai or when the binary is absent).
	var graxRunner *graxpert.Runner
	var starRunner *starnet.Runner
	if !*noAI {
		graxRunner = graxpert.New(cfg.GraxpertBin)
		starRunner = starnet.New(cfg.StarnetBin)
	}
	// Optional local-AI-agent finish supervisor (opt-in via -supervise; nil → standard finish).
	var superRunner *llm.Runner
	if *supervise {
		superRunner = llm.New(cfg.LLMBaseURL, cfg.LLMModel, cfg.LLMImageFormat)
		preset.Supervise = true
	}

	if m == mode.Planetary {
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			return fmt.Errorf("planetary expects a video file: %s", path)
		}
		res, err := planetary.Process(ctx, runner, cfg.FfmpegBin, path, workDir, outDir, preset.Planetary, videoProgress(*verbose))
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
			Graxpert: graxRunner, Starnet: starRunner, Solve: solve, Spcc: spcc,
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

	// One-shot-color: milkyway mode, or a deepsky/nebula directory that turns out to be Bayer CFA FITS
	// (an older OSC capture). Both debayer through the OSC pipeline, finished with the chosen mode's preset.
	useOSC := m == mode.Milkyway
	if !useOSC && (m == mode.Deepsky || m == mode.Nebula) {
		if info, err := os.Stat(path); err == nil && info.IsDir() && inspect.IsOSCDir(path) {
			useOSC = true
		}
	}
	if useOSC {
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
			Solve:      solve,
			Spcc:       spcc,
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

func videoProgress(verbose bool) func(siril.Progress) {
	lastStep := ""
	return func(p siril.Progress) {
		if p.Line == "" {
			return
		}
		if verbose {
			fmt.Fprintf(os.Stderr, "    %s\n", p.Line)
		} else if p.Line != lastStep {
			lastStep = p.Line
			fmt.Fprintf(os.Stderr, "==> %s\n", p.Line)
		}
	}
}

func pick(v, def string) string {
	if v != "" {
		return v
	}
	return def
}
