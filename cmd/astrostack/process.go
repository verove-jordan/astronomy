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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 3 {
		return fmt.Errorf("usage: astrostack process [flags] <mode> <format> <path>\n" +
			"  modes: deepsky nebula milkyway planetary    formats: image video both")
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
	cfg := config.Load()
	outDir := pick(*out, cfg.OutputDir)
	workDir := pick(*work, cfg.WorkDir)
	ctx := context.Background()
	runner := siril.New(cfg.SirilBin)
	solve := siril.SolveOptions{FocalMM: cfg.FocalLenMM, PixelUm: cfg.PixelSizeUm, Catalog: cfg.PlateSolveCatalog}
	spcc := siril.SpccOptions{
		MonoSensor: cfg.SpccMonoSensor, RFilter: cfg.SpccRFilter, GFilter: cfg.SpccGFilter,
		BFilter: cfg.SpccBFilter, WhiteRef: cfg.SpccWhiteRef,
	}
	// Optional astro-AI host tools (skipped with -no-ai or when the binary is absent).
	var graxRunner *graxpert.Runner
	var starRunner *starnet.Runner
	if !*noAI {
		graxRunner = graxpert.New(cfg.GraxpertBin)
		starRunner = starnet.New(cfg.StarnetBin)
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
	if m == mode.Milkyway {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			return fmt.Errorf("milkyway expects a directory of color images (iPhone DNG/HEIC, jpg/png/tif): %s", path)
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
