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

	res, err := planetary.Process(context.Background(), siril.New(cfg.SirilBin), cfg.FfmpegBin,
		file, pick(*work, cfg.WorkDir), pick(*out, cfg.OutputDir),
		planetary.Options{BestPercent: *best, Sharpen: true, Formats: []string{"png", "tif"}}, onProgress)
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
