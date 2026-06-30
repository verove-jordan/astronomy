package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/gimp"
	"github.com/verove-jordan/astronomy/internal/graxpert"
	"github.com/verove-jordan/astronomy/internal/llm"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/pipeline"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/starnet"
)

// runRefine re-runs ONLY the finish — driving the local-AI-agent supervisor — on an already-stacked
// run directory (output/<object>/<runID>), reusing the channel masters on disk. No re-stacking. It
// updates final.* in place and prints the supervisor's iteration trail.
func runRefine(args []string) error {
	fs := flag.NewFlagSet("refine", flag.ContinueOnError)
	modeName := fs.String("mode", "deepsky", "finish preset: deepsky|nebula")
	noAI := fs.Bool("no-ai", false, "skip GraXpert/StarNet (keep the supervisor + Siril/GIMP finish)")
	verbose := fs.Bool("v", false, "stream progress log lines")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: astrostack refine [flags] <run-dir>\n" +
			"  <run-dir> is an output/<object>/<runID> folder with run.json + master_*/aligned_*.fits")
	}
	runDir := fs.Arg(0)

	m, err := mode.ParseMode(*modeName)
	if err != nil {
		return err
	}
	preset := mode.For(m)
	preset.Supervise = true // refine drives the agent (soft-falls to the standard finish if the server is down)

	cfg := config.Load()
	ctx := context.Background()
	var graxRunner *graxpert.Runner
	var starRunner *starnet.Runner
	if !*noAI {
		graxRunner = graxpert.New(cfg.GraxpertBin)
		starRunner = starnet.New(cfg.StarnetBin)
	}

	final, err := pipeline.RefineExistingRun(ctx, pipeline.Options{
		WorkDir:    cfg.WorkDir,
		Runner:     siril.New(cfg.SirilBin, sirilLimits(cfg)),
		Gimp:       gimp.New(cfg.GimpBin, cfg.GimpHost, cfg.GimpPort),
		Graxpert:   graxRunner,
		Starnet:    starRunner,
		Supervisor: llm.New(cfg.LLMBaseURL, cfg.LLMModel, cfg.LLMImageFormat),
		Preset:     &preset,
		Solve:      siril.SolveOptions{FocalMM: cfg.FocalLenMM, PixelUm: cfg.PixelSizeUm, Catalog: cfg.PlateSolveCatalog},
		Spcc: siril.SpccOptions{
			MonoSensor: cfg.SpccMonoSensor, OSCSensor: cfg.NightscapeOSCSensor,
			RFilter: cfg.SpccRFilter, GFilter: cfg.SpccGFilter,
			BFilter: cfg.SpccBFilter, WhiteRef: cfg.SpccWhiteRef,
		},
		CatalogDir: cfg.SirilCatalogDir,
		OnProgress: pipelineProgress(*verbose),
	}, runDir)
	if err != nil {
		return err
	}

	fmt.Printf("\nRefined finish: %s — %s\n", final.Mode, runDir)
	for _, o := range final.Outputs {
		fmt.Printf("  → %s\n", o)
	}
	if n := len(final.Iterations); n > 0 {
		fmt.Printf("\nSupervisor iterations: %d\n", n)
		for _, it := range final.Iterations {
			mark := " "
			if it.Chosen {
				mark = "★"
			}
			fmt.Printf("  %s iter %d  score %.1f (metrics %.1f, model %.1f)  %s\n",
				mark, it.Index+1, it.CombinedScore, it.DetScore, it.ModelScore, it.Reasoning)
		}
	}
	return nil
}
