package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/gimp"
	"github.com/verove-jordan/astronomy/internal/grade"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// ProcessOSC runs the one-shot-color pipeline (milkyway / iPhone ProRAW/HEIC): convert+debayer →
// register → grade → stack → background-extract + stretch → GIMP curves. No per-filter channels
// and no calibration library (phone captures rarely have darks/flats).
func ProcessOSC(ctx context.Context, opts Options) (*Result, error) {
	if err := opts.Runner.Available(ctx); err != nil {
		return nil, fmt.Errorf("siril unavailable: %w", err)
	}
	frames, err := inspect.ListRawFrames(opts.InputDir)
	if err != nil {
		return nil, err
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
	workRun := filepath.Join(workAbs, "milkyway", "run_"+runID)
	outDir := filepath.Join(outAbs, "milkyway", runID)
	if err := fsutil.EnsureDir(outDir); err != nil {
		return nil, err
	}
	res := &Result{InputDir: opts.InputDir, OutputDir: outDir}

	seqDir := filepath.Join(workRun, "02_osc")
	if _, err := fsutil.LinkFrames(seqDir, frames); err != nil {
		return nil, err
	}

	opts.report(Progress{Step: "convert + register", Index: 1, Total: 3})
	convReg := "requires 1.2.0\nsetext fits\nconvert osc -out=.\nregister osc\n"
	if _, err := opts.Runner.Run(ctx, seqDir, convReg, nil); err != nil {
		return nil, err
	}

	gradeOpts := grade.DefaultOptions()
	if opts.Grade != nil {
		gradeOpts = *opts.Grade
	}
	pseudo := make([]*inspect.Frame, len(frames))
	for i, p := range frames {
		pseudo[i] = &inspect.Frame{Path: p}
	}
	metrics, rejectedReg, regCount, err := gradeChannel(seqDir, "osc", pseudo, gradeOpts)
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

	opts.report(Progress{Step: "stacking", Index: 2, Total: 3})
	if _, err := opts.Runner.Run(ctx, seqDir, siril.StackSelectedScript("r_osc", regCount, rejectedReg, masterBase), nil); err != nil {
		return nil, err
	}

	opts.report(Progress{Step: "finishing (GIMP)", Index: 3, Total: 3})
	finishOSC(ctx, opts, res, masterBase+".fits", workRun, outDir)
	return res, nil
}

func finishOSC(ctx context.Context, opts Options, res *Result, masterPath, workRun, outDir string) {
	stretchDir := filepath.Join(workRun, "05_stretched")
	if err := fsutil.EnsureDir(stretchDir); err != nil {
		res.Warnings = append(res.Warnings, "stretch dir: "+err.Error())
		return
	}
	deg := 3
	if opts.Preset != nil && opts.Preset.BackgroundDegree > 0 {
		deg = opts.Preset.BackgroundDegree
	}
	base := filepath.Join(stretchDir, "base")
	script := fmt.Sprintf("requires 1.2.0\nsetext fits\nload %s\nsubsky %d\nautostretch\nsavetif %s\n", masterPath, deg, base)
	if _, err := opts.Runner.Run(ctx, stretchDir, script, nil); err != nil {
		res.Warnings = append(res.Warnings, "background extraction/stretch failed: "+err.Error())
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
				res.Warnings = append(res.Warnings, "GIMP finishing failed, keeping Siril stretch: "+gerr.Error())
			}
		}
	}
	res.Final = &postprocess.Result{Mode: "OSC-RGB", Channels: []string{"RGB"}, Outputs: []string{base + ".tif"}, Notes: []string{"Siril stretch (GIMP unavailable)"}}
}
