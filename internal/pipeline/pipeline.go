// Package pipeline orchestrates the end-to-end deep-sky workflow: inspect a directory, build
// master calibration frames, then for each light channel match the right masters and run
// calibrate → register → stack via Siril. Channel combination lives in package postprocess.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/gimp"
	"github.com/verove-jordan/astronomy/internal/grade"
	"github.com/verove-jordan/astronomy/internal/graxpert"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/llm"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/skycat"
	"github.com/verove-jordan/astronomy/internal/starnet"
	"github.com/verove-jordan/astronomy/internal/sysmon"
)

// trailDownsample is the working size (larger axis) for the trail detector.
const trailDownsample = 512

// Options configures a pipeline run.
type Options struct {
	InputDir string
	// InputDirs, when non-empty, are the capture folders merged into one Inventory for this run
	// (cross-folder multi-select). Empty → scan InputDir alone. InputDir stays the primary dir used
	// for run naming, the output path, and the target lock.
	InputDirs   []string
	OutputDir   string
	WorkDir     string
	Runner      *siril.Runner
	Grade       *grade.Options       // nil → grade.DefaultOptions()
	Postprocess *postprocess.Options // nil → postprocess.DefaultOptions()
	Preset      *mode.Preset         // mode preset (curves/Ha/saturation for the GIMP finish)
	Gimp        *gimp.Client         // nil → Siril-only finish (no layered GIMP composite)
	Graxpert    *graxpert.Runner     // nil → Siril subsky only (no AI background extraction)
	Starnet     *starnet.Runner      // nil → no star removal in the finish
	Supervisor  *llm.Runner          // nil → no local-AI-agent finish supervision (opt-in; see supervise.go)
	Library     calib.MasterStore    // nil → no reuse; masters built into scratch
	LibraryDir  string               // persistent master library dir (when Library is set)
	// PhoneCalib is the reusable phone/DSLR calibration-master library for the milkyway path (nil → no
	// persistence/reuse; masters are built per run). Its masters are written under LibraryDir.
	PhoneCalib calib.PhoneCalibStore
	OnProgress func(Progress)

	// JobID ties persisted finish-supervisor iterations to a job row; 0 for non-job runs (CLI/MCP),
	// which skip iteration persistence. FinishIterStore is the sink (nil → no persistence).
	JobID           int64
	FinishIterStore FinishIterStore

	// Reprocess re-runs the stack stages (calibrate → register → grade → stack) from the raw frames
	// with a modified preset and returns fresh aligned-channel masters. It is the Tier-C re-entry the
	// supervised finish uses for structural fixes; nil (a pure refine with no raws) disables Tier C, so
	// the supervisor caps at re-running the finish prep. Set by Process/ProcessOSC.
	Reprocess func(ctx context.Context, preset *mode.Preset) (map[string]string, error)

	// Catalog records the scanned inventory (frames + target) so future runs can reuse this data.
	// nil → the run is not persisted and no cross-session reuse is possible.
	Catalog Catalog
	// RawCalib pools raw bias/dark frames from every prior session into deeper, lower-noise masters.
	// nil → fall back to the stacked-master library (BuildOrReuseMasters) or scratch.
	RawCalib calib.RawCalibProvider
	// Deep tunes the raw-calibration pool (dark recency + temperature tolerance).
	Deep calib.DeepOptions
	// Reuse folds prior light frames of the same target into the stack to grow integration. The
	// zero value (nil Provider) disables it, leaving the run single-session.
	Reuse ReuseConfig

	// FilterMapping is an optional user override (detected/known filter → chosen channel; "" or
	// "ignore" excludes it), applied during the scan.
	FilterMapping map[string]string
	// Solve / Spcc are the plate-solve + SPCC inputs for color calibration (from config).
	Solve siril.SolveOptions
	Spcc  siril.SpccOptions
	// CatalogDir is Siril's bundled object-catalogue dir, used to resolve the target name → coords
	// for plate-solving when the FITS header lacks RA/Dec.
	CatalogDir string

	// DarkDir/FlatDir/BiasDir are optional calibration-frame folders for the nightscape (milkyway)
	// path: lights in opts.InputDir are calibrated against masters built from these before stacking.
	// Empty keeps the proven uncalibrated single-pass path. Offset == bias.
	DarkDir, FlatDir, BiasDir string

	// CalibExclude holds SuggestID keys (per light-set, per role) the user unchecked in the Import
	// "Calibration" panel; the matcher drops those darks/flats/bias from each channel's selection.
	CalibExclude []string
}

// scanRoots returns the capture folders to merge for this run: the explicit InputDirs when set,
// otherwise just InputDir. The first element is the primary dir (naming, output, lock).
func (o Options) scanRoots() []string {
	if len(o.InputDirs) > 0 {
		return o.InputDirs
	}
	return []string{o.InputDir}
}

// Catalog persists a scanned inventory as a session (frames + resolved target) and returns the new
// session id. Implemented by package store; an interface keeps pipeline free of a DB dependency.
type Catalog interface {
	SaveInventory(ctx context.Context, inv *inspect.Inventory) (int64, error)
}

// FinishIterStore persists the supervisor's per-iteration finish decisions. An interface keeps
// pipeline free of a DB dependency (implemented by package store).
type FinishIterStore interface {
	CreateFinishIteration(ctx context.Context, jobID int64, iter int, tier string, params, metrics, defects []byte, detScore, modelScore, combined float64, reasoning string) (int64, error)
	MarkFinishIterationChosen(ctx context.Context, id int64) error
}

// libraryDir resolves the persistent master-library directory (absolute), defaulting under workAbs.
func libraryDir(opts Options, workAbs string) (string, error) {
	dir := opts.LibraryDir
	if dir == "" {
		dir = filepath.Join(workAbs, "library")
	}
	return filepath.Abs(dir)
}

// Progress is a pipeline-level progress event (and forwarded Siril log lines). When Sample is
// non-nil the event carries a live resource reading for the running step instead of a log line.
type Progress struct {
	Step    string         `json:"step"`
	Index   int            `json:"index"`
	Total   int            `json:"total"`
	Line    string         `json:"line,omitempty"`
	Preview string         `json:"preview,omitempty"` // a preview PNG path, emitted as it is produced
	Sample  *sysmon.Sample `json:"-"`                 // live CPU/RAM of the step's subprocess; not serialized
	// Iteration carries one completed supervised-finish pass as it happens, so the UI can stream the
	// agent's iterations (preview + defects + scores) live instead of only after the job finishes.
	Iteration *postprocess.IterationRecord `json:"iteration,omitempty"`
}

// ChannelResult is the stacked output for one light channel (filter).
type ChannelResult struct {
	Object        string          `json:"object"`
	Filter        string          `json:"filter"`
	ExposureMs    int64           `json:"exposure_ms"`
	InputFrames   int             `json:"input_frames"`
	StackedFrames int             `json:"stacked_frames"`
	OutputPath    string          `json:"output_path,omitempty"`
	PreviewPath   string          `json:"preview_path,omitempty"`
	Selection     calib.Selection `json:"selection"`
	Metrics       []grade.Metric  `json:"metrics,omitempty"`
	Err           string          `json:"error,omitempty"`
}

// Result summarizes a completed run. It is both the API/DB job result and the durable on-disk
// run.json, so it carries everything the UI charts (per-channel metrics, masters, detection).
// The full Inventory is excluded (json:"-") to keep the row small; Detection is the compact summary.
type Result struct {
	InputDir  string                    `json:"input_dir"`
	OutputDir string                    `json:"output_dir"`
	Object    string                    `json:"object,omitempty"`
	RunID     string                    `json:"run_id,omitempty"`
	Inventory *inspect.Inventory        `json:"-"`
	Detection *inspect.ChannelDetection `json:"detection,omitempty"`
	Masters   []calib.Master            `json:"masters"`
	Channels  []ChannelResult           `json:"channels"`
	Final     *postprocess.Result       `json:"final,omitempty"`
	Reuse     *ReuseSummary             `json:"reuse,omitempty"` // prior data folded into this run
	Warnings  []string                  `json:"warnings"`
}

// Process runs the full pipeline and returns its result. Per-channel failures are recorded as
// warnings/channel errors rather than aborting the whole run.
func Process(ctx context.Context, opts Options) (*Result, error) {
	if err := opts.Runner.Available(ctx); err != nil {
		return nil, fmt.Errorf("siril unavailable: %w", err)
	}
	scanOpts := inspect.DefaultScanOptions()
	scanOpts.FilterMapping = opts.FilterMapping
	inv, err := inspect.ScanMany(ctx, opts.scanRoots(), scanOpts)
	if err != nil {
		return nil, err
	}
	// The mono per-filter pipeline cannot stack one-shot-color (Bayer) frames — drop them (with a warning)
	// before grouping, so a directory that mixes mono FITS with an older OSC session does not mis-stack
	// the colour mosaics. The OSC frames are processed by the dedicated OSC path instead.
	if n := inv.ExcludeBayer(); n > 0 {
		inv.Warnings = append(inv.Warnings, fmt.Sprintf(
			"%d one-shot-color (Bayer) frame(s) excluded from the mono pipeline — process them with the OSC path", n))
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
	if object == "session" { // no OBJECT header — name the run after the target folder (e.g. M101)
		if base := smartObject(opts.InputDir); base != "session" {
			object = base
		}
	}
	outDir := filepath.Join(outAbs, object, runID)
	if err := fsutil.EnsureDir(outDir); err != nil {
		return nil, err
	}

	// Resolve target coordinates for plate-solving from the object/folder name when the header
	// carried none, so SPCC can run on otherwise-unlocatable captures (e.g. ASI lights of M101). A
	// compound folder name ("M81_M82_2020") won't resolve as a whole, so also try each of its tokens —
	// the first catalogued one ("M81") gives the solver a position seed instead of a fragile blind solve.
	if opts.Solve.Coords == "" {
		for _, name := range objectCandidates(object) {
			if c, ok := skycat.Resolve(name, opts.CatalogDir); ok {
				opts.Solve.Coords = c
				break
			}
		}
	}

	res := &Result{
		InputDir: opts.InputDir, OutputDir: outDir, Object: object, RunID: runID,
		Inventory: inv, Detection: inv.ChannelDetection,
	}
	res.Warnings = append(res.Warnings, inv.Warnings...)
	// Surface preset-enabled AI steps whose binary is unreachable up front, so a silent fallback
	// (which leaves the gradient/noise/color uncorrected) is visible rather than looking like a no-op.
	res.Warnings = append(res.Warnings, aiToolWarnings(ctx, opts)...)

	// Record this run in the catalog so its frames become reusable by future runs. Best-effort:
	// a catalog failure must not abort processing. (currentSession is excluded from light reuse.)
	var currentSession int64
	if opts.Catalog != nil {
		if id, perr := opts.Catalog.SaveInventory(ctx, inv); perr != nil {
			res.Warnings = append(res.Warnings, "catalog: could not record session: "+perr.Error())
		} else {
			currentSession = id
		}
	}

	lights := inv.SetsOfType(inspect.Light)
	total := len(lights) + 2 // masters + per-channel + final combine
	step := 0
	progress := func(stepName string) func(siril.Progress) {
		step++
		idx := step
		opts.report(Progress{Step: stepName, Index: idx, Total: total})
		return func(p siril.Progress) {
			opts.report(Progress{Step: stepName, Index: idx, Total: total, Line: p.Line, Sample: p.Sample})
		}
	}

	var masters []calib.Master
	var mWarn []string
	switch {
	case opts.RawCalib != nil && opts.Library != nil:
		libDir, lerr := libraryDir(opts, workAbs)
		if lerr != nil {
			return nil, lerr
		}
		masters, mWarn, err = calib.BuildDeepMasters(ctx, opts.Runner, inv, opts.RawCalib, opts.Library,
			opts.Deep, libDir, workRun, progress("building deep master calibration frames"))
	case opts.Library != nil:
		libDir, lerr := libraryDir(opts, workAbs)
		if lerr != nil {
			return nil, lerr
		}
		masters, mWarn, err = calib.BuildOrReuseMasters(ctx, opts.Runner, inv, opts.Library, libDir, workRun,
			progress("building/reusing master calibration frames"))
	default:
		masters, mWarn, err = calib.BuildMasters(ctx, opts.Runner, inv, mastersDir, workRun,
			progress("building master calibration frames"))
	}
	if err != nil {
		return nil, err
	}
	res.Masters = masters
	res.Warnings = append(res.Warnings, mWarn...)

	gradeOpts := grade.DefaultOptions()
	if opts.Grade != nil {
		gradeOpts = *opts.Grade
	}

	// Fold in prior light frames of the same target (cross-session integration). With no provider or
	// no recorded session this yields only the current session's groups (single-session behavior).
	plan, perr := buildReusePlan(ctx, opts.Reuse, inv, currentSession, targetQueryFor(inv, dominantObject(inv), opts.CatalogDir))
	if perr != nil {
		res.Warnings = append(res.Warnings, "reuse discovery failed (using current session only): "+perr.Error())
	}
	if plan.Summary.PriorFrames > 0 {
		s := plan.Summary
		res.Reuse = &s
		res.Warnings = append(res.Warnings, fmt.Sprintf("reuse: +%d prior frames from %d session(s) folded in",
			s.PriorFrames, s.PriorSessions))
	}
	flats := newFlatCache(opts.Reuse.Provider)
	// Detects + corrects a mirror/parity flip when a session was shot through a different optical train;
	// shared across channels so each session is plate-solved only once.
	parity := newParityCache(opts.Runner, opts.Solve)

	for _, filter := range orderedPlanFilters(plan) {
		groups := plan.byFilter[filter]
		var ch ChannelResult
		if len(groups) == 1 && groups[0].Current { // no prior data → proven single-session path
			set := inspect.Set{Key: groups[0].Key, Frames: groups[0].Frames, Count: len(groups[0].Frames)}
			ch = processChannel(ctx, opts, set, masters, workRun, outDir, gradeOpts,
				progress(fmt.Sprintf("grading + stacking %s %s", object, filter)))
		} else {
			ch = processChannelGroups(ctx, opts, object, filter, groups, masters, flats, parity, workRun, outDir, gradeOpts,
				progress(fmt.Sprintf("grading + stacking %s %s (%d groups)", object, filter, len(groups))))
		}
		res.Channels = append(res.Channels, ch)
		if ch.Err != "" {
			res.Warnings = append(res.Warnings, fmt.Sprintf("channel %s: %s", filter, ch.Err))
		}
		if ch.PreviewPath != "" { // surface per-channel imagery live
			opts.report(Progress{Step: "preview " + filter, Index: step, Total: total, Preview: ch.PreviewPath})
		}
	}

	// Tier-C re-entry for the supervised finish: re-grade + re-stack every channel from the raw frames
	// with a model-tuned preset, then co-register, returning fresh aligned masters. Captures the run's
	// inventory/plan/masters/dirs so a re-stack reuses everything up to the light frames. nil when the
	// agent is off, so the ordinary run pays nothing.
	if superviseEnabled(ctx, opts) {
		opts.Reprocess = func(rctx context.Context, rp *mode.Preset) (map[string]string, error) {
			return reStack(rctx, opts, rp, inv, plan, masters, flats, parity, workRun, outDir, object)
		}
	}

	combine(ctx, opts, res, workRun, outDir, progress("aligning channels + combining"))
	if res.Final != nil {
		for _, o := range res.Final.Outputs {
			if strings.HasSuffix(o, ".png") {
				opts.report(Progress{Step: "final", Index: total, Total: total, Preview: o})
				break
			}
		}
	}
	writeRunJSON(outDir, res) // durable record so any run can be reopened from disk
	return res, nil
}

// writeRunJSON persists the full result (channels, metrics, masters, detection, notes) next to the
// images so a run is self-contained and reopenable independent of the database. Best-effort.
func writeRunJSON(outDir string, res *Result) {
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(outDir, "run.json"), b, 0o644)
}

// filterOrder is the canonical channel order (L first so it is the alignment reference).
var filterOrder = []string{"L", "R", "G", "B", "Ha", "OIII", "SII"}

// channelMastersMap builds the filter→master-path map from a run's successful channels (the
// master_<tag>.fits written next to the run), applying the OIII-as-blue substitution when there is no B
// filter. Shared by combine() and the supervisor's Tier-C re-stack so both co-register the same set.
func channelMastersMap(res *Result, outDir string) map[string]string {
	masters := map[string]string{} // filter -> absolute master path
	for _, ch := range res.Channels {
		if ch.Err == "" && ch.OutputPath != "" && ch.Filter != "" {
			masters[ch.Filter] = filepath.Join(outDir, "master_"+filterTag(ch.Filter)+".fits")
		}
	}
	// No blue filter but OIII present (e.g. an LRG+Ha+OIII narrowband-broadband set): use OIII as the
	// blue channel. OIII (~500 nm) is blue-green, so the broadband L/R/G keep natural star colour while
	// OIII supplies the blue nebulosity — a natural HaLRGB look. The UI filter-mapping can override this.
	if _, hasB := masters["B"]; !hasB {
		if oiii, ok := masters["OIII"]; ok {
			masters["B"] = oiii
			delete(masters, "OIII")
			res.Warnings = append(res.Warnings, "OIII used as the blue channel (no B filter)")
		}
	}
	return masters
}

// combine co-registers the successful per-channel masters, then assembles the final image.
func combine(ctx context.Context, opts Options, res *Result, workRun, outDir string, onProgress func(siril.Progress)) {
	masters := channelMastersMap(res, outDir)
	if len(masters) == 0 {
		res.Warnings = append(res.Warnings, "no channels available to combine")
		return
	}
	channels := alignChannels(ctx, opts.Runner, masters, filepath.Join(workRun, "04_aligned"), outDir, res)
	finishAligned(ctx, opts, channels, res, workRun, outDir, onProgress)
}

// reStack re-grades and re-stacks every planned channel from the raw frames with a model-tuned preset
// (new frame-rejection thresholds / trail mask / denoise / background), overwriting master_<tag>.fits,
// then co-registers into fresh aligned_<tag>.fits and returns the aligned-channel map. It is the
// Tier-C re-entry the supervised finish calls for structural fixes; it reuses the run's inventory,
// reuse plan, calibration masters and caches, so only the light-frame stages re-run. It intentionally
// mirrors Process's channel loop (with a plain "re-stack" progress) rather than the step-counted one.
func reStack(ctx context.Context, opts Options, preset *mode.Preset, inv *inspect.Inventory, plan *ReusePlan,
	masters []calib.Master, flats *flatCache, parity *parityCache, workRun, outDir, object string) (map[string]string, error) {
	ro := opts
	ro.Preset = preset
	g := preset.Grade
	ro.Grade = &g
	prog := func(p siril.Progress) { opts.report(Progress{Step: "re-stack", Line: p.Line, Sample: p.Sample}) }

	rres := &Result{InputDir: opts.InputDir, OutputDir: outDir, Object: object}
	for _, filter := range orderedPlanFilters(plan) {
		groups := plan.byFilter[filter]
		var ch ChannelResult
		if len(groups) == 1 && groups[0].Current {
			set := inspect.Set{Key: groups[0].Key, Frames: groups[0].Frames, Count: len(groups[0].Frames)}
			ch = processChannel(ctx, ro, set, masters, workRun, outDir, g, prog)
		} else {
			ch = processChannelGroups(ctx, ro, object, filter, groups, masters, flats, parity, workRun, outDir, g, prog)
		}
		rres.Channels = append(rres.Channels, ch)
	}
	channels := channelMastersMap(rres, outDir)
	if len(channels) == 0 {
		return nil, fmt.Errorf("re-stack produced no channel masters")
	}
	aligned := alignChannels(ctx, ro.Runner, channels, filepath.Join(workRun, "04_aligned"), outDir, rres)
	if len(aligned) == 0 {
		return nil, fmt.Errorf("re-stack alignment produced no channels")
	}
	return aligned, nil
}

// finishAligned assembles the final image from co-registered channel masters: the optional local-AI-
// agent supervised finish (opt-in), else the layered GIMP composite, else the Siril rgbcomp fallback.
// It sets res.Final. Shared by combine() (a fresh run) and RefineExistingRun (re-tune an existing run).
func finishAligned(ctx context.Context, opts Options, channels map[string]string, res *Result, workRun, outDir string, onProgress func(siril.Progress)) {
	// Optional: local-AI-agent supervised finish (opt-in; GIMP composite path only). Soft-fall to the
	// standard finish on any error so a run never fails because of the agent.
	if superviseEnabled(ctx, opts) {
		if final, err := superviseFinish(ctx, opts, channels, workRun, outDir, res); err != nil {
			res.Warnings = append(res.Warnings, "supervised finish failed, using standard finish: "+err.Error())
		} else {
			res.Final = final
			return
		}
	}

	// Preferred path: layered GIMP composite + curves.
	if opts.Gimp != nil && opts.Preset != nil {
		if err := opts.Gimp.Available(); err != nil {
			res.Warnings = append(res.Warnings, "GIMP unavailable, using Siril finish: "+err.Error())
		} else if final, err := finishWithGimp(ctx, opts, channels, workRun, outDir); err != nil {
			res.Warnings = append(res.Warnings, "GIMP finishing failed, falling back to Siril: "+err.Error())
		} else {
			res.Final = final
			return
		}
	}

	// Fallback: Siril rgbcomp + staged color calibration + finish.
	ppOpts := postprocess.DefaultOptions()
	if opts.Postprocess != nil {
		ppOpts = *opts.Postprocess
	}
	if opts.Preset != nil {
		ppOpts.BackgroundDegree = backgroundDegree(ctx, opts) // 0 when GraXpert handled gradients
		ppOpts.Saturation = opts.Preset.Saturation
		ppOpts.ColorCalibration = opts.Preset.ColorCalibration
		ppOpts.LinkedStretch = opts.Preset.LinkedStretch
	}
	ppOpts.Solve, ppOpts.Spcc = opts.Solve, opts.Spcc
	final, err := postprocess.Combine(ctx, opts.Runner, outDir, channels, "final", ppOpts, onProgress)
	if err != nil {
		res.Warnings = append(res.Warnings, "channel combination failed: "+err.Error())
		return
	}
	res.Final = final
}

// finishWithGimp produces stretched per-component TIFFs with Siril, then composites them into a
// layered image (with curves) in GIMP.
func finishWithGimp(ctx context.Context, opts Options, channels map[string]string, workRun, outDir string) (*postprocess.Result, error) {
	stretchDir := filepath.Join(workRun, "05_stretched")
	if err := fsutil.EnsureDir(stretchDir); err != nil {
		return nil, err
	}
	deg := backgroundDegree(ctx, opts) // 0 when GraXpert already extracted the background
	cc := postprocess.ColorCalOptions{
		Enabled: opts.Preset.ColorCalibration, RemoveGreen: true, Solve: opts.Solve, Spcc: opts.Spcc,
	}
	in, notes, err := prepGimpInputs(ctx, opts, opts.Runner, channels, outDir, stretchDir, deg, cc, opts.Preset.BackgroundLevel, opts.Preset.LinkedStretch)
	if err != nil {
		return nil, err
	}
	in.HaBlack = opts.Preset.HaBlackPoint                // clip the Ha background to black so its red screen lifts only HII knots
	in.ChromaBlur = opts.Preset.ChromaBlur               // colour-only denoise (kills the thin RGB's pink noise; L keeps detail)
	in.LumCurve = opts.Preset.LumCurve                   // brighten the galaxy from the L luminance (not the combined value)
	in.CoreHighlightKnee = opts.Preset.CoreHighlightKnee // roll off the blown nebula core in the L luminance (pre-Ha-screen)
	in.CoreHighlightCeil = opts.Preset.CoreHighlightCeil
	in.CropFrac = opts.Preset.CropFrac             // trim ragged stacking-edge bands off the export
	in.HaExcludeStars = opts.Preset.HaExcludeStars // screen Ha onto nebulosity only when requested
	g, err := gimp.BuildImage(opts.Gimp, in, opts.Preset.Curve, opts.Preset.HaScreen, opts.Preset.Saturation, filepath.Join(outDir, "final"))
	if err != nil {
		return nil, err
	}
	out := &postprocess.Result{
		Mode:     compMode(channels),
		Channels: filterList(channels),
		Outputs:  []string{g.Xcf, g.Tif, g.Png},
		Notes:    append([]string{"layered GIMP composite + curves"}, notes...),
	}

	// Optional: StarNet++ star reduction on the flattened composite (works for any compose mode).
	if aiStars(ctx, opts) {
		extra, note := reduceStarsAI(ctx, opts, g.Tif, outDir, nil)
		out.Outputs = append(out.Outputs, extra...)
		if note != "" {
			out.Notes = append(out.Notes, note)
		} else {
			out.Notes = append(out.Notes, fmt.Sprintf("StarNet++ star reduction (stars at %.0f%%)", opts.Preset.StarReduce*100))
		}
	}
	return out, nil
}

// prepGimpInputs builds the stretched per-component TIFFs GIMP composites. The RGB base is produced
// as a linear, background-extracted image, color-calibrated (SPCC → neutralization fallback), then
// stretched; L and Ha are stretched as luminance/structure layers (no color calibration). Returns
// the GIMP inputs and any color-calibration notes.
func prepGimpInputs(ctx context.Context, opts Options, runner *siril.Runner, channels map[string]string,
	outDir, stretchDir string, deg int, cc postprocess.ColorCalOptions, bgLevel float64, linked bool) (gimp.Inputs, []string, error) {
	has := func(f string) bool { _, ok := channels[f]; return ok }
	rgb := has("R") && has("G") && has("B")
	base := filepath.Join(stretchDir, "base")
	in := gimp.Inputs{Base: base + ".tif", Color: rgb}
	var notes []string
	const hdr = "requires 1.2.0\nsetext fits\n"

	// Ha is screened into the composite, so it gets a darker target background than the base/lum: its
	// pedestal must stay near black or the red screen washes the whole sky (a brown cast). The GIMP
	// black-point clip (preset.HaBlackPoint) is the belt; this is the suspenders.
	haBg := bgLevel * 0.6
	if haBg < 0.03 {
		haBg = 0.03
	}

	if rgb {
		// Linear, background-extracted RGB base → SPCC color calibration → dark, *linked* stretch.
		// Linked keeps SPCC's neutral channel balance (an unlinked stretch re-casts each channel and
		// re-introduces a color tint); the dark target background keeps the sky near-black instead of
		// Siril's washed-out 0.25 default.
		s1 := hdr + fmt.Sprintf("rgbcomp %s %s %s -out=rgb_base\nload rgb_base\n%ssave rgb_base\n",
			channels["R"], channels["G"], channels["B"], siril.SubskyCmd(deg))
		if _, err := runner.Run(ctx, outDir, s1, nil); err != nil {
			return gimp.Inputs{}, nil, err
		}
		// Remove the residual large-scale colour gradient (amp-glow + light pollution) that survives the
		// per-channel extraction and the combine — a 2nd GraXpert pass on the linear combined RGB, before
		// SPCC, so the whole sky is homogeneous. RBF subsky is the deterministic fallback (no GraXpert).
		if n := extractCombinedBackground(ctx, opts, runner, outDir, "rgb_base", hdr); n != "" {
			notes = append(notes, n)
		}
		// AI colour denoise on the combined linear RGB (edge-preserving) — cuts the thin colour's heavy
		// chrominance noise *before* SPCC/stretch amplify it, without smearing star halos (a blur would).
		if opts.Preset != nil && opts.Preset.ColorDenoiseAI && opts.Graxpert != nil && opts.Graxpert.Available(ctx) == nil {
			if n := denoiseAI(ctx, opts, filepath.Join(outDir, "rgb_base.fits"), nil); n != "" {
				notes = append(notes, "colour "+n)
			}
		}
		note, err := postprocess.ColorCalibrate(ctx, runner, outDir, "rgb_base", cc)
		if err != nil {
			return gimp.Inputs{}, nil, err
		}
		if note != "" {
			notes = append(notes, note)
		}
		// SCNR (rmgreen, average-neutral) after SPCC removes the residual green star/sky cast SPCC alone
		// leaves; then a dark linked stretch.
		s2 := hdr + fmt.Sprintf("load rgb_base\nrmgreen 0\n%s\nsavetif %s\n", siril.AutostretchCmd(linked, bgLevel), base)
		if _, err := runner.Run(ctx, outDir, s2, nil); err != nil {
			return gimp.Inputs{}, nil, err
		}
	} else {
		mono := channels[firstFilter(channels)]
		s := hdr + fmt.Sprintf("load %s\n%s%s\nsavetif %s\n", mono, siril.SubskyCmd(deg), siril.AutostretchCmd(false, bgLevel), base)
		if _, err := runner.Run(ctx, outDir, s, nil); err != nil {
			return gimp.Inputs{}, nil, err
		}
	}

	// L and Ha components (stretched for layering; no color calibration).
	var b strings.Builder
	b.WriteString(hdr)
	wrote := false
	if rgb && has("L") {
		lum := filepath.Join(stretchDir, "lum")
		fmt.Fprintf(&b, "load %s\n%s%s\nsavetif %s\n", channels["L"], siril.SubskyCmd(deg), siril.AutostretchCmd(false, bgLevel), lum)
		in.Lum = lum + ".tif"
		wrote = true
	}
	if has("Ha") {
		ha := filepath.Join(stretchDir, "ha")
		fmt.Fprintf(&b, "load %s\n%s%s\nsavetif %s\n", channels["Ha"], siril.SubskyCmd(deg), siril.AutostretchCmd(false, haBg), ha)
		in.Ha = ha + ".tif"
		wrote = true
	}
	if wrote {
		if _, err := runner.Run(ctx, outDir, b.String(), nil); err != nil {
			return gimp.Inputs{}, nil, err
		}
	}
	return in, notes, nil
}

func compMode(channels map[string]string) string {
	has := func(f string) bool { _, ok := channels[f]; return ok }
	rgb := has("R") && has("G") && has("B")
	switch {
	case rgb && has("L") && has("Ha"):
		return "HaLRGB"
	case rgb && has("Ha"):
		return "HaRGB"
	case rgb && has("L"):
		return "LRGB"
	case rgb:
		return "RGB"
	default:
		return "mono"
	}
}

func firstFilter(channels map[string]string) string {
	for _, f := range filterOrder {
		if _, ok := channels[f]; ok {
			return f
		}
	}
	for f := range channels {
		return f
	}
	return ""
}

func filterList(channels map[string]string) []string {
	out := orderedFilters(channels)
	return out
}

// alignChannels co-registers the channel masters to a common reference (Siril global star
// alignment) and returns a filter->basename map (in outDir) for the finishing stage. If alignment
// does not produce one frame per channel, it falls back to the unaligned masters with a warning.
func alignChannels(ctx context.Context, runner *siril.Runner, masters map[string]string,
	alignDir, outDir string, res *Result) map[string]string {
	unaligned := map[string]string{}
	for f := range masters {
		unaligned[f] = "master_" + filterTag(f)
	}
	if len(masters) < 2 {
		return unaligned // single channel: nothing to co-register
	}

	ordered := orderedFilters(masters)
	if err := fsutil.EnsureDir(alignDir); err != nil {
		res.Warnings = append(res.Warnings, "alignment skipped: "+err.Error())
		return unaligned
	}
	for i, f := range ordered {
		link := filepath.Join(alignDir, fmt.Sprintf("%d_%s.fits", i, f))
		_ = removeIfExists(link)
		if err := os.Symlink(masters[f], link); err != nil {
			res.Warnings = append(res.Warnings, "alignment skipped: "+err.Error())
			return unaligned
		}
	}
	if _, err := runner.Run(ctx, alignDir, siril.AlignMastersScript("ch"), nil); err != nil {
		res.Warnings = append(res.Warnings, "cross-channel alignment failed, using unaligned channels: "+err.Error())
		return unaligned
	}

	aligned := map[string]string{}
	for i, f := range ordered {
		reg := filepath.Join(alignDir, fmt.Sprintf("r_ch_%05d.fits", i+1))
		if !fileExists(reg) {
			res.Warnings = append(res.Warnings, "cross-channel alignment incomplete, using unaligned channels")
			return unaligned
		}
		dst := "aligned_" + filterTag(f)
		if err := fsutil.CopyFile(reg, filepath.Join(outDir, dst+".fits")); err != nil {
			res.Warnings = append(res.Warnings, "alignment copy failed, using unaligned channels: "+err.Error())
			return unaligned
		}
		aligned[f] = dst
	}
	return aligned
}

func orderedFilters(masters map[string]string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range filterOrder {
		if _, ok := masters[f]; ok {
			out = append(out, f)
			seen[f] = true
		}
	}
	for f := range masters {
		if !seen[f] {
			out = append(out, f)
		}
	}
	return out
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func removeIfExists(p string) error {
	if _, err := os.Lstat(p); err == nil {
		return os.Remove(p)
	}
	return nil
}

func processChannel(ctx context.Context, opts Options, set inspect.Set, masters []calib.Master,
	workRun, outDir string, gradeOpts grade.Options, onProgress func(siril.Progress)) ChannelResult {
	sel := calib.MatchForLightExcluding(set.Key, masters, opts.CalibExclude)
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

	// Calibrate + register (writes per-frame metrics to the calibrated sequence's .seq), then grade
	// and stack the survivors.
	if _, err := opts.Runner.Run(ctx, seqDir, siril.CalibrateRegisterScript("light", cm), onProgress); err != nil {
		ch.Err = err.Error()
		return ch
	}
	finishStackedChannel(ctx, opts, seqDir, siril.CalibratedSeq("light", cm), siril.RegisteredSeq("light", cm),
		set.Key.Filter, set.Frames, outDir, gradeOpts, "", onProgress, &ch)
	return ch
}

// finishStackedChannel grades a calibrated+registered sequence, stacks the survivors into the channel
// master, then runs the linear-master finishing (AI background extraction, denoise, preview). It is
// shared by the single-session path (processChannel) and the cross-session path (processChannelGroups).
func finishStackedChannel(ctx context.Context, opts Options, seqDir, baseSeq, regSeq, filter string,
	frames []*inspect.Frame, outDir string, gradeOpts grade.Options, stackWeight string,
	onProgress func(siril.Progress), ch *ChannelResult) {
	dropTransition := opts.Preset != nil && opts.Preset.DropFilterWheelTransition
	metrics, rejectedReg, regCount, err := gradeChannel(seqDir, baseSeq, frames, gradeOpts, dropTransition)
	if err != nil {
		ch.Err = fmt.Sprintf("grading: %v", err)
		return
	}
	ch.Metrics = metrics
	ch.StackedFrames = regCount - len(rejectedReg)
	if regCount == 0 {
		ch.Err = "no frames could be registered"
		return
	}

	// Cross-frame transient mask: clean satellite/plane trail segments + cosmic rays from the registered
	// subs before stacking (a slow satellite lands in many subs at marching positions, which the per-frame
	// trail detector can't drop without losing the channel and a normal stack sigma-clip is too loose to
	// remove). Soft-fail: on error, note it and stack the frames as-is.
	if opts.Preset != nil && opts.Preset.TrailMaskK > 0 {
		if note, err := maskChannelTrails(seqDir, regSeq, opts.Preset.TrailMaskK); err != nil {
			ch.Selection.Notes = append(ch.Selection.Notes, "trail mask skipped: "+err.Error())
		} else if note != "" {
			ch.Selection.Notes = append(ch.Selection.Notes, note)
		}
	}

	masterName := "master_" + filterTag(filter) // basename in outDir (Siril CWD)
	outBase := filepath.Join(outDir, masterName)
	if _, err := opts.Runner.Run(ctx, seqDir, siril.StackSelectedScript(regSeq, regCount, rejectedReg, outBase, stackWeight), onProgress); err != nil {
		ch.Err = err.Error()
		return
	}
	ch.OutputPath = outBase + ".fits"

	// Drop any spurious BAYERPAT the stack inherited from older ASICAP mono captures: left in place it
	// makes Siril treat this monochrome master as an undebayered CFA image (a checkerboard) — which
	// breaks the per-channel denoise and, after rgbcomp, the plate-solve that SPCC needs. Safe here: the
	// mono pipeline only ever stacks non-Bayer frames. Soft-fail (cosmetic header edit).
	if err := fits.StripKeyword(ch.OutputPath, "BAYERPAT"); err != nil {
		ch.Selection.Notes = append(ch.Selection.Notes, "BAYERPAT strip skipped: "+err.Error())
	}

	// AI background extraction (GraXpert) on the linear master, replacing Siril's polynomial subsky at
	// finish. Soft-fail: a missing/erroring GraXpert leaves the master untouched.
	if aiBackground(ctx, opts) {
		if note := extractBackgroundAI(ctx, opts, ch.OutputPath, onProgress); note != "" {
			ch.Selection.Notes = append(ch.Selection.Notes, note)
		}
	}
	// Denoise the linear master in place (chroma harder than luminance to keep detail).
	if d := denoiseFor(filter, opts.Preset); d.Enabled() {
		if _, err := opts.Runner.Run(ctx, outDir, siril.DenoiseScript(masterName+".fits", masterName, d), onProgress); err != nil {
			ch.Selection.Notes = append(ch.Selection.Notes, "denoise skipped: "+err.Error())
		}
	}
	// Quick preview PNG for the UI.
	if opts.Preset != nil && opts.Preset.Previews {
		if _, err := opts.Runner.Run(ctx, outDir, siril.PreviewScript(masterName+".fits", masterName+"_preview", 0.5), nil); err == nil {
			ch.PreviewPath = filepath.Join(outDir, masterName+"_preview.png")
		}
	}
	_ = os.RemoveAll(seqDir) // reclaim the bulky calibrated/registered frame copies
}

// denoiseFor returns the denoise options for a channel: luminance keeps detail (often skipped),
// chrominance is denoised harder to cut color noise.
func denoiseFor(filter string, p *mode.Preset) siril.DenoiseOptions {
	if p == nil {
		return siril.DenoiseOptions{}
	}
	mod := p.DenoiseChroma
	if filter == "L" {
		mod = p.DenoiseLum
	}
	return siril.DenoiseOptions{Modulation: mod, VST: p.DenoiseVST, DA3D: p.DenoiseDA3D}
}

// gradeChannel builds per-frame metrics from the calibrated sequence's .seq (1:1 with input
// frames) and the calibrated pixels (trail detection), applies the rejection rules, and maps the
// rejected frames to 1-based indices in the registered sequence used for stacking. Frames Siril
// could not register (zero metrics) are rejected up front and excluded from the registered space.
func gradeChannel(seqDir, baseSeq string, frames []*inspect.Frame, opts grade.Options, dropTransition bool) (
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
		switch {
		case sm.FWHM <= 0: // Siril could not register this frame
			m.Rejected = true
			m.RejectReason = "could not register (too few/elongated stars)"
		default:
			if dropTransition && i < len(frames) && frames[i].WheelTransition {
				m.Rejected = true
				m.RejectReason = "filter-wheel transition (off-brightness first frame)"
			}
			if f, ferr := fits.Open(filepath.Join(seqDir, fmt.Sprintf("%s_%05d.fits", baseSeq, i+1))); ferr == nil {
				if grid, w, h, derr := f.ReadDownsampled(trailDownsample, fits.Max); derr == nil {
					m.TrailDetected, m.TrailScore = grade.DetectTrail(grid, w, h)
				}
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
