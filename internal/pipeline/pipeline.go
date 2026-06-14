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
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/grade"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// trailDownsample is the working size (larger axis) for the trail detector.
const trailDownsample = 512

// Options configures a pipeline run.
type Options struct {
	InputDir   string
	OutputDir  string
	WorkDir    string
	Runner      *siril.Runner
	Grade       *grade.Options       // nil → grade.DefaultOptions()
	Postprocess *postprocess.Options // nil → postprocess.DefaultOptions()
	OnProgress  func(Progress)
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
	Object        string          `json:"object"`
	Filter        string          `json:"filter"`
	ExposureMs    int64           `json:"exposure_ms"`
	InputFrames   int             `json:"input_frames"`
	StackedFrames int             `json:"stacked_frames"`
	OutputPath    string          `json:"output_path,omitempty"`
	Selection     calib.Selection `json:"selection"`
	Metrics       []grade.Metric  `json:"metrics,omitempty"`
	Err           string          `json:"error,omitempty"`
}

// Result summarizes a completed run.
type Result struct {
	InputDir  string               `json:"input_dir"`
	OutputDir string               `json:"output_dir"`
	Inventory *inspect.Inventory   `json:"-"`
	Masters   []calib.Master       `json:"masters"`
	Channels  []ChannelResult      `json:"channels"`
	Final     *postprocess.Result  `json:"final,omitempty"`
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
	total := len(lights) + 2 // masters + per-channel + final combine
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

	gradeOpts := grade.DefaultOptions()
	if opts.Grade != nil {
		gradeOpts = *opts.Grade
	}

	for _, set := range lights {
		ch := processChannel(ctx, opts, set, masters, workRun, outDir, gradeOpts,
			progress(fmt.Sprintf("grading + stacking %s %s", set.Key.Object, set.Key.Filter)))
		res.Channels = append(res.Channels, ch)
		if ch.Err != "" {
			res.Warnings = append(res.Warnings, fmt.Sprintf("channel %s: %s", set.Key.Filter, ch.Err))
		}
	}

	combine(ctx, opts, res, outDir, progress("combining channels into final image"))
	return res, nil
}

// combine assembles the successful per-channel masters into the final image.
func combine(ctx context.Context, opts Options, res *Result, outDir string, onProgress func(siril.Progress)) {
	channels := map[string]string{}
	for _, ch := range res.Channels {
		if ch.Err == "" && ch.OutputPath != "" && ch.Filter != "" {
			channels[ch.Filter] = "master_" + filterTag(ch.Filter)
		}
	}
	if len(channels) == 0 {
		res.Warnings = append(res.Warnings, "no channels available to combine")
		return
	}
	ppOpts := postprocess.DefaultOptions()
	if opts.Postprocess != nil {
		ppOpts = *opts.Postprocess
	}
	final, err := postprocess.Combine(ctx, opts.Runner, outDir, channels, "final", ppOpts, onProgress)
	if err != nil {
		res.Warnings = append(res.Warnings, "channel combination failed: "+err.Error())
		return
	}
	res.Final = final
}

func processChannel(ctx context.Context, opts Options, set inspect.Set, masters []calib.Master,
	workRun, outDir string, gradeOpts grade.Options, onProgress func(siril.Progress)) ChannelResult {
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
	dark, flat, bias := sel.Masters()
	cm := siril.CalibMasters{Dark: dark, Flat: flat, Bias: bias}

	// 1. Calibrate + register (writes per-frame metrics to the calibrated sequence's .seq).
	if _, err := opts.Runner.Run(ctx, seqDir, siril.CalibrateRegisterScript("light", cm), onProgress); err != nil {
		ch.Err = err.Error()
		return ch
	}
	baseSeq := siril.CalibratedSeq("light", cm) // stable, 1:1 with input frames
	regSeq := siril.RegisteredSeq("light", cm)

	// 2. Grade in the calibrated index space, then map rejects to registered indices.
	metrics, rejectedReg, regCount, err := gradeChannel(seqDir, baseSeq, set.Frames, gradeOpts)
	if err != nil {
		ch.Err = fmt.Sprintf("grading: %v", err)
		return ch
	}
	ch.Metrics = metrics
	ch.StackedFrames = regCount - len(rejectedReg)
	if regCount == 0 {
		ch.Err = "no frames could be registered"
		return ch
	}

	// 3. Stack only the survivors.
	outBase := filepath.Join(outDir, "master_"+filterTag(set.Key.Filter))
	if _, err := opts.Runner.Run(ctx, seqDir, siril.StackSelectedScript(regSeq, regCount, rejectedReg, outBase), onProgress); err != nil {
		ch.Err = err.Error()
		return ch
	}
	ch.OutputPath = outBase + ".fits"
	return ch
}

// gradeChannel builds per-frame metrics from the calibrated sequence's .seq (1:1 with input
// frames) and the calibrated pixels (trail detection), applies the rejection rules, and maps the
// rejected frames to 1-based indices in the registered sequence used for stacking. Frames Siril
// could not register (zero metrics) are rejected up front and excluded from the registered space.
func gradeChannel(seqDir, baseSeq string, frames []*inspect.Frame, opts grade.Options) (
	metrics []grade.Metric, rejectedReg []int, regCount int, err error) {
	seq, err := grade.ParseSeq(filepath.Join(seqDir, baseSeq+"_.seq"))
	if err != nil {
		return nil, nil, 0, err
	}
	metrics = make([]grade.Metric, len(seq.Metrics))
	for i, sm := range seq.Metrics {
		m := grade.Metric{
			Index: i + 1, FWHM: sm.FWHM, WFWHM: sm.WFWHM, Roundness: sm.Roundness,
			Quality: sm.Quality, Background: sm.Background, StarCount: sm.StarCount,
		}
		if i < len(frames) {
			m.Path = frames[i].Path
		}
		if sm.FWHM <= 0 { // Siril could not register this frame
			m.Rejected = true
			m.RejectReason = "could not register (too few/elongated stars)"
		} else if f, ferr := fits.Open(filepath.Join(seqDir, fmt.Sprintf("%s_%05d.fits", baseSeq, i+1))); ferr == nil {
			if grid, w, h, derr := f.ReadDownsampled(trailDownsample, fits.Max); derr == nil {
				m.TrailDetected, m.TrailScore = grade.DetectTrail(grid, w, h)
			}
		}
		metrics[i] = m
	}
	grade.Grade(metrics, opts)

	for i := range metrics {
		if metrics[i].FWHM > 0 { // present in the registered sequence
			regCount++
			if metrics[i].Rejected {
				rejectedReg = append(rejectedReg, regCount)
			}
		}
	}
	return metrics, rejectedReg, regCount, nil
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
