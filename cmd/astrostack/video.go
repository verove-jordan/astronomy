package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/planetary"
	"github.com/verove-jordan/astronomy/internal/siril"
)

func runVideo(args []string) error {
	fs := flag.NewFlagSet("video", flag.ContinueOnError)
	out := fs.String("out", "", "output directory (default $ASTRO_OUTPUT_DIR)")
	work := fs.String("work", "", "scratch directory (default $ASTRO_WORK_DIR)")
	best := fs.Int("best", 50, "keep this percent of the sharpest frames")
	earthshine := fs.Float64("earthshine", 0, "reveal the Moon's unlit side (earthshine); 0 = off, 1 = natural, up to 2")
	drizzle := fs.Float64("drizzle", 0, "super-resolution output grid (1, 1.5 or 2 — snapped); 0 = native")
	alignPoints := fs.Int("align-points", 0, "total stacking reference points for the distortion grid (100..2304, snapped to N×N); 0 = auto")
	verbose := fs.Bool("v", false, "stream Siril/ffmpeg log lines")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: astrostack video [--out dir] [--best N] [-v] <file>")
	}
	file := fs.Arg(0)
	if info, err := os.Stat(file); err != nil || info.IsDir() {
		return fmt.Errorf("not a file: %s", file)
	}

	cfg := config.Load()
	lastStep := ""
	onProgress := func(p siril.Progress) {
		if p.Line == "" {
			return
		}
		if *verbose {
			fmt.Fprintf(os.Stderr, "    %s\n", p.Line)
		} else if p.Line != lastStep {
			lastStep = p.Line
			fmt.Fprintf(os.Stderr, "==> %s\n", p.Line)
		}
	}

	opts := planetary.Options{BestPercent: *best, Sharpen: true, Formats: []string{"png", "tif"}}
	if *drizzle > 0 {
		opts.DrizzleScale = planetary.SnapDrizzle(*drizzle)
	}
	if *alignPoints > 0 {
		opts.AlignPoints = planetary.SnapAlignPoints(*alignPoints)
	}
	if *earthshine > 0 {
		// The earthshine composite rides the finish knobs, so enabling it here also switches this
		// bare-bones command from the legacy zero finish to the standard mineral-Moon finish.
		opts.Finish = planetary.DefaultFinish()
		opts.Finish.EarthshineGain = *earthshine
	}
	res, err := planetary.Process(context.Background(), siril.New(cfg.SirilBin, sirilLimits(cfg)), cfg.FfmpegBin,
		file, pick(*work, cfg.WorkDir), pick(*out, cfg.OutputDir),
		opts, nil, onProgress) // nil RunExtras: single root, no calib, no bar
	if err != nil {
		return err
	}

	fmt.Printf("\nPlanetary stack: %s\n", res.Source)
	fmt.Printf("Frames: %d total, %d stacked (best %d%%)\n", res.FrameCount, res.StackedFrames, *best)
	for _, o := range res.Outputs {
		fmt.Printf("  → %s\n", o)
	}
	for _, n := range res.Notes {
		fmt.Printf("  · %s\n", n)
	}
	return nil
}
