package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/pipeline"
	"github.com/verove-jordan/astronomy/internal/report"
	"github.com/verove-jordan/astronomy/internal/siril"
)

func runProcess(args []string) error {
	fs := flag.NewFlagSet("process", flag.ContinueOnError)
	out := fs.String("out", "", "output directory (default $ASTRO_OUTPUT_DIR)")
	work := fs.String("work", "", "scratch directory (default $ASTRO_WORK_DIR)")
	asJSON := fs.Bool("json", false, "emit the run result as JSON")
	verbose := fs.Bool("v", false, "stream Siril log lines")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: astrostack process [--out dir] [--work dir] [-v] <dir>")
	}
	dir := fs.Arg(0)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("not a directory: %s", dir)
	}

	cfg := config.Load()
	outDir := pick(*out, cfg.OutputDir)
	workDir := pick(*work, cfg.WorkDir)

	lastStep := ""
	onProgress := func(p pipeline.Progress) {
		if p.Line != "" {
			if *verbose {
				fmt.Fprintf(os.Stderr, "    %s\n", p.Line)
			}
			return
		}
		if p.Step != lastStep {
			lastStep = p.Step
			fmt.Fprintf(os.Stderr, "[%d/%d] %s\n", p.Index, p.Total, p.Step)
		}
	}

	res, err := pipeline.Process(context.Background(), pipeline.Options{
		InputDir:   dir,
		OutputDir:  outDir,
		WorkDir:    workDir,
		Runner:     siril.New(cfg.SirilBin),
		OnProgress: onProgress,
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
	return nil
}

func pick(v, def string) string {
	if v != "" {
		return v
	}
	return def
}
