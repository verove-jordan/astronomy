package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/buildinfo"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/solar"
	"github.com/verove-jordan/astronomy/internal/videoout"
)

// sunmode.go is the `sun` mode entry point.
//
// It differs from every other mode in what it does first: a solar folder is triaged before anything
// is stacked, because a real session is a pile of attempts at different zooms, resolutions and
// exposures, and which of those files may be combined with which is a question only measurement can
// answer. Triage answers it by measuring the disc in every file and grouping on that, then the
// best-scoring group is stacked while the rest stay in the report so nothing disappears silently.

// ProcessSun runs the solar pipeline over a capture folder.
func ProcessSun(ctx context.Context, opts Options) (*Result, error) {
	workAbs, err := filepath.Abs(opts.WorkDir)
	if err != nil {
		return nil, err
	}
	outAbs, err := filepath.Abs(opts.OutputDir)
	if err != nil {
		return nil, err
	}
	preset := solar.DefaultPreset()
	if opts.Preset != nil {
		preset = opts.Preset.Sun
	}
	runID := time.Now().UTC().Format("20060102_150405")
	object := sunObject(opts.InputDir)
	outDir := filepath.Join(outAbs, object, runID)
	workRun := filepath.Join(workAbs, "sun_"+runID)
	if err := fsutil.EnsureDir(outDir); err != nil {
		return nil, err
	}
	if err := fsutil.EnsureDir(workRun); err != nil {
		return nil, err
	}
	res := &Result{
		InputDir: opts.InputDir, OutputDir: outDir, Object: object, RunID: runID,
		Engine: buildinfo.Version, Options: runOptionsFrom(opts.Preset),
	}
	opts.PriorObject = object

	const sunSteps = 6
	opts.report(Progress{Step: "inspecting the capture", Index: 1, Total: sunSteps})
	report, err := solar.Triage(ctx, opts.InputDir, preset.TriageOpts(opts.FfmpegBin))
	if err != nil {
		return nil, fmt.Errorf("sun: %w", err)
	}
	writeTriage(outDir, report, res)
	res.Warnings = append(res.Warnings, report.Warnings...)
	reportTriage(opts, report, sunSteps)

	group, err := pickGroup(report, opts.ExcludeSets, preset.RescaleGroups)
	if err != nil {
		return nil, fmt.Errorf("sun: %w", err)
	}
	opts.report(Progress{Step: "ingesting frames", Index: 2, Total: sunSteps,
		Line: fmt.Sprintf("%s — %d file(s), %d frame(s), disc ⌀%.0f px",
			group.Label, group.Files, group.Frames, 2*group.DiscRadius)})

	frames, warn, err := ingestGroup(ctx, group, preset, workRun, opts.FfmpegBin, func(line string) {
		opts.report(Progress{Step: "ingesting frames", Index: 2, Total: sunSteps, Line: line})
	})
	res.Warnings = append(res.Warnings, warn...)
	if err != nil {
		return nil, fmt.Errorf("sun: %w", err)
	}
	sunPreview(opts, res, outDir, ordSunFrame, "frame", frames[0].Path, frames[0].Limb, preset)
	opts.report(Progress{Step: "normalising exposures", Index: 3, Total: sunSteps,
		Line: fmt.Sprintf("%d frame(s) onto one photometric scale", len(frames))})
	if nWarn, nerr := solar.Normalize(frames); nerr != nil {
		res.Warnings = append(res.Warnings, "sun: photometric normalisation: "+nerr.Error())
	} else {
		res.Warnings = append(res.Warnings, nWarn...)
	}

	windows := solar.Windows(frames, preset.WindowOpts())
	opts.report(Progress{Step: "stacking", Index: 4, Total: sunSteps,
		Line: fmt.Sprintf("%d window(s) over %d frame(s)", len(windows), len(frames))})
	masters, mWarn := stackWindows(ctx, opts, windows, preset, outDir, res, sunSteps)
	res.Warnings = append(res.Warnings, mWarn...)
	if len(masters) == 0 {
		return nil, fmt.Errorf("sun: no window produced a master")
	}

	opts.report(Progress{Step: "finishing", Index: 5, Total: sunSteps})
	hero := solar.Sharpest(windows)
	if hero >= len(masters) {
		hero = 0
	}
	final, ferr := finishSun(opts, masters, hero, preset, outDir, object)
	if ferr != nil {
		return nil, fmt.Errorf("sun: %w", ferr)
	}
	res.Final = final
	copyFinalPreview(opts, outDir, final)

	// The time-lapse is rendered whenever the session split into more than one window, regardless of
	// the requested format: windowing exists because the scene evolves, so the evolution is the
	// result, not an optional extra. The job layer may still add its own Ken-Burns pan on top.
	if len(masters) > 1 {
		opts.report(Progress{Step: "rendering the session time-lapse", Index: 6, Total: sunSteps,
			Line: fmt.Sprintf("%d window(s)", len(masters))})
		if out, verr := renderTimelapse(ctx, opts, masters, preset, outDir, object); verr != nil {
			res.Warnings = append(res.Warnings, "sun: time-lapse: "+verr.Error())
		} else {
			res.Final.Outputs = append(res.Final.Outputs, out)
		}
	}
	res.StagePreviews = collectStagePreviews(outDir)
	writeRunJSON(outDir, res)
	return res, nil
}

// Stage-preview ordering for a solar run: an ingested frame, then each window's master, then the
// finished image — the milestones a viewer needs to tell "the frames were bad" from "the stack was
// bad" from "the finish was bad".
const (
	ordSunFrame  = 50
	ordSunWindow = 100
	ordSunFinal  = 900
)

// copyFinalPreview registers the finished image as the last milestone of the timeline.
func copyFinalPreview(opts Options, outDir string, final *postprocess.Result) {
	if final == nil || len(final.Outputs) == 0 {
		return
	}
	dir := filepath.Join(outDir, "previews")
	if err := fsutil.EnsureDir(dir); err != nil {
		return
	}
	dst := filepath.Join(dir, fmt.Sprintf("%03d_final.png", ordSunFinal))
	if err := fsutil.CopyFile(final.Outputs[0], dst); err != nil {
		return
	}
	opts.report(Progress{StagePreview: &postprocess.StagePreview{Index: ordSunFinal, Stage: "final", PngPath: dst}})
}

// reportTriage narrates the grouping decision, so the run explains what it chose and why before it
// spends ten minutes on it.
func reportTriage(opts Options, rep *solar.Report, total int) {
	opts.report(Progress{Step: "inspecting the capture", Index: 1, Total: total,
		Line: fmt.Sprintf("%d file(s) → %d group(s) by measured disc size", rep.Files, len(rep.Groups))})
	for _, g := range rep.Groups {
		mark := "skip"
		if g.Stackable {
			mark = "use "
		}
		opts.report(Progress{Step: "inspecting the capture", Index: 1, Total: total,
			Line: fmt.Sprintf("  %s %s — %d file(s), %d frame(s), ⌀%.0f px", mark, g.Label, g.Files, g.Frames, 2*g.DiscRadius)})
	}
}

// sunPreview renders and registers one milestone preview. A failure here is never fatal: a missing
// thumbnail must not cost a run that otherwise succeeded.
func sunPreview(opts Options, res *Result, outDir string, ord int, stage, framePath string, limb solar.Limb, p solar.Preset) {
	dir := filepath.Join(outDir, "previews")
	if err := fsutil.EnsureDir(dir); err != nil {
		return
	}
	dst := filepath.Join(dir, fmt.Sprintf("%03d_%s.png", ord, stage))
	var img *fits.Image
	switch {
	case framePath != "":
		im, err := fits.ReadImage(framePath)
		if err != nil {
			return
		}
		mono := &fits.Image{W: im.W, H: im.H, C: 1, Pix: [][]float32{im.Pix[0]}}
		img = solar.Finish(mono, limb, p.Finish)
	default:
		return
	}
	if err := solar.WritePNG(img, dst); err != nil {
		return
	}
	opts.report(Progress{StagePreview: &postprocess.StagePreview{Index: ord, Stage: stage, PngPath: dst}})
}

// sunWindowMaster is one stacked window plus where it was persisted.
type sunWindowMaster struct {
	Stack  *solar.StackResult
	Path   string
	Window solar.Window
}

// pickGroup chooses which triage group to stack: the highest-scoring stackable one that the caller
// has not excluded.
func pickGroup(rep *solar.Report, excluded []string, merge bool) (solar.Group, error) {
	drop := map[string]bool{}
	for _, id := range excluded {
		drop[id] = true
	}
	var keep []solar.Group
	for _, g := range rep.Groups {
		if g.Stackable && !drop[g.ID] {
			keep = append(keep, g)
		}
	}
	if len(keep) == 0 {
		return solar.Group{}, fmt.Errorf("no stackable group (of %d found); see the triage report", len(rep.Groups))
	}
	if !merge || len(keep) == 1 {
		return keep[0], nil
	}
	return solar.MergeGroups(keep), nil
}

// ingestGroup materialises a group's frames, dispatching per source kind.
func ingestGroup(ctx context.Context, g solar.Group, p solar.Preset, workDir, ffmpegBin string,
	note func(string)) ([]solar.Frame, []string, error) {
	io := p.IngestOpts(workDir, ffmpegBin, g.DiscRadius)
	var frames []solar.Frame
	var warnings []string
	var stills []string
	for _, m := range g.Members {
		if m.Rejected {
			continue
		}
		if m.Kind == solar.KindVideo && m.Video != nil {
			if note != nil {
				note(fmt.Sprintf("scanning %s (%d frames)", filepath.Base(m.Path), m.Video.Frames))
			}
			f, w, err := solar.IngestVideo(ctx, m.Path, *m.Video, io)
			warnings = append(warnings, w...)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %v", filepath.Base(m.Path), err))
				continue
			}
			frames = append(frames, f...)
			continue
		}
		stills = append(stills, m.Path)
	}
	if len(stills) > 0 {
		if note != nil {
			note(fmt.Sprintf("developing %d still(s)", len(stills)))
		}
		f, w, err := solar.IngestStills(ctx, stills, io)
		warnings = append(warnings, w...)
		if err != nil {
			warnings = append(warnings, err.Error())
		}
		frames = append(frames, f...)
	}
	if len(frames) == 0 {
		return nil, warnings, fmt.Errorf("no frame survived ingest")
	}
	return frames, warnings, nil
}

// stackWindows stacks each window and persists its master, which is what a re-finish replays.
func stackWindows(ctx context.Context, opts Options, windows []solar.Window, p solar.Preset, outDir string,
	res *Result, total int) ([]sunWindowMaster, []string) {

	var out []sunWindowMaster
	var warnings []string
	for i, w := range windows {
		opts.report(Progress{Step: "stacking", Index: 4, Total: total,
			Line: fmt.Sprintf("window %d/%d — registering and stacking %d frames", i+1, len(windows), w.Count)})
		st, err := solar.Stack(ctx, w.Frames, p.StackOpts())
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("window %d: %v", i+1, err))
			continue
		}
		warnings = append(warnings, st.Notes...)
		path := filepath.Join(outDir, fmt.Sprintf("master_w%02d.fits", i+1))
		if err := st.Master.WriteFITS(path); err != nil {
			warnings = append(warnings, fmt.Sprintf("window %d: persist: %v", i+1, err))
			continue
		}
		out = append(out, sunWindowMaster{Stack: st, Path: path, Window: w})
		opts.report(Progress{Step: "stacking", Index: 4, Total: total,
			Line: fmt.Sprintf("window %d/%d — %d frames stacked", i+1, len(windows), st.Frames)})
		sunPreview(opts, res, outDir, ordSunWindow+i, fmt.Sprintf("window%02d", i+1), path, st.Limb, p)
	}
	return out, warnings
}

// finishSun renders the hero window and returns the run result.
func finishSun(opts Options, masters []sunWindowMaster, hero int, p solar.Preset, outDir, object string) (*postprocess.Result, error) {
	m := masters[hero]
	img := solar.Finish(m.Stack.Master, m.Stack.Limb, p.Finish)
	base := filepath.Join(outDir, object+"_stack")
	outs, err := writeSunImage(img, base)
	if err != nil {
		return nil, err
	}
	return &postprocess.Result{
		Mode:     "sun",
		Channels: []string{string(p.Band)},
		Outputs:  outs,
		Notes: []string{fmt.Sprintf("window %d of %d, %d frames stacked, disc ⌀%.0f px",
			hero+1, len(masters), m.Stack.Frames, 2*m.Stack.Limb.R)},
	}, nil
}

// renderTimelapse finishes every window with IDENTICAL parameters and renders them as a clip.
//
// Identical is the operative word. Re-tuning per window — or letting anything auto-scale to each
// window's own statistics — makes the sequence pulse, because the brightest thing on the disc is
// plage and plage evolves. Every frame of the time-lapse therefore goes through one finish, and the
// stretch is anchored on the hero window's master rather than on each frame's own histogram.
func renderTimelapse(ctx context.Context, opts Options, masters []sunWindowMaster, p solar.Preset, outDir, object string) (string, error) {
	seqDir := filepath.Join(outDir, "timelapse")
	if err := fsutil.EnsureDir(seqDir); err != nil {
		return "", err
	}
	for i, m := range masters {
		img := solar.Finish(m.Stack.Master, m.Stack.Limb, p.Finish)
		if _, err := writeSunImage(img, filepath.Join(seqDir, fmt.Sprintf("f_%04d", i+1))); err != nil {
			return "", err
		}
	}
	out := filepath.Join(outDir, object+"_timelapse.mp4")
	if err := videoout.RenderSequence(ctx, opts.FfmpegBin, filepath.Join(seqDir, "f_%04d.png"), out, len(masters)); err != nil {
		return "", err
	}
	return out, nil
}

// writeSunImage saves a finished RGB image, returning the paths written.
func writeSunImage(img *fits.Image, base string) ([]string, error) {
	out := base + ".png"
	if err := solar.WritePNG(img, out); err != nil {
		return nil, err
	}
	return []string{out}, nil
}

// sunObject names the run from its input folder.
func sunObject(inputDir string) string {
	base := filepath.Base(strings.TrimRight(inputDir, string(filepath.Separator)))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "sun"
	}
	// A run may be pointed at a single clip rather than a folder — comparing one video against
	// another is a normal thing to want — so drop the extension to keep the output path clean.
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// writeTriage persists the compatibility report beside the run, so the grouping decision stays
// auditable after the fact.
func writeTriage(outDir string, rep *solar.Report, res *Result) {
	blob, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		res.Warnings = append(res.Warnings, "sun: triage report: "+err.Error())
		return
	}
	if err := os.WriteFile(filepath.Join(outDir, "triage.json"), blob, 0o644); err != nil {
		res.Warnings = append(res.Warnings, "sun: triage report: "+err.Error())
	}
}
