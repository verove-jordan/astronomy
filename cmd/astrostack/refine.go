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
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/starnet"
)

// runRefine re-runs ONLY the finish — driving the local-AI-agent supervisor — on an already-stacked
// run directory (output/<object>/<runID>), reusing the channel masters on disk. No re-stacking. It
// updates final.* in place and prints the supervisor's iteration trail.
func runRefine(args []string) error {
	fs := flag.NewFlagSet("refine", flag.ContinueOnError)
	modeName := fs.String("mode", "deepsky", "finish preset: deepsky|nebula|planetary|milkyway|comet")
	paramsJSON := fs.String("params", "", `JSON knob overrides applied to the preset, same keys as the API (e.g. '{"earthshine_gain":1}')`)
	noAI := fs.Bool("no-ai", false, "skip GraXpert/StarNet (keep the supervisor + Siril/GIMP finish)")
	noSupervise := fs.Bool("no-supervise", false, "run the deterministic finish only (no VLM agent) — re-finish a stored run with the current preset/params")
	tierCeiling := fs.String("tier", "B", "how far the agent may reach: A=composite, B=+finish prep, C=+re-stack")
	iters := fs.Int("iters", 0, "max supervisor iterations (0 → engine default of 4, hard max 8)")
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
	preset.Supervise = !*noSupervise // refine drives the agent (soft-falls to the standard finish if the server is down); --no-supervise forces the plain deterministic finish
	preset.SuperviseTier = *tierCeiling
	preset.SuperviseMaxIters = *iters
	if *paramsJSON != "" {
		res, err := pipeline.ApplyParamPatch(&preset, []byte(*paramsJSON))
		if err != nil {
			return err
		}
		if len(res.Ignored) > 0 {
			fmt.Printf("params: ignored unknown keys %v\n", res.Ignored)
		}
	}

	cfg := config.Load()
	ctx := context.Background()
	var graxRunner *graxpert.Runner
	var starRunner *starnet.Runner
	if !*noAI {
		graxRunner = graxpert.New(cfg.GraxpertBin, cfg.GraxpertURL).SetDefaults(cfg.GraxpertGPU, cfg.GraxpertBatch)
		starRunner = starnet.New(cfg.StarnetBin)
	}
	refineSolve, refineSpcc := postprocess.SolveSpccFromConfig(cfg)

	final, err := pipeline.RefineExistingRun(ctx, pipeline.Options{
		WorkDir:    cfg.WorkDir,
		Runner:     siril.New(cfg.SirilBin, sirilLimits(cfg)),
		Gimp:       gimp.New(cfg.GimpBin, cfg.GimpHost, cfg.GimpPort),
		Graxpert:   graxRunner,
		Starnet:    starRunner,
		Supervisor: llm.New(cfg.LLMBaseURL, cfg.LLMModel, cfg.LLMImageFormat).WithTimeout(cfg.LLMTimeout),
		Preset:     &preset,
		Solve:      refineSolve,
		Spcc:       refineSpcc,
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
			fmt.Printf("  %s iter %d [%s]  score %.1f (metrics %.1f, model %.1f)  %s\n",
				mark, it.Index+1, it.Tier, it.CombinedScore, it.DetScore, it.ModelScore, it.Reasoning)
		}
	}
	return nil
}
