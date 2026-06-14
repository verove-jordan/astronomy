package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/pipeline"
	"github.com/verove-jordan/astronomy/internal/report"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/store"
)

func runProcess(args []string) error {
	fs := flag.NewFlagSet("process", flag.ContinueOnError)
	out := fs.String("out", "", "output directory (default $ASTRO_OUTPUT_DIR)")
	work := fs.String("work", "", "scratch directory (default $ASTRO_WORK_DIR)")
	asJSON := fs.Bool("json", false, "emit the run result as JSON")
	verbose := fs.Bool("v", false, "stream Siril log lines")
	noDB := fs.Bool("no-db", false, "disable the calibration library (no database)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: astrostack process [--out dir] [--work dir] [--no-db] [-v] <dir>")
	}
	dir := fs.Arg(0)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("not a directory: %s", dir)
	}

	cfg := config.Load()
	outDir := pick(*out, cfg.OutputDir)
	workDir := pick(*work, cfg.WorkDir)

	ctx := context.Background()
	var library calib.MasterStore
	if !*noDB {
		if st, err := store.New(ctx, cfg.DatabaseURL); err != nil {
			fmt.Fprintf(os.Stderr, "note: calibration library disabled (%v)\n", err)
		} else {
			defer st.Close()
			library = st
		}
	}

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

	res, err := pipeline.Process(ctx, pipeline.Options{
		InputDir:   dir,
		OutputDir:  outDir,
		WorkDir:    workDir,
		Runner:     siril.New(cfg.SirilBin),
		Library:    library,
		LibraryDir: cfg.LibraryDir,
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
