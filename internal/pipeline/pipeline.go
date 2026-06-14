// Package pipeline orchestrates the end-to-end deep-sky workflow: inspect a directory, build
// master calibration frames, then for each light channel match the right masters and run
// calibrate → register → stack via Siril. Channel combination lives in package postprocess.
package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// Options configures a pipeline run.
type Options struct {
	InputDir   string
	OutputDir  string
	WorkDir    string
	Runner     *siril.Runner
	OnProgress func(Progress)
}

// Progress is a pipeline-level progress event (and forwarded Siril log lines).
type Progress struct {
	Step  string `json:"step"`
	Index int    `json:"index"`
	Total int    `json:"total"`
	Line  string `json:"line,omitempty"`
}

// ChannelResult is the stacked output for one light channel (filter).
type ChannelResult struct {
	Object        string           `json:"object"`
	Filter        string           `json:"filter"`
	ExposureMs    int64            `json:"exposure_ms"`
	InputFrames   int              `json:"input_frames"`
	StackedFrames int              `json:"stacked_frames"`
	OutputPath    string           `json:"output_path,omitempty"`
	Selection     calib.Selection  `json:"selection"`
	Err           string           `json:"error,omitempty"`
}

// Result summarizes a completed run.
type Result struct {
	InputDir  string               `json:"input_dir"`
	OutputDir string               `json:"output_dir"`
	Inventory *inspect.Inventory   `json:"-"`
	Masters   []calib.Master       `json:"masters"`
	Channels  []ChannelResult      `json:"channels"`
	Warnings  []string             `json:"warnings"`
}

// Process runs the full pipeline and returns its result. Per-channel failures are recorded as
// warnings/channel errors rather than aborting the whole run.
func Process(ctx context.Context, opts Options) (*Result, error) {
	if err := opts.Runner.Available(ctx); err != nil {
		return nil, fmt.Errorf("siril unavailable: %w", err)
	}
	inv, err := inspect.Scan(ctx, opts.InputDir)
	if err != nil {
		return nil, err
	}

	// Absolute paths: Siril runs with its CWD set per-sequence, so every -out path must be absolute.
	workAbs, err := filepath.Abs(opts.WorkDir)
	if err != nil {
		return nil, err
	}
	outAbs, err := filepath.Abs(opts.OutputDir)
	if err != nil {
		return nil, err
	}
	runID := time.Now().Format("20060102_150405")
	workRun := filepath.Join(workAbs, "run_"+runID)
	mastersDir := filepath.Join(workRun, "masters")
	object := sanitize(dominantObject(inv))
	outDir := filepath.Join(outAbs, object, runID)
	if err := fsutil.EnsureDir(outDir); err != nil {
		return nil, err
	}

	res := &Result{InputDir: opts.InputDir, OutputDir: outDir, Inventory: inv}
	res.Warnings = append(res.Warnings, inv.Warnings...)

	lights := inv.SetsOfType(inspect.Light)
	total := len(lights) + 1 // +1 for the masters step
	step := 0
	progress := func(stepName string) func(siril.Progress) {
		step++
		idx := step
		opts.report(Progress{Step: stepName, Index: idx, Total: total})
		return func(p siril.Progress) {
			opts.report(Progress{Step: stepName, Index: idx, Total: total, Line: p.Line})
		}
	}

	masters, mWarn, err := calib.BuildMasters(ctx, opts.Runner, inv, mastersDir, workRun, progress("building master calibration frames"))
	if err != nil {
		return nil, err
	}
	res.Masters = masters
	res.Warnings = append(res.Warnings, mWarn...)

	for _, set := range lights {
		ch := processChannel(ctx, opts, set, masters, workRun, outDir,
			progress(fmt.Sprintf("stacking %s %s", set.Key.Object, set.Key.Filter)))
		res.Channels = append(res.Channels, ch)
		if ch.Err != "" {
			res.Warnings = append(res.Warnings, fmt.Sprintf("channel %s: %s", set.Key.Filter, ch.Err))
		}
	}
	return res, nil
}

func processChannel(ctx context.Context, opts Options, set inspect.Set, masters []calib.Master,
	workRun, outDir string, onProgress func(siril.Progress)) ChannelResult {
	sel := calib.MatchForLight(set.Key, masters)
	ch := ChannelResult{
		Object:      set.Key.Object,
		Filter:      set.Key.Filter,
		ExposureMs:  set.Key.ExposureMs,
		InputFrames: set.Count,
		Selection:   sel,
	}

	seqDir := filepath.Join(workRun, "light_"+sanitize(set.Key.Filter))
	if _, err := fsutil.LinkFrames(seqDir, framePaths(set.Frames)); err != nil {
		ch.Err = err.Error()
		return ch
	}

	outBase := filepath.Join(outDir, "master_"+filterTag(set.Key.Filter))
	dark, flat, bias := sel.Masters()
	script := siril.LightStackScript("light", siril.CalibMasters{Dark: dark, Flat: flat, Bias: bias}, outBase)
	if _, err := opts.Runner.Run(ctx, seqDir, script, onProgress); err != nil {
		ch.Err = err.Error()
		return ch
	}
	ch.OutputPath = outBase + ".fits"
	ch.StackedFrames = set.Count // grading (M3) will reduce this
	return ch
}

func (o Options) report(p Progress) {
	if o.OnProgress != nil {
		o.OnProgress(p)
	}
}

func framePaths(frames []*inspect.Frame) []string {
	out := make([]string, len(frames))
	for i, f := range frames {
		out[i] = f.Path
	}
	return out
}

func dominantObject(inv *inspect.Inventory) string {
	counts := map[string]int{}
	for _, f := range inv.Frames {
		if f.Type == inspect.Light && f.Object != "" {
			counts[f.Object]++
		}
	}
	best, n := "session", 0
	for obj, c := range counts {
		if c > n {
			best, n = obj, c
		}
	}
	return best
}

func filterTag(filter string) string {
	if filter == "" {
		return "mono"
	}
	return sanitize(filter)
}

func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		case r == ' ':
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "session"
	}
	return string(out)
}
