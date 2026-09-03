package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/buildinfo"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/mode"
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
	modeName := string(mode.Sun)
	if opts.Preset != nil {
		preset = opts.Preset.Sun
		if opts.Preset.Mode != "" {
			modeName = string(opts.Preset.Mode)
		}
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

	// The sequence is a seventh step when it is on, so the progress bar does not stall at 6/6 for
	// the several minutes a sheet of panels takes.
	sunSteps := 6
	if preset.WantsSequence() {
		sunSteps = 7
	}
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
	// The recording's own colour, measured before anything is rendered. It is a property of the
	// SOURCE, so it is taken from the clip rather than from the master — by the time a master exists
	// the colour has been through a mono stack and there is none left to measure.
	if preset.WantsNativeColour() {
		if ch, nerr := measureNativeColour(ctx, group, opts.FfmpegBin); nerr != nil {
			res.Warnings = append(res.Warnings,
				"sun: native colour: "+nerr.Error()+" — falling back to the gold palette")
			preset.Finish.Palette = solar.PaletteGold
		} else {
			preset.Finish.NativeChroma = ch
			opts.report(Progress{Step: "ingesting frames", Index: 2, Total: sunSteps,
				Line: "measured the capture's own colour for the finish"})
		}
	}

	sunPreview(opts, res, outDir, ordSunFrame, "frame", frames[0].Path,
		solar.Pair{Sun: frames[0].Limb, Moon: frames[0].Moon}, preset)

	if n := exportBestFrames(opts, frames, preset, outDir, object); n > 0 {
		opts.report(Progress{Step: "ingesting frames", Index: 2, Total: sunSteps,
			Line: fmt.Sprintf("exported the %d sharpest frames, spread through the clip, to frames/", n)})
	}

	// A bracketed group is split here and stays split all the way to the composite. Exposure tiers
	// are normalised, windowed and stacked apart from each other because every one of those steps
	// compares a frame against its siblings — normalisation maps onto their median, clipping rejects
	// what disagrees with them — and across a two-stop gap those comparisons are meaningless.
	tiers := buildSunTiers(group, frames, preset)
	opts.report(Progress{Step: "normalising exposures", Index: 3, Total: sunSteps,
		Line: sunTierLine(tiers, len(frames))})
	for i := range tiers {
		if nWarn, nerr := solar.Normalize(tiers[i].Frames); nerr != nil {
			res.Warnings = append(res.Warnings, "sun: photometric normalisation: "+nerr.Error())
		} else {
			res.Warnings = append(res.Warnings, nWarn...)
		}
		tiers[i].Windows = solar.Windows(tiers[i].Frames, preset.WindowOpts())
	}

	opts.report(Progress{Step: "stacking", Index: 4, Total: sunSteps,
		Line: fmt.Sprintf("%d window(s) over %d frame(s)", countWindows(tiers), len(frames))})
	sWarn := stackTiers(ctx, opts, tiers, preset, outDir, res, sunSteps)
	res.Warnings = append(res.Warnings, sWarn...)
	masters := tiers[0].Masters
	if len(masters) == 0 {
		return nil, fmt.Errorf("sun: no window produced a master")
	}

	opts.report(Progress{Step: "finishing", Index: 5, Total: sunSteps})
	hero, hWarn := heroMaster(opts, tiers, outDir, res, preset, sunSteps)
	res.Warnings = append(res.Warnings, hWarn...)
	if hero == nil {
		return nil, fmt.Errorf("sun: no master to finish")
	}

	// The finish is resolved ONCE, against the master the run is about to render, and the resolved
	// settings are then what the hero, the time-lapse and the run record all use. Deconvolution is
	// only meaningful at the width of the point spread function, and that is measured from this
	// master's own limb rather than assumed — but the time-lapse's own rule still holds, that every
	// frame of it goes through one identical finish, so the resolution has to happen here and not
	// separately per render.
	fin, psf, notes := solar.ResolveFinish(hero.Master, hero.Limb, preset.Finish)
	for _, n := range notes {
		opts.report(Progress{Step: "finishing", Index: 5, Total: sunSteps, Line: n})
		res.Warnings = append(res.Warnings, n)
	}
	if psf.OK {
		res.PSF = &psf
	}
	preset.Finish = fin

	final, ferr := finishSun(opts, hero, preset, outDir, object, modeName)
	if ferr != nil {
		return nil, fmt.Errorf("sun: %w", ferr)
	}
	res.Final = final
	copyFinalPreview(opts, outDir, final)

	// The time-lapse is rendered whenever the session split into more than one window, regardless of
	// the requested format: windowing exists because the scene evolves, so the evolution is the
	// result, not an optional extra. The job layer may still add its own Ken-Burns pan on top.
	//
	// It runs over the REFERENCE tier alone. A time-lapse is a sequence in time, and a bracket is a
	// sequence in exposure that happens to have been shot in time order — interleaving the two would
	// render the same minute twice at two brightnesses, which reads as a flicker rather than as the
	// chromosphere moving.
	if len(masters) > 1 {
		opts.report(Progress{Step: "rendering the session time-lapse", Index: 6, Total: sunSteps,
			Line: fmt.Sprintf("%d window(s)", len(masters))})
		if out, verr := renderTimelapse(ctx, opts, masters, preset, outDir, object); verr != nil {
			res.Warnings = append(res.Warnings, "sun: time-lapse: "+verr.Error())
		} else {
			res.Final.Outputs = append(res.Final.Outputs, out)
		}
	}
	if preset.WantsSequence() {
		opts.report(Progress{Step: "rendering the phase sequence", Index: 7, Total: sunSteps})
		res.Warnings = append(res.Warnings,
			renderPhaseSequence(ctx, opts, group, frames, preset, outDir, object, res, 7, sunSteps)...)
	}
	res.StagePreviews = collectStagePreviews(outDir)
	writeRunJSON(outDir, res)
	return res, nil
}

// Stage-preview ordering for a solar run: an ingested frame, then each window's master, then the
// finished image — the milestones a viewer needs to tell "the frames were bad" from "the stack was
// bad" from "the finish was bad".
const (
	ordSunFrame     = 50
	ordSunWindow    = 100
	ordSunComposite = 800
	ordSunFinal     = 900
)

// sunCompositeMaster is where a bracketed run persists its exposure composite — the master it
// finished, and therefore the one a re-finish must replay.
const sunCompositeMaster = "master_hdr.fits"

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
func sunPreview(opts Options, res *Result, outDir string, ord int, stage, framePath string, g solar.Pair, p solar.Preset) {
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
		// Every milestone is resolved against ITS OWN limb, the same way the final image is.
		//
		// The timeline exists to answer "was it the frames, the stack or the finish?", and it can only
		// answer that if the only difference between the pictures is the thing being compared. Render
		// a single frame at the finish's fallback deconvolution width and the stack at its measured
		// one and the timeline reports a difference the pipeline invented — which reads, wrongly and
		// convincingly, as the stack having thrown detail away.
		fin, _, _ := solar.ResolveFinish(mono, g.Sun, p.Finish)
		img = solar.FinishPair(mono, g, fin)
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

// sunTier is one exposure tier of a run: the files shot at that exposure, their frames, and what
// stacking them produced. A session shot at one exposure is one tier, and every step below then
// behaves exactly as it did before brackets existed.
type sunTier struct {
	Label   string
	Level   float64 // measured on-disc median of the tier's files, the exposure it stands for
	Frames  []solar.Frame
	Windows []solar.Window
	Masters []sunWindowMaster
}

// sunHero is the master the run finishes: one window of a single-exposure session, or the bracket
// composite of several tiers' best windows.
type sunHero struct {
	Master *fits.Image
	Limb   solar.Limb
	// Moon is the occulting body on the same raster, R=0 when there is none. Without it the finish
	// treats the hole the stack left as a very dark piece of Sun.
	Moon solar.Limb
	Note string
}

// pair is the hero's geometry as the finish wants it.
func (h *sunHero) pair() solar.Pair { return solar.Pair{Sun: h.Limb, Moon: h.Moon} }

// buildSunTiers assigns the ingested frames to their exposure tiers.
//
// The split is decided on the FILES, by triage's measured disc level, and the frames follow their
// source. Deciding it on the frames instead would be measuring the phone's own metering jitter and
// splitting a single clip in half on it.
func buildSunTiers(group solar.Group, frames []solar.Frame, p solar.Preset) []sunTier {
	groups := p.Tiers(group.Members)
	if len(groups) == 0 {
		return []sunTier{{Label: "all frames", Frames: frames}}
	}
	index := map[string]int{}
	tiers := make([]sunTier, len(groups))
	for i, g := range groups {
		tiers[i] = sunTier{Label: sunTierLabel(i, g), Level: solar.TierLevel(g)}
		for _, m := range g {
			index[m.Path] = i
		}
	}
	for _, f := range frames {
		// A frame whose source was not tiered — it cannot normally happen, since every ingested frame
		// came from a member — joins the reference tier rather than being dropped.
		i, ok := index[f.Source]
		if !ok {
			i = 0
		}
		tiers[i].Frames = append(tiers[i].Frames, f)
	}
	out := tiers[:0]
	for _, t := range tiers {
		if len(t.Frames) > 0 {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return []sunTier{{Label: "all frames", Frames: frames}}
	}
	return out
}

// sunTierLabel names a tier by its files, which is how the user thinks of a bracket.
func sunTierLabel(i int, members []solar.Member) string {
	if len(members) == 1 {
		return filepath.Base(members[0].Path)
	}
	return fmt.Sprintf("exposure %d (%d files)", i+1, len(members))
}

// sunTierLine narrates the exposure split before the run spends minutes on it.
func sunTierLine(tiers []sunTier, frames int) string {
	if len(tiers) < 2 {
		return fmt.Sprintf("%d frame(s) onto one photometric scale", frames)
	}
	parts := make([]string, 0, len(tiers))
	for _, t := range tiers {
		parts = append(parts, fmt.Sprintf("%s %d frame(s)", t.Label, len(t.Frames)))
	}
	return fmt.Sprintf("bracket: %d exposure tiers spanning %.1f stops — %s — each onto its own scale",
		len(tiers), sunTierSpanStops(tiers), strings.Join(parts, ", "))
}

// sunTierSpanStops is the exposure range the bracket covers, measured rather than read off metadata
// (a phone clip carries no shutter or ISO at all).
func sunTierSpanStops(tiers []sunTier) float64 {
	hi, lo := 0.0, math.Inf(1)
	for _, t := range tiers {
		if t.Level <= 0 {
			continue
		}
		hi, lo = math.Max(hi, t.Level), math.Min(lo, t.Level)
	}
	if hi <= 0 || math.IsInf(lo, 1) || lo <= 0 {
		return 0
	}
	return math.Log2(hi / lo)
}

// countWindows totals the windows across every tier.
func countWindows(tiers []sunTier) int {
	n := 0
	for _, t := range tiers {
		n += len(t.Windows)
	}
	return n
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

// stackTiers stacks every window of every exposure tier and persists its master, which is what a
// re-finish replays.
func stackTiers(ctx context.Context, opts Options, tiers []sunTier, p solar.Preset, outDir string,
	res *Result, total int) []string {

	var warnings []string
	for t := range tiers {
		for i, w := range tiers[t].Windows {
			label := sunWindowLabel(tiers, t, i)
			opts.report(Progress{Step: "stacking", Index: 4, Total: total,
				Line: fmt.Sprintf("%s — registering and stacking %d frames", label, w.Count)})
			st, err := solar.Stack(ctx, w.Frames, p.StackOpts())
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %v", label, err))
				continue
			}
			warnings = append(warnings, st.Notes...)
			path := filepath.Join(outDir, sunMasterName(t, i))
			if err := st.Master.WriteFITS(path); err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: persist: %v", label, err))
				continue
			}
			tiers[t].Masters = append(tiers[t].Masters, sunWindowMaster{Stack: st, Path: path, Window: w})
			opts.report(Progress{Step: "stacking", Index: 4, Total: total,
				Line: fmt.Sprintf("%s — %d frames stacked", label, st.Frames)})
			sunPreview(opts, res, outDir, ordSunWindow+20*t+i, sunPreviewStage(t, i), path, st.Pair(), p)
		}
	}
	return warnings
}

// sunMasterName is where one tier's window master is persisted.
//
// The reference tier keeps the historic `master_wNN.fits`, so an existing run directory, the Refine
// panel and anything else that globs for those files behave exactly as they did. Only the extra
// tiers a bracket introduces get a new name.
func sunMasterName(tier, window int) string {
	if tier == 0 {
		return fmt.Sprintf("master_w%02d.fits", window+1)
	}
	return fmt.Sprintf("master_t%02d_w%02d.fits", tier+1, window+1)
}

// sunPreviewStage names a window's stage preview.
func sunPreviewStage(tier, window int) string {
	if tier == 0 {
		return fmt.Sprintf("window%02d", window+1)
	}
	return fmt.Sprintf("exposure%02d_window%02d", tier+1, window+1)
}

// sunWindowLabel describes a window in progress output, naming its tier only when there is more than
// one to tell apart.
func sunWindowLabel(tiers []sunTier, t, i int) string {
	if len(tiers) < 2 {
		return fmt.Sprintf("window %d/%d", i+1, len(tiers[t].Windows))
	}
	return fmt.Sprintf("%s window %d/%d", tiers[t].Label, i+1, len(tiers[t].Windows))
}

// heroMaster produces the image the run finishes.
//
// With one exposure that is the sharpest window, exactly as before. With a bracket it is the
// composite of each tier's sharpest window: a tier is stacked against its own siblings, and only
// then — once each exposure is a clean master with a measurable noise level — are they put on one
// scale and combined.
func heroMaster(opts Options, tiers []sunTier, outDir string, res *Result, p solar.Preset, total int) (*sunHero, []string) {
	var warnings []string
	best := make([]solar.Exposure, 0, len(tiers))
	var lead *sunWindowMaster
	for t := range tiers {
		m := sharpestMaster(tiers[t])
		if m == nil {
			if t == 0 {
				continue
			}
			warnings = append(warnings, fmt.Sprintf("sun: %s produced no master and is left out of the composite", tiers[t].Label))
			continue
		}
		if lead == nil {
			lead = m
		}
		best = append(best, solar.Exposure{Master: m.Stack.Master, Limb: m.Stack.Limb, Label: tiers[t].Label})
	}
	if lead == nil {
		return nil, warnings
	}
	hero := &sunHero{Master: lead.Stack.Master, Limb: lead.Stack.Limb, Moon: lead.Stack.Moon,
		Note: sunHeroNote(lead.Stack)}
	if len(best) < 2 {
		return hero, warnings
	}

	opts.report(Progress{Step: "finishing", Index: 5, Total: total,
		Line: fmt.Sprintf("compositing %d exposure tiers into one high-dynamic-range master", len(best))})
	merged, err := solar.MergeExposures(best)
	if err != nil {
		warnings = append(warnings, "sun: exposure composite: "+err.Error()+" — finishing the brightest exposure alone")
		return hero, warnings
	}
	warnings = append(warnings, merged.Notes...)
	for _, t := range merged.Tiers[1:] {
		opts.report(Progress{Step: "finishing", Index: 5, Total: total,
			Line: fmt.Sprintf("  %s: %.2f stops under, rotated %+.2f°, contributing %.0f%% of the composite",
				t.Label, t.Stops, t.RotationDeg, 100*t.Share)})
	}
	path := filepath.Join(outDir, sunCompositeMaster)
	if err := merged.Master.WriteFITS(path); err != nil {
		warnings = append(warnings, "sun: exposure composite: persist: "+err.Error())
	} else {
		sunPreview(opts, res, outDir, ordSunComposite, "composite", path,
			solar.Pair{Sun: merged.Limb, Moon: lead.Stack.Moon}, p)
	}
	res.Bracket = merged.Tiers
	// The composite is built on the brightest tier's raster, so that tier's occulter is the one that
	// describes it.
	return &sunHero{Master: merged.Master, Limb: merged.Limb, Moon: lead.Stack.Moon,
		Note: fmt.Sprintf("%d exposures composited over %.1f stops, disc ⌀%.0f px",
			len(best), sunTierSpanStops(tiers), 2*merged.Limb.R)}, warnings
}

// sharpestMaster returns a tier's best window master, or nil when nothing stacked.
func sharpestMaster(t sunTier) *sunWindowMaster {
	if len(t.Masters) == 0 {
		return nil
	}
	if len(t.Windows) == 0 {
		return &t.Masters[0]
	}
	// Sharpest indexes the WINDOWS; a window that failed to stack has no master, so the pick is
	// resolved by matching window start times rather than by index.
	want := t.Windows[solar.Sharpest(t.Windows)]
	for i := range t.Masters {
		if t.Masters[i].Window.StartMs == want.StartMs && t.Masters[i].Window.Count == want.Count {
			return &t.Masters[i]
		}
	}
	return &t.Masters[0]
}

// finishSun renders the hero master and returns the run result.
func finishSun(opts Options, hero *sunHero, p solar.Preset, outDir, object, modeName string) (*postprocess.Result, error) {
	if hero == nil {
		return nil, fmt.Errorf("no master to finish")
	}
	img := solar.FinishPair(hero.Master, hero.pair(), p.Finish)
	base := filepath.Join(outDir, object+"_stack")
	outs, err := writeSunImage(img, base)
	if err != nil {
		return nil, err
	}
	return &postprocess.Result{
		Mode:     modeName,
		Channels: []string{string(p.Band)},
		Outputs:  outs,
		Notes:    []string{hero.Note},
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
		img := solar.FinishPair(m.Stack.Master, m.Stack.Pair(), p.Finish)
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

// sunHeroNote describes what the hero master is made of, naming the occulter when there is one.
func sunHeroNote(st *solar.StackResult) string {
	if st.Moon.R <= 0 {
		return fmt.Sprintf("%d frames stacked, disc ⌀%.0f px", st.Frames, 2*st.Limb.R)
	}
	return fmt.Sprintf("%d frames stacked, disc ⌀%.0f px, occulter ⌀%.0f px covering %.0f%%",
		st.Frames, 2*st.Limb.R, 2*st.Moon.R, 100*solar.OverlapFraction(st.Limb, st.Moon))
}

// measureNativeColour reads the recording's colour off the first video the chosen group holds.
//
// One source, not all of them: a group is by construction one optical configuration at one scale, so
// its members were shot through the same train and share a colour. Averaging several would only add
// the risk that a clip shot at a different exposure drags the hue.
func measureNativeColour(ctx context.Context, g solar.Group, ffmpegBin string) (solar.NativeChroma, error) {
	for _, m := range g.Members {
		if m.Rejected || m.Kind != solar.KindVideo || m.Video == nil {
			continue
		}
		return solar.MeasureNativeChroma(ctx, ffmpegBin, m.Path, *m.Video)
	}
	return solar.NativeChroma{}, fmt.Errorf("no video in the chosen group to measure")
}

// exportBestFrames renders a spread of the sharpest individual frames at their own resolution.
//
// Each is finished against its OWN measured geometry and its own point spread function, not the
// run's. That is the whole point of exporting them: a frame is not a small stack, and deconvolving
// one at the master's width would blur the sharp ones and over-correct the soft ones — the very
// comparison these exist to let a person make.
func exportBestFrames(opts Options, frames []solar.Frame, p solar.Preset, outDir, object string) int {
	if p.BestFrames <= 0 || len(frames) == 0 {
		return 0
	}
	dir := filepath.Join(outDir, "frames")
	if err := fsutil.EnsureDir(dir); err != nil {
		return 0
	}
	picks := solar.SelectSpread(frames, p.BestFrames, int64(p.BestFrameGapSeconds*1000))
	n := 0
	for i, f := range picks {
		im, err := fits.ReadImage(f.Path)
		if err != nil {
			continue
		}
		mono := &fits.Image{W: im.W, H: im.H, C: 1, Pix: [][]float32{im.Pix[0]}}
		g := solar.Pair{Sun: f.Limb, Moon: f.Moon}
		fin, psf, _ := solar.ResolveFinish(mono, g.Sun, p.Finish)
		// A frame whose edge cannot be measured does not get exported, however well it scored.
		// The sharpness metric and the limb measurement can disagree, and when they do it is the
		// metric that is wrong: on the 12 Aug clip one frame scored twice everything else while its
		// point spread function came back unmeasurable — a frame that is not a picture of the Sun
		// still has band-pass energy, and plenty of it.
		if !psf.OK {
			continue
		}
		base := filepath.Join(dir, fmt.Sprintf("%s_best%02d", object, i+1))
		if _, err := writeSunImage(solar.FinishPair(mono, g, fin), base); err != nil {
			continue
		}
		n++
	}
	return n
}
