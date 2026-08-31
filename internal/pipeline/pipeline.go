// Package pipeline orchestrates the end-to-end deep-sky workflow: inspect a directory, build
// master calibration frames, then for each light channel match the right masters and run
// calibrate → register → stack via Siril. Channel combination lives in package postprocess.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/buildinfo"
	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/dither"
	"github.com/verove-jordan/astronomy/internal/filters"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/gimp"
	"github.com/verove-jordan/astronomy/internal/grade"
	"github.com/verove-jordan/astronomy/internal/graxpert"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/libmirror"
	"github.com/verove-jordan/astronomy/internal/llm"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/mosaic"
	"github.com/verove-jordan/astronomy/internal/noise"
	"github.com/verove-jordan/astronomy/internal/photom"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/skycat"
	"github.com/verove-jordan/astronomy/internal/solar"
	"github.com/verove-jordan/astronomy/internal/stackalg"
	"github.com/verove-jordan/astronomy/internal/stacknative"
	"github.com/verove-jordan/astronomy/internal/starnet"
	"github.com/verove-jordan/astronomy/internal/sysmon"
	"github.com/verove-jordan/astronomy/internal/transient"
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
	FfmpegBin   string               // ffmpeg path for the planetary video/frame source (ProcessPlanetary)
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
	// FinishPriors reads the best prior iterations for the SAME target across jobs — the supervised
	// loop's cross-run memory: it warm-starts the working preset from the best prior pass and tells
	// the model what already scored well. nil → every supervised run starts cold from the preset.
	// PriorObject is the target key (the run's object name); set by the mode entries once known.
	FinishPriors FinishPriorStore
	PriorObject  string
	// Goal is the user's free-text objective for this run (from the run request), folded into the
	// supervisor's FIRST critique as guidance so the agent optimizes for what was actually asked.
	Goal string

	// Reprocess re-runs the stack stages (calibrate → register → grade → stack) from the raw frames
	// with a modified preset and returns fresh aligned-channel masters. It is the Tier-C re-entry the
	// supervised finish uses for structural fixes; nil (a pure refine with no raws) disables Tier C, so
	// the supervisor caps at re-running the finish prep. Set by Process/ProcessOSC.
	Reprocess func(ctx context.Context, preset *mode.Preset) (map[string]string, error)

	// Steer lets the user nudge a supervised finish between iterations: it returns free-text guidance
	// folded into the next critique and a stop flag that ends the loop early keeping the best pass. nil →
	// the loop runs autonomously (CLI/MCP and non-conversation jobs unchanged). Set by the job manager.
	Steer func() (guidance string, stop bool)
	// Confirm blocks the supervised finish before an expensive step (the deep-sky Tier-C re-stack) to ask
	// the user, returning the chosen option; ok=false (unanswered/unavailable) → proceed. nil → no gate
	// (auto-proceed), preserving the autonomous path. Set by the job manager for supervised jobs.
	Confirm func(ctx context.Context, question string, options []string) (choice string, ok bool)

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
	// Stager, when set, supplies the run's inputs on demand from S3 (the low-disk staged mode): Scan
	// builds the inventory without downloading captures, then the pipeline stages one frame-type/channel
	// set at a time and frees it after (bounded peak disk). nil → all inputs are already local (the
	// default; CLI/MCP/host-dev unchanged). See internal/pipeline/stager.go + internal/job/stager.go.
	Stager InputStager

	// MosaicPlan is the saved capture plan a Mode "mosaic" run references (resolved from Postgres by
	// the job manager — the pipeline stays DB-free): panel labels/validation, expected tile centers
	// as plate-solve hints, and the canvas center. nil → panels are auto-detected.
	MosaicPlan *mosaic.Plan

	// LibraryMirror, when set, pulls a matched calibration master back from the S3 library mirror when its
	// file is absent locally (the library is kept as a synced mirror, but a given machine may not hold every
	// file), then frees the transiently-pulled copies after the run. nil → local-only (the default; the
	// library must be on disk). See internal/libmirror + internal/job/libpuller.go.
	LibraryMirror libmirror.Puller

	// steps is the run's named-step progress tracker (set by Process; nil for OSC/refine/CLI and
	// the supervised/star-fix re-entries, which must never advance the main bar). progress_steps.go.
	steps *stepper

	// FilterMapping is an optional user override (detected/known filter → chosen channel; "" or
	// "ignore" excludes it), applied during the scan.
	FilterMapping map[string]string
	// Solve / Spcc are the plate-solve + SPCC inputs for color calibration (from config).
	Solve siril.SolveOptions
	Spcc  siril.SpccOptions
	// TargetHint is the user-declared imaging target — a catalogue name ("M66", "NGC 3628") or an
	// explicit "RA,Dec" position — tried ahead of any header/folder-derived resolution when seeding
	// the plate-solve (and therefore SPCC). Optional; naming of the run is never derived from it.
	TargetHint string
	// DenoiseScale (0,1) runs the joint AI colour denoise on a downscaled copy and transfers only
	// the chroma back (ASTRO_DENOISE_SCALE; ≤0 or ≥1 → the byte-identical full-resolution pass).
	DenoiseScale float64
	// ChannelParallel stacks up to N channels concurrently (ASTRO_CHANNEL_PARALLEL; ≤1 → the
	// byte-identical serial loop). Forced serial in low-disk staged mode. See parallel.go.
	ChannelParallel int
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
	// ExcludeSets holds canonical inspect.SetKey.ID tokens the user chose to drop in the Import
	// stray-light check; those whole light sets are removed from the scan before grouping/stacking.
	ExcludeSets []string
	// ForceCalibration relaxes the calibration matcher's gain/offset/bin, exposure and sensor-temperature
	// gates so the available dark/flat/bias masters are applied to the lights even when they don't match
	// (the Import "force these calibration frames" toggle). false → the strict, physically-matched default.
	ForceCalibration bool

	// Resume, when set, continues a previously paused run: the run reuses Resume.RunID/OutDir so the
	// per-channel masters already stacked on disk are found and those channels skipped. nil → a fresh run.
	Resume *ResumeState
	// PauseRequested lets the job layer ask a run to pause cooperatively at the next channel boundary;
	// when it returns true the run stops and returns a *PausedError. nil → never pauses (CLI/MCP unchanged).
	PauseRequested func() bool
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
	CreateFinishIteration(ctx context.Context, jobID int64, iter int, tier string, params, metrics, defects []byte, detScore, modelScore, combined float64, reasoning, pngPath string, preset []byte) (int64, error)
	MarkFinishIterationChosen(ctx context.Context, id int64) error
}

// PriorIteration is one prior supervised pass for a target — the cross-run memory record. DB-free
// (the job manager adapts the store rows); Preset is the versioned full working preset JSON.
type PriorIteration struct {
	JobID     int64
	Tier      string
	Combined  float64
	Det       float64
	Reasoning string
	PngPath   string
	Preset    json.RawMessage
}

// FinishPriorStore reads the best prior supervised iterations for a target across all jobs (the
// warm-start memory), best combined score first.
type FinishPriorStore interface {
	BestFinishIterations(ctx context.Context, object, kind string, minDet float64, limit int) ([]PriorIteration, error)
}

// libraryDir resolves the persistent master-library directory (absolute), defaulting under workAbs.
func libraryDir(opts Options, workAbs string) (string, error) {
	dir := opts.LibraryDir
	if dir == "" {
		dir = filepath.Join(workAbs, "library")
	}
	return filepath.Abs(dir)
}

// ensureMasters pulls the given matched-master files back from the S3 library mirror if they are absent
// locally (no-op when no mirror is configured, or a path is empty / already present). Called at each
// calibration match site right before Siril reads the masters.
func (o Options) ensureMasters(ctx context.Context, paths []string) {
	if o.LibraryMirror != nil {
		_ = o.LibraryMirror.Ensure(ctx, paths)
	}
}

// freePulledMasters frees the master files the mirror transiently downloaded this run (deferred by Process).
func (o Options) freePulledMasters(ctx context.Context) {
	if o.LibraryMirror != nil {
		o.LibraryMirror.FreePulled(ctx)
	}
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
	// Session attributes the event to one capture night ("YYYY-MM-DD") inside a cross-session
	// channel step — the UI's per-night progress rows key on it. "" = run-level (unchanged).
	Session string `json:"session,omitempty"`
	// Photom streams one group's photometric-normalization record the moment NormalizeGroups
	// produced it, so the job UI shows the ×scale/offset chips live (they also land in run.json).
	Photom *photom.GroupRecord `json:"photom,omitempty"`
	// Iteration carries one completed supervised-finish pass as it happens, so the UI can stream the
	// agent's iterations (preview + defects + scores) live instead of only after the job finishes.
	Iteration *postprocess.IterationRecord `json:"iteration,omitempty"`
	// StagePreview carries one saved processing-milestone preview (stacked/aligned/combined/finish…) as it
	// is produced, so the UI accumulates a labeled timeline rather than only the latest live preview.
	StagePreview *postprocess.StagePreview `json:"stage_preview,omitempty"`
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
	// Photom records the per-group photometric normalization applied before a heterogeneous merge
	// (different exposure/gain/temperature sessions), so run.json shows how each group was scaled.
	Photom []photom.GroupRecord `json:"photom,omitempty"`
	// Groups is the per-night/per-session provenance of a cross-session merge: which masters actually
	// calibrated each group, the flat's origin, the parity flip, the normalization record and the
	// before/after previews. Populated ONLY by the grouped path — a single-session channel (the fast
	// path) never sets it, so its run.json stays byte-identical.
	Groups []GroupResult `json:"groups,omitempty"`
	// Noise is the measured sky noise/SNR of the linear master before and after denoising, so the UI
	// and supervisor can see how much noise was present and how much the denoiser removed.
	Noise *noise.Summary `json:"noise,omitempty"`
	// TrailMask summarizes the line-aware satellite/aircraft-trail masking applied before stacking.
	TrailMask *transient.Summary `json:"trail_mask,omitempty"`
	// Dither classifies the capture-time pointing pattern (dithered / drift / static) from the
	// registration offsets — the walking-noise risk diagnosis and its dithering advice.
	Dither *dither.Report `json:"dither,omitempty"`
	// Warnings are channel-scoped soft-failures (partial registration, restored frames, lone-frame
	// master); each is also emitted to the live journal when it happens and promoted to the run's
	// warning list by recordChannelOutcome.
	Warnings []string `json:"warnings,omitempty"`
	Err      string   `json:"error,omitempty"`
	// CoveredFrac is the fraction of the anchor canvas this channel's STACKED frames cover at the
	// preset's minimum depth; CoverageMask is the grayscale thumbnail of that coverage. Grouped
	// (multi-night) channels only — the inputs of the coverage-aware combine crop.
	CoveredFrac  float64 `json:"covered_frac,omitempty"`
	CoverageMask string  `json:"coverage_mask,omitempty"`
	// coverage is the in-process grid behind CoveredFrac (not serialized; run.json carries the
	// summary + PNG instead — 250k cells have no business in a JSON row).
	coverage *coverageGrid
	// Seam records the multi-night seam repair applied to this channel (overlap offset refit +
	// coverage-weighted noise equalization) — provenance for every correction and every skip.
	Seam *SeamRepair `json:"seam,omitempty"`
	// Canvas is the mosaic union-canvas geometry of this channel's master (Preset.Mosaic grouped
	// runs only): dims + the anchor frame's origin offset — the coordinate key for the coverage
	// grid, the sky fill and the cross-channel padding.
	Canvas *CanvasInfo `json:"canvas,omitempty"`
	// MosaicFill records the sky fill of the union's never-covered margins (mosaic runs only).
	MosaicFill *FillRecord `json:"mosaic_fill,omitempty"`
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
	// AnchorNight is the capture night whose canvas every channel master was registered onto (grouped
	// multi-night/reuse runs only — absent for single-session runs).
	AnchorNight string `json:"anchor_night,omitempty"`
	// CombineCrop records the coverage-derived crop applied to the colour-combine inputs (grouped
	// runs with CoverageCrop on): the common covered rectangle, or the honest union fallback.
	CombineCrop *CombineCrop `json:"combine_crop,omitempty"`
	// EdgeCrop records the ragged-stacking-edge trim of the colour-combine inputs (edgecrop.go),
	// measured from the stack's own pixels rather than from registration geometry — so it applies to
	// single-session runs, which carry no coverage grid. Absent when nothing needed cutting.
	EdgeCrop *CombineCrop `json:"edge_crop,omitempty"`
	// MosaicAssembly records a Mode "mosaic" run's panel assembly (segmentation, per-panel solves,
	// photometric matching, canvas + seam metrics). See mosaicassemble.go.
	MosaicAssembly *MosaicResult `json:"mosaic_assembly,omitempty"`
	// Bracket records a Mode "sun" run's exposure composite: how far apart the tiers were measured to
	// be, and how much of the finished master each one actually contributed. Absent for a session
	// shot at a single exposure. See solar/bracket.go.
	Bracket []solar.TierReport `json:"bracket,omitempty"`
	// PSF is what the finished solar master actually resolved, measured off its limb — the width the
	// run deconvolved at, and whether the camera had already sharpened the frames. It is the number
	// that says whether a session was worth the sharpening it was given. See solar/psf.go.
	PSF      *solar.PSF `json:"psf,omitempty"`
	Warnings []string   `json:"warnings"`
	// Engine identifies the build that produced this run (buildinfo; "dev" for un-stamped binaries) —
	// so a result from a stale Docker engine is identifiable instead of masquerading as current code.
	Engine string `json:"engine,omitempty"`
	// StagePreviews are the saved milestone preview PNGs (stacked/aligned/combined/finish…), reconstructed
	// from the run's previews/ dir so the UI shows the processing timeline after a reload.
	StagePreviews []postprocess.StagePreview `json:"stage_previews,omitempty"`
	// Options records the resolved algorithmic configuration of this run (mode + the finish/quality
	// toggles), so run.json is a durable provenance record for comparing runs even after the job row is
	// gone. Storage/agent flags are job-level (jobs.params) and not visible here.
	Options *RunOptions `json:"options,omitempty"`
	// Timings is the per-step wall-time breakdown ("where did the two hours go?"), in step order.
	Timings []StepTiming `json:"timings,omitempty"`
}

// RunOptions is the compact, resolved algorithmic configuration of a run (from the preset), persisted
// into run.json for run-to-run comparison.
type RunOptions struct {
	Mode             string  `json:"mode,omitempty"`
	ColorCalibration bool    `json:"color_calibration,omitempty"`
	Denoise          bool    `json:"denoise,omitempty"`
	HaExcludeStars   bool    `json:"ha_exclude_stars,omitempty"`
	HaContinuumSub   bool    `json:"ha_continuum_sub,omitempty"`
	DropTransition   bool    `json:"drop_transition,omitempty"`
	BackgroundAI     bool    `json:"background_ai,omitempty"`
	StarReduce       float64 `json:"star_reduce,omitempty"`
	StackWeight      string  `json:"stack_weight,omitempty"`
	// How the pixels were combined. Recorded because `params` alone cannot answer "which algorithm
	// produced this master?" when the choice came from the mode default or a preset rather than
	// from a knob the user typed. StackReject is "auto" when the count-adaptive rule chose per
	// channel — the per-channel clause is in the Siril log.
	StackEngine     string `json:"stack_engine,omitempty"`
	StackCombine    string `json:"stack_combine,omitempty"`
	StackReject     string `json:"stack_reject,omitempty"`
	StackNorm       string `json:"stack_norm,omitempty"`
	Mosaic          bool   `json:"mosaic,omitempty"`
	MosaicFill      string `json:"mosaic_fill,omitempty"`
	SeamOffsetRefit bool   `json:"seam_offset_refit,omitempty"`
	SeamNoiseEq     bool   `json:"seam_noise_eq,omitempty"`
	Palette         string `json:"palette,omitempty"`
	LuminanceMono   bool   `json:"luminance_mono,omitempty"`
	AllChannelMono  bool   `json:"all_channel_mono,omitempty"`
}

// runOptionsFrom snapshots the resolved preset toggles into a RunOptions (nil when there is no preset).
func runOptionsFrom(p *mode.Preset) *RunOptions {
	if p == nil {
		return nil
	}
	return &RunOptions{
		Mode:             string(p.Mode),
		ColorCalibration: p.ColorCalibration,
		Denoise:          p.DenoiseChroma > 0 || p.DenoiseLum > 0,
		HaExcludeStars:   p.HaExcludeStars,
		HaContinuumSub:   p.HaContinuumSub,
		Mosaic:           p.Mosaic,
		MosaicFill:       p.MosaicFill,
		SeamOffsetRefit:  p.SeamOffsetRefit,
		SeamNoiseEq:      p.SeamNoiseEq,
		DropTransition:   p.DropFilterWheelTransition,
		BackgroundAI:     p.BackgroundAI,
		StarReduce:       p.StarReduce,
		StackWeight:      p.StackWeight,
		StackEngine:      displayEngine(p.Stack.Engine),
		StackCombine:     displayCombine(p.Stack.Combine),
		StackReject:      displayReject(p.Stack.Reject),
		StackNorm:        displayNorm(p.Stack.Norm),
		Palette:          p.Palette,
		LuminanceMono:    p.EmitLuminanceMono,
		AllChannelMono:   p.EmitAllChannelMono,
	}
}

// Process runs the full pipeline and returns its result. Per-channel failures are recorded as
// warnings/channel errors rather than aborting the whole run.
func Process(ctx context.Context, opts Options) (*Result, error) {
	if err := opts.Runner.Available(ctx); err != nil {
		return nil, fmt.Errorf("siril unavailable: %w", err)
	}
	applyMosaicConstraints(opts.Preset) // a mosaic run must keep the union: coverage crop/trim off
	// Per-step wall-time accounting: observe every progress event's step (installed before anything
	// reports, so the whole run is covered) — lands in run.json + one final "timing:" summary.
	timer := newStepTimer(nil)
	if inner := opts.OnProgress; inner != nil {
		opts.OnProgress = func(p Progress) { timer.observe(p.Step); inner(p) }
	} else {
		opts.OnProgress = func(p Progress) { timer.observe(p.Step) }
	}
	defer opts.freePulledMasters(ctx) // discard any masters pulled from the S3 library mirror this run
	scanOpts := inspect.DefaultScanOptions()
	scanOpts.FilterMapping = opts.FilterMapping
	scanOpts.ExcludeSets = opts.ExcludeSets
	inv, err := opts.scanInputs(ctx, scanOpts) // remote (no downloads) when a low-disk Stager is set, else local
	if err != nil {
		return nil, err
	}
	// One-shot-color captures stack through this same pipeline as a SINGLE channel named RGB (inspect
	// names it; see nameColorChannel), which is what lets colour inherit the calibration library,
	// grading, trail masking, plate-solving, SPCC and the whole finish rather than needing a parallel
	// implementation. Only a MIXED folder still drops frames: nothing can stack mono and colour lights
	// together, so the mono session wins and the colour frames are reported rather than silently lost.
	osc := inv.ColorModel == inspect.ColorOSC
	if inv.ColorModel == inspect.ColorMixed {
		if n := inv.ExcludeColor(); n > 0 {
			inv.Warnings = append(inv.Warnings, fmt.Sprintf(
				"%d one-shot-color frame(s) excluded — this folder also holds monochrome lights, and one "+
					"run cannot stack both; process the colour frames from their own folder", n))
		}
	}
	if osc {
		markColorPreset(opts.Preset)
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
	// On resume, reuse the paused run's id + output dir so the per-channel masters it already stacked
	// (output/<object>/<run_id>/master_<tag>.fits) are found and skipped; a fresh run stamps a new id.
	runID := time.Now().Format("20060102_150405")
	if opts.Resume != nil && opts.Resume.RunID != "" {
		runID = opts.Resume.RunID
	}
	workRun := filepath.Join(workAbs, "run_"+runID)
	object := sanitize(dominantObject(inv))
	if object == "session" { // no OBJECT header — name the run after the target folder (e.g. M101)
		if base := smartObject(opts.InputDir); base != "session" {
			object = base
		}
	}
	outDir := filepath.Join(outAbs, object, runID)
	if opts.Resume != nil && opts.Resume.OutDir != "" {
		outDir = opts.Resume.OutDir
	}
	if err := fsutil.EnsureDir(outDir); err != nil {
		return nil, err
	}

	res := &Result{
		InputDir: opts.InputDir, OutputDir: outDir, Object: object, RunID: runID,
		Inventory: inv, Detection: inv.ChannelDetection,
		Options: runOptionsFrom(opts.Preset),
	}
	opts.PriorObject = object // key for the supervisor's cross-run memory (warm start)
	res.Warnings = append(res.Warnings, inv.Warnings...)
	// Resolve the plate-solve position seed (explicit target > object-name tokens > input-folder path
	// segments) so SPCC can run on otherwise-unlocatable captures — without a seed Siril's internal
	// solver hard-fails (blind solving needs local astrometry.net) and the colour ladder degrades to
	// the star-field fallback (task #316's green stars).
	if coords, source := resolveSolveCoords(&opts, object); coords != "" {
		opts.Solve.Coords = coords
		opts.report(Progress{Line: fmt.Sprintf("target position %s (from %s)", coords, source)})
	} else if opts.TargetHint != "" {
		warnLive(opts, res, fmt.Sprintf("target %q not found in the catalogues — plate-solve/SPCC run without a position seed", opts.TargetHint))
	}
	// Surface preset-enabled AI steps whose binary is unreachable up front — live, so the fallback
	// (which leaves the gradient/noise/color uncorrected) is visible at launch rather than looking
	// like a no-op discovered in the final report.
	for _, w := range aiToolWarnings(ctx, opts) {
		warnLive(opts, res, w)
	}

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

	// One named step per stage: masters, each channel, then the preset-derived finish plan — the
	// whole finish tail used to hide inside a single 92%-pinned step (see progress_steps.go). The
	// channel slots count light sets with the capture night normalized OUT: a multi-night split
	// stacks its per-night groups inside ONE channel step, so counting split sets would leave the
	// bar permanently undershooting; a single-night run keeps the historical count exactly.
	opts.steps = newStepper(opts.report, lightStepSlots(inv)+1+len(finishStepPlan(opts)))
	defer opts.finishSteps()
	progress := opts.beginStep

	// Low-disk staged calibration: download this run's calib frames, build the masters (kept in the small
	// local library), then verified-free the calib raws before the light waves. Prior-session calib stays
	// on S3 (freed) and is dropped by dropMissing, so the masters build from the staged frames alone.
	if opts.Stager != nil {
		if paths := calibStagePaths(inv); len(paths) > 0 {
			if serr := opts.Stager.Ensure(ctx, "calibration frames", paths); serr != nil {
				return nil, &StagePullError{RunID: runID, OutDir: outDir, Err: serr}
			}
		}
	}
	masters, mWarn, err := buildRunMasters(ctx, opts, inv, workRun, workAbs, progress)
	if err != nil {
		return nil, err
	}
	res.Masters = masters
	res.Warnings = append(res.Warnings, mWarn...)
	if opts.Stager != nil {
		opts.Stager.Free(ctx, "calibration frames", calibStagePaths(inv)) // non-fatal; masters are built
	}

	gradeOpts := grade.DefaultOptions()
	if opts.Grade != nil {
		gradeOpts = *opts.Grade
	}

	// Fold in prior light frames of the same target (cross-session integration). With no provider or
	// no recorded session this yields only the current session's groups (single-session behavior).
	tq := targetQueryFor(inv, dominantObject(inv), opts.CatalogDir)
	if tq.HasCoords && opts.Solve.Coords == "" {
		// Stacked channel masters lose the capture headers' OBJCTRA/DEC, so downstream probes
		// (master parity, SPCC's solve) get the resolved target as an explicit hint — without it
		// task #312 logged "channel parity check skipped (no master could be plate-solved)".
		opts.Solve.Coords = fmt.Sprintf("%.5f,%.5f", tq.RADeg, tq.DecDeg)
	}
	plan, perr := buildReusePlan(ctx, opts.Reuse, inv, currentSession, tq)
	if perr != nil {
		res.Warnings = append(res.Warnings, "reuse discovery failed (using current session only): "+perr.Error())
	}
	if plan.Summary.PriorFrames > 0 {
		s := plan.Summary
		res.Reuse = &s
		res.Warnings = append(res.Warnings, fmt.Sprintf("reuse: +%d prior frames from %d session(s) folded in",
			s.PriorFrames, s.PriorSessions))
	}
	if plan.MissingPrior > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"reuse: skipped %d catalogued prior frame(s) missing on disk (freed to S3?)", plan.MissingPrior))
	}
	if plan.Anchored && plan.AnchorNight != "" {
		res.AnchorNight = plan.AnchorNight
		opts.sessionLine(stepRef{}, plan.AnchorNight, fmt.Sprintf(
			"anchor night %s — every channel stacks on its field (%d night(s) merged)",
			plan.AnchorNight, len(plan.Nights)))
	}
	flats := newFlatCache(opts.Reuse.Provider)
	// Detects + corrects a mirror/parity flip when a session was shot through a different optical train;
	// shared across channels so each session is plate-solved only once.
	parity := newParityCache(opts.Runner, opts.Solve)

	// Channels stack in waves of `parallel` (default 1 = the proven serial loop, byte-identical;
	// ASTRO_CHANNEL_PARALLEL opts into concurrent waves — see parallel.go).
	filters := orderedPlanFilters(plan)
	parallel := opts.channelParallelism(len(filters))
	for waveStart := 0; waveStart < len(filters); waveStart += parallel {
		wave := filters[waveStart:min(waveStart+parallel, len(filters))]
		results := make([]ChannelResult, len(wave))
		reusedFromDisk := make([]bool, len(wave))
		var pending []int

		for i, filter := range wave {
			// Resume: a channel already stacked in a prior (paused) attempt is reused from disk,
			// skipping the expensive calibrate+register+stack.
			if reused, ok := reuseStackedChannel(opts, object, filter, outDir); ok {
				results[i], reusedFromDisk[i] = reused, true
				if reused.PreviewPath != "" {
					idx, tot := opts.stepPos()
					opts.report(Progress{Step: "reused " + filter, Index: idx, Total: tot, Preview: reused.PreviewPath})
					capturePreview(ctx, opts, outDir, ordStacked+waveStart+i, stageStacked, filter, reused.PreviewPath, false)
				}
				continue
			}
			// Low-disk staged light wave: download only this channel's current-session frames (all gain
			// groups) just before stacking it (staged mode forces parallel == 1, so this stays a
			// one-channel-at-a-time download). Freed after the stack + preview, before the pause boundary.
			if opts.Stager != nil {
				if paths := currentGroupPaths(plan, filter); len(paths) > 0 {
					if serr := opts.Stager.Ensure(ctx, filter+" lights", paths); serr != nil {
						return nil, &StagePullError{RunID: runID, OutDir: outDir, Err: serr}
					}
				}
			}
			pending = append(pending, i)
		}

		if parallel == 1 {
			for _, i := range pending {
				label := channelStepLabel(plan, object, wave[i])
				prog := progress(label)
				idx, tot := opts.stepPos() // beginStep just advanced the cursor to this channel's slot
				results[i] = stackOneChannel(ctx, opts, plan, object, wave[i], masters, flats, parity,
					workRun, outDir, gradeOpts, prog, stepRef{Name: label, Index: idx, Total: tot})
			}
		} else if len(pending) > 0 {
			runParallelWave(ctx, opts, plan, object, wave, pending, waveStart, results, masters, flats, parity,
				workRun, outDir, gradeOpts)
		}

		for i, filter := range wave {
			ch := results[i]
			if reusedFromDisk[i] { // already surfaced above; reused channels carry no fresh warnings
				res.Channels = append(res.Channels, ch)
				continue
			}
			recordChannelOutcome(opts, res, filter, ch)
			if ch.PreviewPath != "" { // surface per-channel imagery live
				idx, tot := opts.stepPos()
				opts.report(Progress{Step: "preview " + filter, Index: idx, Total: tot, Preview: ch.PreviewPath})
				// Milestone timeline: the stacked+extracted master for this channel (copy the ready PNG).
				capturePreview(ctx, opts, outDir, ordStacked+waveStart+i, stageStacked, filter, ch.PreviewPath, false)
			}
			// The channel master is on disk now, so this channel's raws are dead weight — verified-free
			// them (the master + aligned_* survive to combine/finish/resume; only freed raws leave disk).
			if opts.Stager != nil {
				opts.Stager.Free(ctx, filter+" lights", currentGroupPaths(plan, filter))
			}
		}

		// Cooperative pause boundary between waves: stop with the channels done so far persisted on
		// disk. Continue re-enters, reuses them (reuseStackedChannel is idempotent), finishes the rest.
		if opts.pauseRequested() {
			return nil, &PausedError{RunID: runID, OutDir: outDir}
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

	appendDitherAdvice(res)
	combine(ctx, opts, res, workRun, outDir)
	progress("export")
	if res.Final != nil {
		for _, o := range res.Final.Outputs {
			if strings.HasSuffix(o, ".png") {
				idx, tot := opts.stepPos()
				opts.report(Progress{Step: "final", Index: idx, Total: tot, Preview: o})
				capturePreview(ctx, opts, outDir, ordFinal, stageFinal, "", o, false) // milestone: the final image
				break
			}
		}
	}
	res.StagePreviews = collectStagePreviews(outDir) // persist the milestone timeline for reload
	if opts.Stager != nil {                          // low-disk staging summary (sets staged, bytes downloaded/freed, peak local)
		res.Warnings = append(res.Warnings, opts.Stager.Notes()...)
	}
	// Checkpoint the preset behind these artifacts so a later param edit can re-enter at the cheapest
	// stage (rerun.go). Best-effort — a rerun falls back to the job's stored params without it.
	if merr := writeStageManifest(outDir, opts.Preset, runID); merr != nil {
		res.Warnings = append(res.Warnings, "stage checkpoint not written: "+merr.Error())
	}
	opts.finishSteps() // close the export step's ✓ line before the timing summary
	res.Timings = timer.finish()
	if line := timingSummary(res.Timings); line != "" {
		opts.report(Progress{Line: line})
	}
	writeRunJSON(outDir, res) // durable record so any run can be reopened from disk
	if err := combineFailure(ctx, res); err != nil {
		return res, err
	}
	return res, nil
}

// combineFailure turns a no-final multi-channel run into a job failure. Task #312 combined
// nothing (mixed-dimension masters killed rgbcomp) yet finished "Réussi" — every artifact and the
// stage checkpoint are still persisted above (a per-stage rerun can re-enter the combine), only
// the status becomes honest. A single stacked channel keeps its semantics (the master IS the
// deliverable), and a cancelled run stays a cancellation, not a failure.
func combineFailure(ctx context.Context, res *Result) error {
	if ctx.Err() != nil || res.Final != nil {
		return nil
	}
	stacked := 0
	for _, ch := range res.Channels {
		if ch.Err == "" && ch.OutputPath != "" {
			stacked++
		}
	}
	// Zero stacked masters is a failure regardless of channel count: a disk-full run once zeroed
	// every channel and still finished "succeeded" — name the first per-channel error when one
	// was recorded.
	if stacked == 0 && len(res.Channels) > 0 {
		for _, ch := range res.Channels {
			if ch.Err != "" {
				return fmt.Errorf("no channel produced a stacked master (%s: %s)", ch.Filter, ch.Err)
			}
		}
		return fmt.Errorf("no channel produced a stacked master — see the run warnings")
	}
	if stacked < 2 {
		return nil
	}
	cause := "no final image was produced"
	for _, w := range res.Warnings {
		if strings.HasPrefix(w, "channel combination failed: ") {
			cause = w
		}
	}
	return fmt.Errorf("combining %d stacked channels produced no final image: %s", stacked, cause)
}

// writeRunJSON persists the full result (channels, metrics, masters, detection, notes) next to the
// images so a run is self-contained and reopenable independent of the database. Best-effort — but
// never silently: a marshal failure means a non-finite float somewhere in res (encoding/json
// refuses NaN/±Inf), which is zeroed with a warning naming the fields. Task #353 succeeded with no
// run.json AND an empty stored job result because both persist paths swallowed that same error.
// Sanitizing here also repairs the manager's later resultBlob marshal (same res pointer).
func writeRunJSON(outDir string, res *Result) {
	res.Engine = buildinfo.String()
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		if paths := SanitizeNonFinite(res); len(paths) > 0 {
			res.Warnings = append(res.Warnings, nonFiniteNote(paths))
			b, err = json.MarshalIndent(res, "", "  ")
		}
		if err != nil { // not a float problem — leave a reopenable stub naming the failure
			b = []byte(fmt.Sprintf("{\n  \"warnings\": [%q]\n}\n", "run record not serializable: "+err.Error()))
		}
	}
	_ = os.WriteFile(filepath.Join(outDir, "run.json"), b, 0o644)
}

// buildRunMasters builds (or reuses) the master calibration frames for a run, dispatching on the
// configured calibration inputs: raw-frame deep masters, a persistent stacked-master library, or a
// plain in-run masters dir. Shared by Process and the Tier-C rerun's stack-context reconstruction
// (reconstructStackContext) so both calibrate a run identically.
func buildRunMasters(ctx context.Context, opts Options, inv *inspect.Inventory, workRun, workAbs string,
	progress func(string) func(siril.Progress)) ([]calib.Master, []string, error) {
	switch {
	case opts.RawCalib != nil && opts.Library != nil:
		libDir, err := libraryDir(opts, workAbs)
		if err != nil {
			return nil, nil, err
		}
		return calib.BuildDeepMasters(ctx, opts.Runner, inv, opts.RawCalib, opts.Library,
			opts.Deep, libDir, workRun, opts.masterStacks(),
			progress("building deep master calibration frames"))
	case opts.Library != nil:
		libDir, err := libraryDir(opts, workAbs)
		if err != nil {
			return nil, nil, err
		}
		return calib.BuildOrReuseMasters(ctx, opts.Runner, inv, opts.Library, libDir, workRun,
			opts.masterStacks(), progress("building/reusing master calibration frames"))
	default:
		return calib.BuildMasters(ctx, opts.Runner, inv, filepath.Join(workRun, "masters"), workRun,
			opts.masterStacks(), progress("building master calibration frames"))
	}
}

// filterOrder is the canonical channel order (L first so it is the alignment reference).
var filterOrder = filters.Canonical

// channelMastersMap builds the filter→master-path map from a run's successful channels (the
// master_<tag>.fits written next to the run), applying the OIII-as-blue substitution when there is no B
// filter. Shared by combine() and the supervisor's Tier-C re-stack so both co-register the same set.
func channelMastersMap(res *Result, outDir string, substOIII bool) map[string]string {
	masters := map[string]string{} // filter -> absolute master path
	for _, ch := range res.Channels {
		if ch.Err == "" && ch.OutputPath != "" && ch.Filter != "" {
			masters[ch.Filter] = filepath.Join(outDir, "master_"+filterTag(ch.Filter)+".fits")
		}
	}
	// No blue filter but OIII present (e.g. an LRG+Ha+OIII narrowband-broadband set): use OIII as the
	// blue channel. OIII (~500 nm) is blue-green, so the broadband L/R/G keep natural star colour while
	// OIII supplies the blue nebulosity — a natural HaLRGB look. Skipped for a narrowband palette, which
	// consumes OIII as its own base channel (resolvePalette needs it to stay distinct).
	if substOIII {
		if _, hasB := masters["B"]; !hasB {
			if oiii, ok := masters["OIII"]; ok {
				masters["B"] = oiii
				delete(masters, "OIII")
				res.Warnings = append(res.Warnings, "OIII used as the blue channel (no B filter)")
			}
		}
	}
	return masters
}

// combine co-registers the successful per-channel masters, then assembles the final image.
func combine(ctx context.Context, opts Options, res *Result, workRun, outDir string) {
	masters := channelMastersMap(res, outDir, paletteWantsOIIIAsBlue(opts.Preset))
	if len(masters) == 0 {
		warnLive(opts, res, "no channels available to combine")
		return
	}
	// Mosaic: land every channel on the cross-channel common union canvas (pad in anchor
	// coordinates), then crop to the all-channel interior or keep the sky-filled union.
	masters = mosaicHarmonize(opts, res, masters, outDir)
	channels := alignChannels(ctx, opts, masters, filepath.Join(workRun, "04_aligned"), outDir, res,
		opts.beginStep("aligning channels"))
	// Coverage-aware crop of the combine inputs (grouped multi-night runs): every channel is cut to
	// the field they ALL cover, so the colour combine never mixes regions where one channel has no
	// data (regional casts) or none has (black wedges). Masters/aligned files stay untouched.
	channels = applyCoverageCrop(opts, res, channels, outDir)
	// Then the stack's own ragged edge, measured from the pixels. Complementary to the coverage
	// crop, not a duplicate of it: coverage knows where the frames landed, this knows where the
	// finished stack stops being sky — which reaches further (measured: 135 px of drift left a
	// 200 px skirt). A single-session run has only this one.
	channels = applyEdgeCrop(opts, res, channels, outDir)
	finishAligned(ctx, opts, channels, res, workRun, outDir)
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
	ro.steps = nil // a nested re-stack must never advance the main run's step bar
	g := preset.Grade
	ro.Grade = &g
	prog := func(p siril.Progress) { opts.report(Progress{Step: "re-stack", Line: p.Line, Sample: p.Sample}) }

	rres := &Result{InputDir: opts.InputDir, OutputDir: outDir, Object: object}
	for _, filter := range orderedPlanFilters(plan) {
		// A Tier-C re-stack needs this channel's raw frames, which low-disk mode freed after the first
		// stack — re-download them on demand (per the low-disk Tier-C policy), then free again after. A
		// pull failure returns an error the supervised loop soft-falls from (it drops back to a cheaper tier).
		if ro.Stager != nil {
			if paths := currentGroupPaths(plan, filter); len(paths) > 0 {
				if serr := ro.Stager.Ensure(ctx, filter+" lights (re-stack)", paths); serr != nil {
					return nil, fmt.Errorf("re-stack: stage %s lights: %w", filter, serr)
				}
			}
		}
		groups := plan.byFilter[filter]
		var ch ChannelResult
		if useFastPath(plan, groups) {
			set := inspect.Set{Key: groups[0].Key, Frames: groups[0].Frames, Count: len(groups[0].Frames)}
			ch = processChannel(ctx, ro, set, masters, workRun, outDir, g, prog)
		} else {
			// Zero-position stepRef: a nested re-stack must never advance the main run's bar; its
			// per-session lines ride at the last position exactly like its other index-less lines.
			ch = processChannelGroups(ctx, ro, object, filter, groups, masters, flats, parity, workRun, outDir, g, prog, stepRef{Name: "re-stack"}, plan.AnchorNight)
		}
		rres.Channels = append(rres.Channels, ch)
		if ro.Stager != nil {
			ro.Stager.Free(ctx, filter+" lights (re-stack)", currentGroupPaths(plan, filter))
		}
	}
	channels := channelMastersMap(rres, outDir, paletteWantsOIIIAsBlue(preset))
	if len(channels) == 0 {
		return nil, fmt.Errorf("re-stack produced no channel masters")
	}
	aligned := alignChannels(ctx, ro, channels, filepath.Join(workRun, "04_aligned"), outDir, rres, prog)
	if len(aligned) == 0 {
		return nil, fmt.Errorf("re-stack alignment produced no channels")
	}
	return aligned, nil
}

// finishAligned assembles the final image from co-registered channel masters: the optional local-AI-
// agent supervised finish (opt-in), else the layered GIMP composite, else the Siril rgbcomp fallback.
// It sets res.Final. Shared by combine() (a fresh run) and RefineExistingRun (re-tune an existing run).
// Progress: each stage begins its own named step (opts.beginStep; index-less lines without a stepper).
func finishAligned(ctx context.Context, opts Options, channels map[string]string, res *Result, workRun, outDir string) {
	// Every finish path (supervised early-return, GIMP, Siril fallback) gets the objective quality
	// snapshot + threshold warnings stamped on its way out.
	defer stampFinishQuality(res)
	// A pure star cluster (globular/open) gets the gentler star-field finish PROFILE applied to the
	// working preset here — so the supervised loop, the GIMP composite and the Siril fallback all start
	// from the same cluster-adjusted preset (they used to diverge: only the GIMP path saw the override).
	// opts is a value copy, so this never leaks to the caller / the persisted manifest — a rerun re-derives
	// it. Patch-preserving: a user/agent override of any knob wins. Galaxy/nebula are untouched.
	if opts.Preset != nil && starClusterTarget(opts) {
		cp := applyClusterProfile(*opts.Preset)
		opts.Preset = &cp
		defer func() {
			if res.Final != nil {
				res.Final.Notes = append(res.Final.Notes, fmt.Sprintf(
					"star-cluster finish profile: lum %.2f, sat %.2f, chroma blur %.0f, star desat %.2f, headroom %.2f",
					cp.LumOpacity, cp.Saturation, cp.ChromaBlur, cp.StarDesat, cp.StretchHeadroom))
			}
		}()
	}
	// Also save the optional monochrome side outputs (processed L, combined all-channel) once the finish
	// below has set res.Final — one defer covers every finish path (supervised / GIMP / Siril fallback),
	// and it captures the (possibly cluster-adjusted) opts.Preset above. Soft-fail; never blocks the run.
	defer emitMonoOutputs(ctx, opts, channels, res, workRun, outDir)

	// Optional: local-AI-agent supervised finish (opt-in; GIMP composite path only). Soft-fall to the
	// standard finish on any error so a run never fails because of the agent. The loop owns one step
	// on the main bar; its inner re-renders run with a nil stepper so they never advance it.
	if superviseEnabled(ctx, opts) {
		opts.beginStep("supervised finish")
		so := opts
		so.steps = nil
		if final, err := superviseFinishDeepsky(ctx, so, channels, workRun, outDir); err != nil {
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
		} else if final, method, err := finishWithGimp(ctx, opts, channels, workRun, outDir); err != nil {
			if ctx.Err() != nil {
				// The run was cancelled mid-finish: don't dress the interruption up as a tool
				// failure, and don't burn time on the Siril fallback the same cancel would kill.
				res.Warnings = append(res.Warnings, "run cancelled — finishing skipped")
				return
			}
			res.Warnings = append(res.Warnings, "GIMP finishing failed, falling back to Siril: "+err.Error())
		} else {
			res.Final = final
			// Surface a degraded colour ladder as a WARNING, not just a buried final note: the user
			// asked for colour calibration and neither photometric rung (SPCC/PCC) ran
			// (unsolvable/offline), so the balance came from a star-derived or neutral fallback.
			// Preset-gated so runs with color_calibration=false (incl. the byte-pinned goldens) are
			// untouched; the narrowband palette skips calibration by design.
			if opts.Preset.ColorCalibration && !method.Photometric() && method != postprocess.CalPalette {
				warnLive(opts, res, "photometric colour calibration (SPCC/PCC) did not run — colours come from a fallback (see the final notes; a resolvable target name or coords would enable it)")
			}
			// Gated deterministic star repair: no-op unless the finish has fixable burnt/discoloured stars
			// (soft-fail; the deferred stampFinishQuality re-measures whichever final it promotes).
			autoFixStars(ctx, opts, channels, workRun, outDir, res)
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
	final, err := postprocess.Combine(ctx, opts.Runner, outDir, channels, "final", ppOpts,
		opts.beginStep("combining channels (Siril)"))
	if err != nil {
		if ctx.Err() != nil {
			res.Warnings = append(res.Warnings, "run cancelled — channel combination skipped")
		} else {
			res.Warnings = append(res.Warnings, "channel combination failed: "+err.Error())
		}
		return
	}
	res.Final = final
}

// finishWithGimp produces stretched per-component TIFFs with Siril, then composites them into a
// layered image (with curves) in GIMP. It also reports which colour-calibration rung the prep
// landed on, so the caller can surface a degraded ladder (SPCC unavailable) as a run warning.
func finishWithGimp(ctx context.Context, opts Options, channels map[string]string, workRun, outDir string) (*postprocess.Result, postprocess.CalMethod, error) {
	stretchDir := filepath.Join(workRun, "05_stretched")
	if err := fsutil.EnsureDir(stretchDir); err != nil {
		return nil, postprocess.CalNone, err
	}
	deg := backgroundDegree(ctx, opts) // 0 when GraXpert already extracted the background
	cc := postprocess.ColorCalOptions{
		Enabled: opts.Preset.ColorCalibration, RemoveGreen: true, StarField: true,
		Solve: opts.Solve, Spcc: opts.Spcc,
	}
	in, notes, method, err := prepGimpInputs(ctx, opts, opts.Runner, channels, outDir, stretchDir, deg, cc, opts.Preset.BackgroundLevel, opts.Preset.LinkedStretch)
	if err != nil {
		return nil, method, err
	}
	final, err := finishComposite(ctx, opts, in, notes, channels, outDir)
	return final, method, err
}

// finishComposite applies the Tier-A composite knobs from the working preset onto a linear prep —
// freshly built (finishWithGimp) or persisted (a Tier-A rerun, checkpoint.go) — renders the layered
// GIMP composite into <outDir>/final.*, and applies the optional StarNet++ star reduction (capturing
// the star-reduced milestone). Sharing this tail keeps a rerun byte-for-byte consistent with a normal
// finish: a Tier-A rerun feeds a persisted prep here and skips the (slow) linear rebuild.
func finishComposite(ctx context.Context, opts Options, in gimp.Inputs, notes []string, channels map[string]string, outDir string) (*postprocess.Result, error) {
	in.HaBlack = opts.Preset.HaBlackPoint                                   // clip the Ha background to black so its red screen lifts only HII knots
	in.ChromaBlur = opts.Preset.ChromaBlur                                  // colour-only denoise (kills the thin RGB's pink noise; L keeps detail)
	in.LumCurve = boostLumCurve(opts.Preset.LumCurve, opts.Preset.LumBoost) // brighten the galaxy from the L luminance (not the combined value)
	in.LumOpacity = opts.Preset.LumOpacity                                  // blend the L layer below 100% for a softer, more RGB-driven LRGB
	in.CoreHighlightKnee = opts.Preset.CoreHighlightKnee                    // roll off the blown nebula core in the L luminance (pre-Ha-screen)
	in.CoreHighlightCeil = opts.Preset.CoreHighlightCeil
	in.HighlightKnee = opts.Preset.HighlightKnee // star-safe highlight cap on the final composite (cores never burn / over-orange)
	in.HighlightCeil = opts.Preset.HighlightCeil
	in.StarDesat = opts.Preset.StarDesat           // desaturate bright star cores → no colour discs on dense star fields (clusters)
	in.CropFrac = opts.Preset.CropFrac             // trim ragged stacking-edge bands off the export
	in.HaExcludeStars = opts.Preset.HaExcludeStars // screen Ha onto nebulosity only when requested
	// The star-cluster finish profile (gentle luminance, no core roll-off, colour-safe saturation,
	// chroma blur, star-core desaturation) is already baked into opts.Preset by finishAligned/rerun for
	// a cluster target — so every knob above + the saturation below flow through unchanged here.
	saturation := opts.Preset.Saturation
	opts.beginStep("composite (GIMP)") // GIMP itself is silent; the ▶/✓ boundary lines carry its duration
	g, err := gimp.BuildImage(opts.Gimp, in, opts.Preset.Curve, in.HaOpacity(opts.Preset.HaScreen), saturation, filepath.Join(outDir, "final"))
	if err != nil {
		return nil, err
	}
	out := &postprocess.Result{
		Mode:     compMode(channels, opts.Preset),
		Channels: filterList(channels),
		Outputs:  []string{g.Xcf, g.Tif, g.Png},
		Notes:    append([]string{"layered GIMP composite + curves"}, notes...),
	}

	// Optional: StarNet++ star reduction on the flattened composite (works for any compose mode).
	if aiStars(ctx, opts) {
		extra, note := reduceStarsAI(ctx, opts, g.Tif, outDir, opts.beginStep("star reduction (StarNet++)"))
		out.Outputs = append(out.Outputs, extra...)
		if note != "" {
			out.Notes = append(out.Notes, note)
		} else {
			out.Notes = append(out.Notes, fmt.Sprintf("StarNet++ star reduction (stars at %.0f%%)", opts.Preset.StarReduce*100))
		}
		// Milestone: the star-reduced final (copy the ready PNG if StarNet produced one).
		if reduced := filepath.Join(outDir, "final_reduced.png"); fileExists(reduced) {
			capturePreview(ctx, opts, outDir, ordStarless, stageStarless, "", reduced, false)
		}
	}
	return out, nil
}

// Cluster finish profile. A globular/open star CLUSTER (OpenNGC GCl/OCl) is a pure star field: it has
// no extended structure to lift and no nebula core to roll off, so the galaxy/nebula recipe blows the
// dense core to white, paints bright stars as solid colour discs (the noisy shallow RGB chroma spread
// over the L star profile by LAYER-MODE-LUMINANCE) and mottles the crushed background. These gentler
// values are swapped in by applyClusterProfile (see starClusterTarget). Named so they are easy to re-tune.
var (
	// clusterLumCurve is a gentle, near-linear luminance curve that (unlike the galaxy LumCurve's hard
	// mid-tone lift) preserves the core's star-to-glow gradient — so the core reads as resolved stars,
	// not a flat white plateau — while softly rolling the very top below pure white.
	clusterLumCurve = []float64{0, 0, 0.5, 0.48, 0.8, 0.72, 1, 0.85}
)

const (
	// clusterLumOpacity keeps the L luminance at FULL opacity so the clean L owns each star's profile and
	// colour arrives only as a LUMINANCE tint — the opposite of the old 0.85, which let the noisy RGB base
	// fill star centres and made the colour discs worse.
	clusterLumOpacity = 1.0
	// clusterSaturation caps the final saturation low so bright star colours read natural, not neon.
	clusterSaturation = 0.06
	// clusterStretchHeadroom caps the linear cores further below 1.0 (vs the deepsky 0.90) so a dense
	// cluster core does not clip to white through the finishing autostretch.
	clusterStretchHeadroom = 0.80
	// clusterChromaBlur blurs ONLY the colour base in the composite (the full-opacity L keeps all detail),
	// flattening the purple-green chroma mottle that the joint denoise + chroma-smooth leave on the very
	// shallow colour subs a cluster is usually shot with.
	clusterChromaBlur = 4.0
	// clusterStarDesat desaturates the bright star cores/wings toward white, killing the solid blue/magenta
	// colour discs that LAYER-MODE-LUMINANCE paints from the thin RGB base's exaggerated per-star chroma.
	clusterStarDesat = 0.6
)

// starClusterTarget reports whether this run's target is a pure star cluster (OpenNGC globular/open),
// which needs the gentler star-field finish rather than the galaxy/nebula recipe. Soft-fails to false
// (no object name, no catalogue, unknown/typed-otherwise object) → the default recipe stays byte-for-byte
// unchanged. The catalogue Load is cached per dir, so calling this from both finish stages is cheap.
func starClusterTarget(opts Options) bool {
	if opts.PriorObject == "" {
		return false
	}
	cat := skycat.Load(opts.CatalogDir)
	// Try the whole name and each token, like the coord resolver — a compound folder ("M92_LRGB",
	// "NGC6341 session2") still identifies the cluster.
	for _, name := range objectCandidates(opts.PriorObject) {
		if cat.IsStarCluster(name) {
			return true
		}
	}
	return false
}

// applyClusterProfile returns a copy of p with the star-cluster finish profile applied. It is
// PATCH-PRESERVING: a scalar knob is only overridden when it still equals the stock mode default, so a
// user/agent override (from run params or the supervised loop) always wins. LumCurve is not a tunable
// knob, so a cluster always gets the gentle near-linear curve. Only the finish tone is touched —
// stacking/calibration are identical to any other deep-sky run. Called by finishAligned (every finish
// path) and RerunFromStage, so the supervised loop, the GIMP composite and the Siril fallback agree.
func applyClusterProfile(p mode.Preset) mode.Preset {
	stock := mode.For(p.Mode)
	eq := func(a, b float64) bool { return !floatChanged(a, b) }
	if eq(p.Saturation, stock.Saturation) {
		p.Saturation = clusterSaturation
	}
	if eq(p.StretchHeadroom, stock.StretchHeadroom) {
		p.StretchHeadroom = clusterStretchHeadroom
	}
	if eq(p.ChromaBlur, stock.ChromaBlur) {
		p.ChromaBlur = clusterChromaBlur
	}
	if eq(p.StarDesat, stock.StarDesat) {
		p.StarDesat = clusterStarDesat
	}
	if eq(p.LumOpacity, stock.LumOpacity) {
		p.LumOpacity = clusterLumOpacity
	}
	if eq(p.CoreHighlightKnee, stock.CoreHighlightKnee) && eq(p.CoreHighlightCeil, stock.CoreHighlightCeil) {
		p.CoreHighlightKnee, p.CoreHighlightCeil = 0, 0 // disable the nebula-core flatten
	}
	p.LumCurve = clusterLumCurve
	return p
}

// stretchScript builds the Siril program that turns the linear RGB base into the stretched TIFF the
// GIMP composite loads: load, an OPTIONAL SCNR green removal (rmgreen — only right after SPCC; see the
// caller), then the autostretch to the target sky background, saved as a TIFF. loadName is the Siril-
// relative base name (no ext, loaded from the working dir); saveTif is the output TIFF path (no ext).
// The `requires`/`setext` header is prepended by the caller.
func stretchScript(loadName, saveTif string, rmgreen, linked bool, bgLevel float64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "load %s\n", loadName)
	if rmgreen {
		b.WriteString("rmgreen 0\n")
	}
	fmt.Fprintf(&b, "%s\n", siril.AutostretchCmd(linked, bgLevel))
	if saveTif != "" { // "" → the caller appends its own save (the sky-chroma-flatten FITS detour)
		b.WriteString(saveDisplayTif(saveTif))
	}
	return b.String()
}

// saveDisplayTif writes the GIMP composite's input TIFF with the colour profile ASSIGNED rather than
// converted, so the numbers reach GIMP exactly as the stretch left them.
//
// Siril 1.4 is colour-managed, and `savetif` CONVERTS from the image's profile to the output's. That
// is invisible for as long as the image carries no profile — Siril just assigns sRGB and the pixels
// pass through — which is what every run did while the colour ladder fell to its star-field rung. The
// first run where PCC actually landed came out washed out and pink, and the cause is one HDU:
//
//	HDU 2: type=0, EXTNAME=ICCProfile
//
// PCC attaches a profile, so savetif started converting, applying the sRGB transfer curve to already-
// stretched data. Measured on that run: the stretched FITS sky sits at 0.061 and the TIFF handed to
// GIMP at 0.286 — which is 0.061^(1/2.2). A whole image lifted a stop and a half.
//
// The washed-out sky is the obvious half. The pink is the same fault: the composite's saturation boost
// is shadow-protected, full strength only above ~60% luminance, so at 0.06 the galaxy and star halos
// sat under the ramp and at 0.29 they sit over it, and the residual chroma of thin colour data blooms
// magenta exactly where the user saw it — around stars, across the galaxy, everywhere.
//
// icc_assign REINTERPRETS without touching pixels, so this is a no-op for a profile-less image and the
// pre-PCC output stays byte-identical.
func saveDisplayTif(name string) string {
	return fmt.Sprintf("icc_assign sRGB\nsavetif %s\n", name)
}

// prepGimpInputs builds the stretched per-component TIFFs GIMP composites. The RGB base is produced
// as a linear, background-extracted image, color-calibrated (SPCC → neutralization fallback), then
// stretched; L and Ha are stretched as luminance/structure layers (no color calibration). Returns
// the GIMP inputs and any color-calibration notes.
func prepGimpInputs(ctx context.Context, opts Options, runner *siril.Runner, channels map[string]string,
	outDir, stretchDir string, deg int, cc postprocess.ColorCalOptions, bgLevel float64, linked bool) (gimp.Inputs, []string, postprocess.CalMethod, error) {
	var calMethod postprocess.CalMethod
	has := func(f string) bool { _, ok := channels[f]; return ok }
	// channels[f] is a Siril-relative basename (e.g. "aligned_L"): correct for the Siril commands below
	// (they run with outDir as the working dir and append .fits via `setext fits`), but the Go helpers
	// that open the FITS directly — equalizeBackgrounds, applyStretchHeadroom/headroomSource — need the
	// resolved absolute path. Without this they got the bare name, failed to open it ("no such file"),
	// and silently skipped: the L luminance was never headroom-capped, so its star cores clipped to pure
	// white (burnt cores + LRGB colour-ring halos), and the R/G/B backgrounds were never equalized.
	cpath := func(f string) string { return filepath.Join(outDir, channels[f]+".fits") }
	// Resolve the colour palette (natural / HaRGB / HOO / SHO / HOS / Foraxx / mono) against the channels
	// present. A palette missing its required filters falls back (note recorded). "natural" ≡ the legacy
	// R→R/G→G/B→B mapping, so a default run is byte-identical to before the palette engine.
	pal, palNote := resolvePalette(opts.Preset, channels)
	base := filepath.Join(stretchDir, "base")
	in := gimp.Inputs{Base: base + ".tif", Color: pal.Color}
	var notes []string
	if palNote != "" {
		notes = append(notes, palNote)
	}
	const hdr = "requires 1.2.0\nsetext fits\nset32bits\n"
	// Cap bright linear highlights just below 1.0 before each autostretch, so star cores keep their
	// colour instead of clipping to white (0 → off; the RGB base is rolled in place, L/mono via a copy).
	headroom := 0.0
	if opts.Preset != nil {
		headroom = opts.Preset.StretchHeadroom // for a cluster, finishAligned already lowered this to clusterStretchHeadroom
	}

	// An emission layer is SCREENED into the composite, so it gets a darker target background than the
	// base/lum: its pedestal must stay near black or the tinted screen washes the whole sky (a brown
	// cast). The GIMP black-point clip (preset.Ha/OIII/SIIBlackPoint) is the belt; this is the
	// suspenders. Shared by all three emission screens.
	emissionBg := bgLevel * 0.6
	if emissionBg < 0.03 {
		emissionBg = 0.03
	}

	if pal.Color {
		rSrc, gSrc, bSrc := channels[pal.R], channels[pal.G], channels[pal.B]
		pmPre := ""
		if pal.GExpr != "" { // dynamic palette (Foraxx): build the synthetic green with pixel math first
			pmPre = fmt.Sprintf("pm %q\nsave green_dyn\n", pal.GExpr)
			gSrc = "green_dyn"
		}
		// Additively match the source channels' sky backgrounds BEFORE the combine (natural family only),
		// so the merged sky is neutral grey and its chroma noise is zero-mean around grey (a per-channel
		// offset would otherwise stretch into a colour cast and turn the noise into coloured blotches). A
		// mapped narrowband palette assigns emission lines to R/G/B, where equalising the very different
		// line pedestals toward one grey would flatten the intended false colour — so it is skipped there.
		// One-shot color has a single already-merged channel: its three primaries were recorded through
		// one optical path in one exposure, so there is nothing to equalize and nothing to combine.
		osc := isColorRun(opts.Preset)
		if !pal.Narrowband && !osc {
			if n, err := equalizeBackgrounds(cpath(pal.R), cpath(pal.G), cpath(pal.B)); err != nil {
				notes = append(notes, "background equalization skipped: "+err.Error())
			} else if n != "" {
				notes = append(notes, n)
			}
		}
		// Build the linear RGB base (dark stretch below). The dark target background keeps the sky
		// near-black instead of Siril's washed-out 0.25 default. Mono channels are merged with rgbcomp;
		// a colour master IS the base and is only background-extracted. Everything downstream of this
		// point — combined background extraction, AI colour denoise, chroma smoothing, SPCC, the
		// stretch and the GIMP composite — is identical for both.
		combProg := opts.beginStep("combining channels + background")
		s1 := hdr + pmPre + fmt.Sprintf("rgbcomp %s %s %s -out=rgb_base\nload rgb_base\n%ssave rgb_base\n",
			rSrc, gSrc, bSrc, siril.SubskyCmd(deg))
		if osc {
			combProg = opts.beginStep("background extraction")
			s1 = hdr + fmt.Sprintf("load %s\n%ssave rgb_base\n", oscSource(channels), siril.SubskyCmd(deg))
		}
		if _, err := runner.Run(ctx, outDir, s1, combProg); err != nil {
			return gimp.Inputs{}, nil, calMethod, err
		}
		// Milestone: the combined RGB (channels aligned + merged), before gradient/colour calibration.
		capturePreview(ctx, opts, outDir, ordCombined, stageCombined, "", filepath.Join(outDir, "rgb_base.fits"), true)
		// Remove the residual large-scale colour gradient (amp-glow + light pollution) that survives the
		// per-channel extraction and the combine — a 2nd GraXpert pass on the linear combined RGB, before
		// SPCC, so the whole sky is homogeneous. RBF subsky is the deterministic fallback (no GraXpert).
		if n := extractCombinedBackgroundCached(ctx, opts, runner, outDir, "rgb_base", hdr, combProg); n != "" {
			notes = append(notes, n)
		}
		// AI colour denoise on the combined linear RGB (edge-preserving) — the SINGLE joint colour denoise
		// (the per-channel R/G/B denoise is deferred to here) that cuts the thin colour's heavy chrominance
		// noise *before* SPCC/stretch amplify it, without smearing star halos (a blur would).
		if jointColorDenoise(ctx, opts) {
			dnProg := opts.beginStep("AI colour denoise (GraXpert)")
			dnProg(siril.Progress{Line: "AI colour denoise typically takes 5–90 min depending on hardware (the container runs CPU-only) — GraXpert progress streams below"})
			if n := denoiseAICached(ctx, opts, filepath.Join(outDir, "rgb_base.fits"), outDir, dnProg); n != "" {
				notes = append(notes, "colour "+n)
			}
		}
		// Mean-preserving chroma smooth on the combined RGB — gaussian + star-protected (no square
		// patches around bright stars), with a coarse background-only pass for the large chroma mottle.
		// Luminance is byte-for-byte unchanged (the L layer supplies detail in LRGB); see chromasmooth.go.
		if opts.Preset != nil {
			smoothOpts := chromaSmoothOpts{FinePx: opts.Preset.ChromaSmoothPx, BgPx: opts.Preset.ChromaBgSmoothPx}
			if n, err := chromaSmoothRGB(filepath.Join(outDir, "rgb_base.fits"), smoothOpts); err != nil {
				notes = append(notes, "chroma smooth skipped: "+err.Error())
			} else if n != "" {
				notes = append(notes, n)
			}
		}
		// Colour balance. The natural family runs the SPCC → star-field → neutralization ladder, then a
		// LINKED stretch that keeps a trustworthy channel balance (SPCC's) — but only when SPCC actually
		// ran; the neutralization fallback stretches UNLINKED to equalize the channels toward neutral. A
		// mapped narrowband palette IS its colour (the channel→RGB assignment), so it skips calibration +
		// SCNR entirely, stretches UNLINKED to equalize the synthetic channels, and is marked "calibrated"
		// so the GIMP green trim is suppressed.
		ccProg := opts.beginStep("colour calibration + stretch")
		rmgreen := false
		if pal.Narrowband {
			in.CalibratedColor = true
			linked = false
			calMethod = postprocess.CalPalette
			notes = append(notes, fmt.Sprintf("palette %s: narrowband channel mapping (SPCC + SCNR skipped)", pal.Name))
		} else {
			note, method, err := colorCalibrateCached(ctx, opts, runner, outDir, "rgb_base", cc)
			if err != nil {
				return gimp.Inputs{}, nil, calMethod, err
			}
			if note != "" {
				notes = append(notes, note)
			}
			calMethod = method
			calibrated := method.Calibrated()
			linked = linked && calibrated
			in.CalibratedColor = calibrated
			// SCNR (rmgreen) follows BOTH trustworthy rungs. SPCC's photometric balance leaves a known
			// residual green cast; the star-field fallback now anchors the median star WARM (see
			// starcal.go — the old white anchor made the median star "neutral by construction", which is
			// why SCNR was once SPCC-only: on a white-forced field it tipped stars/sky magenta), so its
			// residual green is real cast, not construction — task #316 shipped green stars because this
			// gate left the star-field rung with no green removal at all. The neutralization fallback
			// already strips green inside NeutralizeScript; the narrowband palette skips SCNR by design.
			// ...but NOT after PCC. SCNR is one-sided — it can only ever LOWER green — so it is only
			// safe where a known green excess exists to remove: SPCC's balance leaves one, and the
			// star-field rung's warm anchor means its residual green is real cast. PCC's photometric
			// balance leaves neither, so all SCNR can do there is bias green DOWN, which puts red and
			// blue above it and reads as magenta. On a frame whose signal rides at 1e-4 over a 0.2449
			// pedestal that bias is not subtle: it showed up as blue landing 0.9% over green after the
			// stretch where the two were exactly equal before it.
			rmgreen = method.Calibrated() && method != postprocess.CalPCC
		}
		// Milestone: the colour-calibrated (gradient-removed) linear RGB, before the stretch.
		capturePreview(ctx, opts, outDir, ordColorCal, stageColorCal, "", filepath.Join(outDir, "rgb_base.fits"), true)
		// Orientation guard: Siril's compose can emit rgb_base with REVERSED file rows relative to
		// the aligned masters while stamping the same ROWORDER card (seen with 1.4.3 rgbcomp on the
		// nebula path) — the base then reaches GIMP upside-down vs the L/Ha layers, which load
		// straight from the masters. Content-checked and corrected in place BEFORE the headroom and
		// stretch consume it; soft-fail — a skipped check never blocks the prep.
		orientRef := "L"
		if !has("L") {
			orientRef = firstFilter(channels)
		}
		if orientRef != "" {
			if n, err := ensureRowOrientation(filepath.Join(outDir, "rgb_base.fits"), cpath(orientRef)); err != nil {
				notes = append(notes, "base orientation check skipped: "+err.Error())
			} else if n != "" {
				notes = append(notes, n)
				opts.report(Progress{Line: "⚠ " + n})
			}
		}
		// Cap the linear highlights (star cores, galaxy nucleus) just below 1.0 with their channel ratios
		// intact, so the autostretch can't clip them to colourless white. In place on the calibrated RGB.
		if n, err := applyStretchHeadroom(filepath.Join(outDir, "rgb_base.fits"), filepath.Join(outDir, "rgb_base.fits"), headroom); err != nil {
			notes = append(notes, "base stretch headroom skipped: "+err.Error())
		} else if n != "" {
			notes = append(notes, "base "+n)
		}
		// Post-stretch sky-chroma flatten (preset.SkyChromaFlattenPx): the stretch amplifies sub-percent
		// linear background chroma residuals (RBF ringing, denoise mottle) into visible bands/discs, so
		// neutralize the sky IN DISPLAY SPACE — stretch to a FITS, flatten in Go, then save the TIFF.
		// Skipped for narrowband palettes (their false-colour sky is intentional). Knob off → the single
		// legacy stretch script, byte-identical to before. Soft-fail: a flatten error still saves the TIFF.
		chromaPx, lumPx := 0, 0
		if opts.Preset != nil && !pal.Narrowband {
			chromaPx = opts.Preset.SkyChromaFlattenPx
			lumPx = opts.Preset.SkyLumFlattenPx
		}
		if chromaPx > 0 || lumPx > 0 {
			s2 := hdr + stretchScript("rgb_base", "", rmgreen, linked, bgLevel) + "save rgb_base_stretch\n"
			if _, err := runner.Run(ctx, outDir, s2, ccProg); err != nil {
				return gimp.Inputs{}, nil, calMethod, err
			}
			stretchFits := filepath.Join(outDir, "rgb_base_stretch.fits")
			if chromaPx > 0 {
				if n, err := flattenSkyChroma(stretchFits, chromaPx); err != nil {
					notes = append(notes, "sky chroma flatten skipped: "+err.Error())
				} else if n != "" {
					notes = append(notes, n)
				}
			}
			if lumPx > 0 {
				if n, err := flattenSkyLuminance(stretchFits, lumPx); err != nil {
					notes = append(notes, "sky luminance flatten skipped: "+err.Error())
				} else if n != "" {
					notes = append(notes, n)
				}
			}
			s2b := hdr + "load rgb_base_stretch\n" + saveDisplayTif(base)
			if _, err := runner.Run(ctx, outDir, s2b, ccProg); err != nil {
				return gimp.Inputs{}, nil, calMethod, err
			}
		} else {
			s2 := hdr + stretchScript("rgb_base", base, rmgreen, linked, bgLevel)
			if _, err := runner.Run(ctx, outDir, s2, ccProg); err != nil {
				return gimp.Inputs{}, nil, calMethod, err
			}
		}
	} else {
		monoFilter := pal.R // the palette's chosen single source (mono palette prefers L→Ha→first)
		if monoFilter == "" {
			monoFilter = firstFilter(channels)
		}
		monoProg := opts.beginStep("combining channels + background")
		mono := headroomSource(cpath(monoFilter), "base", stretchDir, headroom, &notes)
		lumPx := 0
		if opts.Preset != nil {
			lumPx = opts.Preset.SkyLumFlattenPx
		}
		// Mono-mode base IS the final's luminance — same post-stretch sky-level flatten detour as
		// the colour base (knob 0 → the single legacy script, byte-identical).
		if lumPx > 0 {
			monoStretch := filepath.Join(stretchDir, "base_stretch")
			s := hdr + fmt.Sprintf("load %s\n%s%s\nsave %s\n", mono, siril.SubskyCmd(deg), siril.AutostretchCmd(false, bgLevel), monoStretch)
			if _, err := runner.Run(ctx, outDir, s, monoProg); err != nil {
				return gimp.Inputs{}, nil, calMethod, err
			}
			if n, err := flattenSkyLuminance(monoStretch+".fits", lumPx); err != nil {
				notes = append(notes, "sky luminance flatten skipped: "+err.Error())
			} else if n != "" {
				notes = append(notes, n)
			}
			s2 := hdr + fmt.Sprintf("load %s\nsavetif %s\n", monoStretch, base)
			if _, err := runner.Run(ctx, outDir, s2, monoProg); err != nil {
				return gimp.Inputs{}, nil, calMethod, err
			}
		} else {
			s := hdr + fmt.Sprintf("load %s\n%s%s\nsavetif %s\n", mono, siril.SubskyCmd(deg), siril.AutostretchCmd(false, bgLevel), base)
			if _, err := runner.Run(ctx, outDir, s, monoProg); err != nil {
				return gimp.Inputs{}, nil, calMethod, err
			}
		}
	}

	// L and Ha components (stretched for layering; no color calibration).
	var b strings.Builder
	b.WriteString(hdr)
	wrote := false
	if pal.UseLum && has("L") { // narrowband palettes consume their channels as base, no separate L luminance
		lum := filepath.Join(stretchDir, "lum")
		lumSrc := headroomSource(cpath("L"), "lum", stretchDir, headroom, &notes)
		lumPx := 0
		if opts.Preset != nil {
			lumPx = opts.Preset.SkyLumFlattenPx
		}
		// The final's luminance comes from THIS layer (GIMP LAYER-MODE-LUMINANCE), so the sky-level
		// flatten must run here too — its own detour (the Ha/OIII layers stay in the batched script;
		// their RBF subsky + black-point clip handle their gradients). Knob 0 → the legacy batched
		// line, byte-identical.
		if lumPx > 0 {
			lumStretch := filepath.Join(stretchDir, "lum_stretch")
			s := hdr + fmt.Sprintf("load %s\n%s%s\nsave %s\n", lumSrc, siril.SubskyCmd(deg), siril.AutostretchCmd(false, bgLevel), lumStretch)
			if _, err := runner.Run(ctx, outDir, s, opts.sirilLines("stretching L/Ha layers")); err != nil {
				return gimp.Inputs{}, nil, calMethod, err
			}
			if n, err := flattenSkyLuminance(lumStretch+".fits", lumPx); err != nil {
				notes = append(notes, "lum sky luminance flatten skipped: "+err.Error())
			} else if n != "" {
				notes = append(notes, "lum "+n)
			}
			fmt.Fprintf(&b, "load %s\nsavetif %s\n", lumStretch, lum)
		} else {
			fmt.Fprintf(&b, "load %s\n%s%s\nsavetif %s\n", lumSrc, siril.SubskyCmd(deg), siril.AutostretchCmd(false, bgLevel), lum)
		}
		in.Lum = lum + ".tif"
		wrote = true
	}
	// The three emission screens (Hα red, [OIII] teal, [SII] deep-red/gold) are mechanically identical:
	// continuum-subtract → RBF-flatten → autostretch to a dark background → save a TIFF for GIMP plus a
	// FITS twin for the wash gate. They were three copies of this block; the table keeps them honest.
	//
	// Screens run for the natural family only — a narrowband palette consumes these filters as its BASE
	// channels, and screening one on top of itself would double-count the line.
	for _, es := range emissionScreens(pal, opts.Preset, &in) {
		if !es.enabled || !has(es.filter) {
			continue
		}
		src := channels[es.filter]
		screen := true
		// Continuum subtraction: screen only true EMISSION. The raw master is continuum+emission — the
		// black-point clip and low opacity that keep its continuum from washing the frame also erase the
		// faint emission sitting just above it. excess = line − k·broadband is black except where the
		// line actually emits, so the stretch can lift faint filaments and the screen shows them.
		if opts.Preset == nil || opts.Preset.HaContinuumSub { // one knob governs all three screens
			if ec, why := es.continuumSub(outDir, channels, stretchDir); ec != nil {
				if ec.Faint {
					screen = false
					n := fmt.Sprintf("%s continuum-subtracted (×%.2f vs %s): no emission above noise (P99.5 %.1fσ) — %s screen dropped",
						es.filter, ec.K, ec.Ref, ec.P995Sigma, es.filter)
					notes = append(notes, n)
					opts.report(Progress{Line: "⚠ " + n})
				} else {
					src = ec.ExcessPath
					n := fmt.Sprintf("%s continuum-subtracted (×%.2f vs %s) — emission-only %s screen", es.filter, ec.K, ec.Ref, es.tint)
					notes = append(notes, n)
					opts.report(Progress{Line: n})
				}
			} else if why != "" {
				notes = append(notes, fmt.Sprintf("%s continuum subtraction unavailable — full %s layer screened: %s", es.filter, es.filter, why))
			}
		}
		if !screen {
			continue
		}
		out := filepath.Join(stretchDir, es.base)
		// The layer is screened as a saturated tint over the whole frame, so any residual large-scale
		// gradient in it becomes a coloured wash/blotch across the sky — a degree-1 polynomial cannot
		// model an asymmetric amp-glow gradient, so it gets the RBF subsky (preset.HaRBF, default on)
		// instead of the gentle plane the other layers use.
		sub := siril.SubskyCmd(deg)
		if opts.Preset == nil || opts.Preset.HaRBF {
			sub = siril.SubskyRBFCmd()
		}
		// `save <line>_stat` keeps a FITS twin of the stretched layer so the wash gate below can measure
		// what the screen would actually paint (deleted after measurement).
		fmt.Fprintf(&b, "load %s\n%s%s\nsavetif %s\nsave %s\n", src, sub, siril.AutostretchCmd(false, emissionBg), out, es.statBase)
		*es.dest = out + ".tif"
		wrote = true
	}
	if wrote {
		if _, err := runner.Run(ctx, outDir, b.String(), opts.sirilLines("stretching L/emission layers")); err != nil {
			return gimp.Inputs{}, nil, calMethod, err
		}
	}
	// The wash gate: measure what each screen would ACTUALLY paint and attenuate (or drop) a layer
	// whose stretched background came out bright. Runs after the Siril pass, since it reads the
	// measurement FITS that pass wrote.
	for _, es := range emissionScreens(pal, opts.Preset, &in) {
		if *es.dest == "" {
			continue
		}
		factor, gateNote := gateScreenStat(outDir, es.statBase, es.filter)
		if gateNote != "" {
			notes = append(notes, gateNote)
			opts.report(Progress{Line: "⚠ " + gateNote})
		}
		if factor == 0 {
			*es.dest = ""
			continue
		}
		// The gate factor persists with the prep (Tier-A retunes re-apply it via <Line>Opacity); the
		// final opacity + black point travel inside Inputs.
		//
		// Ha is the exception: its opacity is still a positional BuildImage argument, so only the
		// factor is stored here and the caller multiplies.
		if es.applyGate != nil {
			es.applyGate(factor)
		}
	}
	// Persist the linear prep (base/lum/ha) next to the run so a later composite-only tweak (e.g.
	// lum_opacity, saturation — Tier A) re-renders in seconds without redoing this prep. Best-effort:
	// a failed copy just makes a Tier-A rerun rebuild from the on-disk channel masters (Tier B).
	if perr := persistLinearPrep(outDir, in, notes); perr != nil {
		notes = append(notes, "linear prep not persisted (Tier-A rerun will rebuild it): "+perr.Error())
	}
	return in, notes, calMethod, nil
}

func compMode(channels map[string]string, p *mode.Preset) string {
	// A mapped narrowband palette names the composite after the palette (SHO/HOO/…); the natural family
	// (natural/hargb/mono) reports the channel-derived composite mode.
	if pal, _ := resolvePalette(p, channels); pal.Narrowband {
		return strings.ToUpper(pal.Name)
	}
	has := func(f string) bool { _, ok := channels[f]; return ok }
	rgb := has("R") && has("G") && has("B")
	if !rgb {
		return "mono"
	}
	// Prefix with whichever emission lines rode along, in canonical order — an LRGB+SII run used to
	// report a bare "LRGB", hiding the narrowband entirely from run.json and the UI. Ha alone keeps
	// its historical "HaLRGB"/"HaRGB" spelling.
	base := "RGB"
	if has("L") {
		base = "LRGB"
	}
	prefix := ""
	for _, f := range filters.Narrowband {
		if has(f) {
			prefix += f
		}
	}
	return prefix + base
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

// stackWeight returns the Siril stack weighting mode from the preset, validated against the modes
// Siril accepts (noise|wfwhm|nbstars|nbstack). An unset or unknown value yields "" (unweighted), so
// the stack command stays byte-identical to the legacy path.
func (o Options) stackWeight() string {
	if o.Preset == nil {
		return ""
	}
	switch o.Preset.StackWeight {
	case "noise", "wfwhm", "nbstars", "nbstack":
		return o.Preset.StackWeight
	default:
		return ""
	}
}

// lightStack is this run's light-frame stacking recipe: the preset's own combination/rejection/
// normalization choice with the weighting resolved for THIS channel — a photometrically normalized
// multi-group channel may override the preset's weight (see photomStackWeight). A nil or unset
// preset falls back to the historical defaults, so the emitted command stays byte-identical.
func (o Options) lightStack(weight string) stackalg.Options {
	s := stackalg.DefaultLights()
	if o.Preset != nil && o.Preset.Stack != (stackalg.Options{}) {
		s = o.Preset.Stack
	}
	s.Weight = stackalg.Weight(weight)
	return s
}

// cometStack is the COMET-ALIGNED stack's recipe — asymmetric by default so the marching star
// trails are clipped while the faint tail survives (see stackalg.DefaultComet).
func (o Options) cometStack() stackalg.Options {
	if o.Preset != nil && o.Preset.StackComet != (stackalg.Options{}) {
		return o.Preset.StackComet
	}
	return stackalg.DefaultComet()
}

// masterStacks is this run's per-frame-type calibration recipes (bias/dark/flat/dark-flat). A nil or
// unset preset falls back to the historical defaults, so master names and contents are unchanged.
func (o Options) masterStacks() stackalg.MasterOptions {
	if o.Preset != nil && o.Preset.Masters != (stackalg.MasterOptions{}) {
		return o.Preset.Masters
	}
	return stackalg.DefaultMasters()
}

// masterStack is one calibration master's stacking recipe for this run.
func (o Options) masterStack(mt calib.MasterType) stackalg.Options {
	return calib.MasterStackOptions(mt, o.masterStacks())
}

func processChannel(ctx context.Context, opts Options, set inspect.Set, masters []calib.Master,
	workRun, outDir string, gradeOpts grade.Options, onProgress func(siril.Progress)) ChannelResult {
	sel := calib.MatchForLightExcluding(set.Key, masters, opts.CalibExclude, opts.ForceCalibration)
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
	// Pull from the S3 library mirror if absent locally — including the dark's defect sidecar (its
	// absence is soft: calibration then falls back to -cc=dark).
	opts.ensureMasters(ctx, []string{dark, flat, bias, calib.DefectsListPath(dark)})
	cm := siril.CalibMasters{Dark: dark, Flat: flat, Bias: bias, DarkOptimize: sel.DarkOptimize,
		BadPixelMap: calib.DefectsListFor(dark), CFA: needsDebayer(set.Frames)}

	// A single-frame set cannot form a Siril sequence (no .seq is written for one image), so the
	// sequence calibrate/register would abort: calibrate the frame alone and promote it to the
	// channel master directly — no registration, grading or stacking to run on one frame.
	ingest := seqIngest(set.Frames)
	if set.Count == 1 {
		if _, err := opts.Runner.Run(ctx, seqDir, siril.CalibrateSingleScriptWith("light", cm, ingest), onProgress); err != nil {
			ch.Err = err.Error()
			return ch
		}
		warnChannel(opts, &ch, set.Key.Filter+": only 1 frame captured — using it as the channel master (no registration/stacking)")
		promoteLoneCalibrated(ctx, opts, &ch,
			calibratedFramePaths(seqDir, siril.CalibratedSeq("light", cm), 1)[0], set.Key.Filter, outDir, onProgress)
		_ = os.RemoveAll(seqDir)
		return ch
	}

	// Calibrate + register (writes per-frame metrics to the calibrated sequence's .seq), then grade
	// and stack the survivors.
	if _, err := opts.Runner.Run(ctx, seqDir, siril.CalibrateRegisterScriptWith("light", cm, ingest), onProgress); err != nil {
		ch.Err = err.Error()
		return ch
	}
	finishStackedChannel(ctx, opts, seqDir, siril.CalibratedSeq("light", cm), siril.RegisteredSeq("light", cm),
		set.Key.Filter, set.Frames, outDir, gradeOpts, opts.stackWeight(), onProgress, &ch, nil, nil,
		(*regGeometry)(nil))
	return ch
}

// finishStackedChannel grades a calibrated+registered sequence, stacks the survivors into the channel
// master, then runs the linear-master finishing (AI background extraction, denoise, preview). It is
// shared by the single-session path (processChannel) and the cross-session path (processChannelGroups).
func finishStackedChannel(ctx context.Context, opts Options, seqDir, baseSeq, regSeq, filter string,
	frames []*inspect.Frame, outDir string, gradeOpts grade.Options, stackWeight string,
	onProgress func(siril.Progress), ch *ChannelResult, preReject map[int]string, spans []grade.Span,
	geom *regGeometry) {
	dropTransition := opts.Preset != nil && opts.Preset.DropFilterWheelTransition
	metrics, rejectedReg, regCount, err := gradeChannel(seqDir, baseSeq, frames, gradeOpts, dropTransition, preReject, spans)
	if err != nil {
		ch.Err = fmt.Sprintf("grading: %v", err)
		return
	}
	ch.Metrics = metrics
	ch.StackedFrames = regCount - len(rejectedReg)
	// Rasterize the stacked-frame coverage now (grouped runs only): the seam noise equalization in
	// finishLinearMaster needs it, and recordChannelCoverage reuses it for the crop/PNG record.
	// Under mosaic the padded frames' homographies are composed onto the real CONTENT rectangle so
	// the zero padding never counts as coverage.
	if geom != nil && geom.FrameH != nil {
		rejected := func(i int) bool {
			return i >= len(metrics) || metrics[i].Rejected || metrics[i].FWHM <= 0
		}
		if geom.Canvas.W > 0 {
			contentH := composeContentH(geom.FrameH, geom.Canvas.OffX, geom.Canvas.OffY)
			cv := canvasSpec{W: geom.Canvas.W, H: geom.Canvas.H} // offsets baked into the homographies
			ch.coverage = rasterizeCoverageOn(contentH, rejected, geom.ContentW, geom.ContentH, cv, coverageDownscale)
		} else if w, h := frameDims(mergedFramePath(seqDir, baseSeq, 0)); w > 0 && h > 0 {
			ch.coverage = rasterizeCoverage(geom.FrameH, rejected, w, h)
		}
	}
	if regCount == 0 {
		ch.Err = "no frames could be registered"
		return
	}
	gradeWarnings(opts, ch, filter, metrics, regCount, regCount-len(rejectedReg))

	// Diagnose the capture-time pointing pattern (dithered / linear drift / static) from the
	// registration offsets: it decides whether fixed-pattern residuals decorrelate (and are
	// rejected) or survive as walking noise — and is the evidence behind the dithering advice.
	if ch.Dither = ditherReport(metrics); ch.Dither != nil {
		ch.Selection.Notes = append(ch.Selection.Notes, "capture offsets: "+ch.Dither.Note)
	}

	applyTrailMask(opts, seqDir, regSeq, ch, satCeilings(opts, ch, spans, metrics))

	masterName := "master_" + filterTag(filter) // basename in outDir (Siril CWD)
	outBase := filepath.Join(outDir, masterName)
	_, stackNote, err := stackSelectedOrCopy(ctx, opts.Runner, seqDir, regSeq, regCount, rejectedReg, outBase, opts.lightStack(stackWeight), onProgress)
	if err != nil {
		ch.Err = err.Error()
		return
	}
	if stackNote != "" {
		warnChannel(opts, ch, filter+": "+stackNote)
	}
	finishLinearMaster(ctx, opts, ch, masterName, outDir, filter, onProgress)
	_ = os.RemoveAll(seqDir) // reclaim the bulky calibrated/registered frame copies
}

// finishLinearMaster runs the shared post-stack finishing on outDir/<masterName>.fits: spurious
// BAYERPAT strip, AI background extraction, linear denoise and the UI preview. Split from
// finishStackedChannel so a promoted lone calibrated frame (promoteLoneCalibrated) gets the same
// finishing — and the same ChannelResult fields — as a stacked master.
func finishLinearMaster(ctx context.Context, opts Options, ch *ChannelResult, masterName, outDir, filter string,
	onProgress func(siril.Progress)) {
	ch.OutputPath = filepath.Join(outDir, masterName) + ".fits"

	// Drop any spurious BAYERPAT the stack inherited from older ASICAP mono captures: left in place it
	// makes Siril treat this monochrome master as an undebayered CFA image (a checkerboard) — which
	// breaks the per-channel denoise and, after rgbcomp, the plate-solve that SPCC needs. Safe here: the
	// mono pipeline only ever stacks non-Bayer frames. Soft-fail (cosmetic header edit).
	if err := fits.StripKeyword(ch.OutputPath, "BAYERPAT"); err != nil {
		ch.Selection.Notes = append(ch.Selection.Notes, "BAYERPAT strip skipped: "+err.Error())
	}

	// Never-dark-calibrated masters carry lone hot/cold pixels ("black pepper"); repair them before
	// any background model or denoise smears them. No-op for dark-calibrated channels.
	masterCosmetic(ch, ch.OutputPath)

	// Mosaic: the union master's never-covered margins are sky-filled BEFORE any background model
	// or stretch sees the zeros (crop mode discards them again at combine).
	if ch.Canvas != nil {
		fillMosaicMargins(opts, ch, outDir, filter)
	}
	// AI background extraction (GraXpert) on the linear master, replacing Siril's polynomial subsky at
	// finish. Soft-fail: a missing/erroring GraXpert leaves the master untouched.
	if aiBackground(ctx, opts) {
		if note := extractBackgroundAI(ctx, opts, ch.OutputPath, onProgress); note != "" {
			ch.Selection.Notes = append(ch.Selection.Notes, note)
		}
	}
	// Fade the noise-DEPTH step at coverage boundaries before the uniform denoise, so the standard
	// pass below sees an already-uniform noise field. Multi-night (coverage-carrying) masters only.
	if opts.Preset != nil && opts.Preset.SeamNoiseEq && ch.coverage != nil {
		equalizeSeamNoise(ctx, opts, ch, masterName, outDir, filter, onProgress)
	}
	// Measure the linear master's noise and denoise it in place — the Go starlet denoiser (adaptive,
	// star-safe) when enabled, else Siril's denoise — recording before/after sigma. Soft-fail inside.
	denoiseLinearMaster(ctx, opts, ch, masterName, outDir, filter, onProgress)
	// Quick preview PNG for the UI.
	if opts.Preset != nil && opts.Preset.Previews {
		if _, err := opts.Runner.Run(ctx, outDir, siril.PreviewScript(masterName+".fits", masterName+"_preview", 0.5), nil); err == nil {
			ch.PreviewPath = filepath.Join(outDir, masterName+"_preview.png")
		}
	}
}

// promoteLoneCalibrated copies a channel's single calibrated frame to the master path and runs the
// shared linear finishing: Siril has no one-image sequences, so a one-frame channel can be neither
// registered nor stacked — one usable frame beats a dead channel (the same trade
// stackSelectedOrCopy makes when only one frame REGISTERED). Callers journal the degraded path.
func promoteLoneCalibrated(ctx context.Context, opts Options, ch *ChannelResult, calibrated, filter, outDir string,
	onProgress func(siril.Progress)) {
	masterName := "master_" + filterTag(filter)
	if err := fsutil.CopyFile(calibrated, filepath.Join(outDir, masterName)+".fits"); err != nil {
		ch.Err = fmt.Sprintf("promote lone calibrated frame: %v", err)
		return
	}
	ch.StackedFrames = 1
	finishLinearMaster(ctx, opts, ch, masterName, outDir, filter, onProgress)
}

// satCeilings maps every REGISTERED frame (the contiguous r_ numbering Siril produces — frames it
// could not register are absent) to its group's post-normalization saturation ceiling: the level a
// sensor-clipped pixel lands at after that group's photometric transform. The pre-stack repair
// replaces at-ceiling pixels from the nights that still see the true value. nil when the preset
// disables the repair or the channel is single-group (no unsaturated night can exist to draw on).
func satCeilings(opts Options, ch *ChannelResult, spans []grade.Span, metrics []grade.Metric) []float32 {
	if opts.Preset == nil || !opts.Preset.CoreSatMask || len(spans) < 2 {
		return nil
	}
	merged := make([]float32, len(metrics))
	for gi, sp := range spans {
		ceil := float32(photom.SatDetectLevel) // no photom record → untransformed frames
		if gi < len(ch.Groups) && ch.Groups[gi].Photom != nil && ch.Groups[gi].Photom.SatCeiling > 0 {
			ceil = float32(ch.Groups[gi].Photom.SatCeiling)
		}
		for i := sp.Start; i < sp.End && i < len(merged); i++ {
			merged[i] = ceil
		}
	}
	reg := make([]float32, 0, len(metrics))
	for i, m := range metrics {
		if m.FWHM > 0 { // present in the registered sequence, same walk as gradeChannel
			reg = append(reg, merged[i])
		}
	}
	return reg
}

// applyTrailMask runs the cross-frame transient mask over the registered subs when the preset asks
// for it: it cleans satellite/plane trail segments + cosmic rays before stacking (a slow satellite
// lands in many subs at marching positions, which the per-frame trail detector can't drop without
// losing the channel, and a normal stack sigma-clip is too loose to remove). Soft-fail: on error,
// note it and stack the frames as-is. The mask is the run's biggest Go-side allocation (frame and
// residual planes); the deferred FreeOSMemory hands that RAM back before the Siril stack spawns —
// inside the containerized stack's shared VM, lazily-returned pages plus Siril's own budget were
// enough to exhaust it.
func applyTrailMask(opts Options, seqDir, regSeq string, ch *ChannelResult, satCeil []float32) {
	if opts.Preset == nil || (opts.Preset.TrailMaskK <= 0 && len(satCeil) == 0) {
		return
	}
	defer debug.FreeOSMemory()
	// Pure-Go pass with no tool output: bracket it with journal lines so the minutes it can take on
	// a large sequence read as work, not a hang.
	opts.report(Progress{Line: "▶ transient trail mask (cross-frame) …"})
	started := time.Now()
	summary, note, err := maskChannelTrails(seqDir, regSeq, opts.Preset.TrailMaskK, satCeil)
	if err != nil {
		ch.Selection.Notes = append(ch.Selection.Notes, "trail mask skipped: "+err.Error())
		return
	}
	opts.report(Progress{Line: "✓ transient trail mask done in " + time.Since(started).Round(time.Second).String()})
	if summary != nil {
		ch.TrailMask = summary
		ch.Selection.Notes = append(ch.Selection.Notes, note)
	}
}

// stackSelectedOrCopy stacks the surviving registered frames into outBase+".fits" — or, when only
// one frame registered at all, promotes that lone frame to the channel master directly: Siril's
// `stack -filter-incl` refuses fewer than two images, and one usable frame beats a dead channel.
// The note return tells the caller a degraded path was taken. Fewer than two survivors with two or
// more frames registered is a grading-contract violation (grade.Grade restores frames to the stack
// minimum) reported as a clear error instead of an opaque Siril script failure.
func stackSelectedOrCopy(ctx context.Context, runner *siril.Runner, seqDir, regSeq string,
	regCount int, rejected []int, outBase string, stack stackalg.Options,
	onProgress func(siril.Progress)) (*siril.Result, string, error) {
	if regCount == 1 {
		src := filepath.Join(seqDir, regSeq+"_00001.fits") // registered space is contiguous
		if err := fsutil.CopyFile(src, outBase+".fits"); err != nil {
			return nil, "", fmt.Errorf("promote lone registered frame: %w", err)
		}
		return nil, "only 1 frame registered — using it as the channel master (no stacking)", nil
	}
	if survivors := regCount - len(rejected); survivors < 2 {
		return nil, "", fmt.Errorf("%d of %d registered frames survived grading — below Siril's two-frame stack minimum", survivors, regCount)
	}
	// An algorithm Siril cannot run goes to the Go combiner over the frames Siril already registered.
	// It does NOT soft-fail back to Siril: silently substituting a different algorithm would make the
	// run's own provenance a lie.
	if stackalg.EngineFor(stack) == stackalg.EngineNative {
		note, err := stackNative(ctx, seqDir, regSeq, regCount, rejected, outBase, stack, onProgress)
		return nil, note, err
	}
	res, err := runner.Run(ctx, seqDir, siril.StackSelectedScript(regSeq, regCount, rejected, outBase, stack), onProgress)
	return res, "", err
}

// stackNative combines the surviving registered frames with the Go combiner (internal/stacknative)
// and returns a note naming the algorithm and the fraction of samples it rejected — the provenance
// the Siril path gets from Siril's own log.
func stackNative(ctx context.Context, seqDir, regSeq string, regCount int, rejected []int,
	outBase string, stack stackalg.Options, onProgress func(siril.Progress)) (string, error) {
	frames := registeredSurvivors(seqDir, regSeq, regCount, rejected)
	if len(frames) < 2 {
		return "", fmt.Errorf("native stack: %d surviving frames", len(frames))
	}
	res, err := stacknative.Stack(ctx, stacknative.Request{
		Frames:  frames,
		Out:     outBase + ".fits",
		Options: stack,
		OnProgress: func(done, total int) {
			if onProgress == nil || total == 0 {
				return
			}
			onProgress(siril.Progress{Percent: done * 100 / total})
		},
	})
	if err != nil {
		return "", fmt.Errorf("native stack: %w", err)
	}
	return fmt.Sprintf("stacked %d frames with the Go combiner (%s, %.2f%% of samples rejected)",
		res.Frames, res.Algorithm, res.Rejected*100), nil
}

// registeredSurvivors lists the registered frame files that survived grading. Siril names them
// <regSeq>_00001.fits upward in a contiguous registered space, and `rejected` holds 1-based indices
// into it — the same convention StackSelectedScript's `unselect` lines use.
func registeredSurvivors(seqDir, regSeq string, regCount int, rejected []int) []string {
	drop := make(map[int]bool, len(rejected))
	for _, i := range rejected {
		drop[i] = true
	}
	out := make([]string, 0, regCount-len(rejected))
	for i := 1; i <= regCount; i++ {
		if drop[i] {
			continue
		}
		out = append(out, filepath.Join(seqDir, fmt.Sprintf("%s_%05d.fits", regSeq, i)))
	}
	return out
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
// preReject (0-based input index → reason) carries upstream rejections — the cross-night
// transform-review gate — into the same selection, with their reason as run.json provenance.
// spans scope the RELATIVE grading rules per capture night in a multi-night merge (nil = one
// population): each night is judged against its own medians, so the sharpest night cannot evict
// every other night wholesale (task #354).
func gradeChannel(seqDir, baseSeq string, frames []*inspect.Frame, opts grade.Options, dropTransition bool,
	preReject map[int]string, spans []grade.Span) (metrics []grade.Metric, rejectedReg []int, regCount int, err error) {
	seq, err := grade.ParseSeq(filepath.Join(seqDir, baseSeq+"_.seq"))
	if err != nil {
		return nil, nil, 0, err
	}
	metrics = make([]grade.Metric, len(seq.Metrics))
	for i, sm := range seq.Metrics {
		m := grade.Metric{
			Index: i + 1, FWHM: sm.FWHM, WFWHM: sm.WFWHM, Roundness: sm.Roundness,
			Quality: sm.Quality, Background: sm.Background, StarCount: sm.StarCount,
			ShiftX: sm.ShiftX, ShiftY: sm.ShiftY,
		}
		if i < len(frames) {
			m.Path = frames[i].Path
		}
		switch {
		case sm.FWHM <= 0: // Siril could not register this frame
			m.Rejected = true
			m.RejectReason = "could not register (too few/elongated stars)"
		default:
			if reason, ok := preReject[i]; ok {
				m.Rejected = true
				m.RejectReason = reason
			} else if dropTransition && i < len(frames) && frames[i].WheelTransition {
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
	grade.GradeGrouped(metrics, spans, opts)

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

// warnLive records a run warning AND surfaces it in the live journal the moment it happens
// (⚠-prefixed, no step context — the job layer publishes it at the current progress), so a
// soft-failure is visible while the run is still going instead of only in the final report.
// boostLumCurve lifts the luminance curve's midtones by boost (the peak lift, at mid-grey) with
// the endpoints fixed — the gentle "brighter galaxy" knob, folded into the existing GIMP spline's
// control points so the compose stays one curve op. Two anchors (smoothsteps over the point's
// OUTPUT level) confine the lift to the galaxy's faint periphery and arms: a shadow anchor pins
// the sky-level points (the flattened sky keeps its exact brightness) and a highlight anchor pins
// the core/star points (bright detail keeps its full local contrast — raising the boost can never
// trade core detail for arm brightness). 0 → the curve unchanged, byte-identical.
func boostLumCurve(curve []float64, boost float64) []float64 {
	if boost <= 0 {
		return curve
	}
	if len(curve) < 4 { // no curve to build on → a plain midtone-lift spline
		curve = []float64{0, 0, 0.5, 0.5, 1, 1}
	}
	out := append([]float64(nil), curve...)
	for i := 1; i < len(out); i += 2 {
		y := out[i]
		w := smoothstep(0.05, 0.18, y) * (1 - smoothstep(0.60, 0.90, y))
		out[i] = math.Min(1, y+boost*4*y*(1-y)*w)
	}
	return out
}

func warnLive(opts Options, res *Result, msg string) {
	res.Warnings = append(res.Warnings, msg)
	opts.report(Progress{Line: "⚠ " + msg})
}

// warnChannel records a channel-scoped warning and surfaces it live; recordChannelOutcome
// promotes it to the run's warning list once the channel lands in the result.
func warnChannel(opts Options, ch *ChannelResult, msg string) {
	ch.Warnings = append(ch.Warnings, msg)
	opts.report(Progress{Line: "⚠ " + msg})
}

// recordChannelOutcome folds one stacked channel into the run result: its live-emitted warnings
// are promoted to run level, and a failed channel is announced in the journal immediately —
// previously the run went quiet and the miss surfaced only in the final warning list.
func recordChannelOutcome(opts Options, res *Result, filter string, ch ChannelResult) {
	res.Channels = append(res.Channels, ch)
	res.Warnings = append(res.Warnings, ch.Warnings...)
	if ch.Err != "" {
		warnLive(opts, res, fmt.Sprintf("channel %s failed — continuing without it: %s", filter, ch.Err))
	}
}

// gradeWarnings turns a channel's grading outcome into loud, immediate journal warnings: frames
// Siril silently failed to register, and rejected frames grading restored to honor the stack
// minimum. Quiet on a healthy channel.
func gradeWarnings(opts Options, ch *ChannelResult, filter string, metrics []grade.Metric, regCount, survivors int) {
	if regCount < len(metrics) {
		warnChannel(opts, ch, fmt.Sprintf("%s: only %d/%d frames registered — %d dropped (no stars matched)",
			filter, regCount, len(metrics), len(metrics)-regCount))
	}
	if note := restoredNote(metrics); note != "" {
		warnChannel(opts, ch, fmt.Sprintf("%s: stacking %d survivor(s) — %s", filter, survivors, note))
	}
}

// restoredNote summarizes grading restorations ("kept (stack minimum)") for the live warning;
// empty when no frame was restored.
func restoredNote(metrics []grade.Metric) string {
	var kept []string
	for _, m := range metrics {
		if m.Rejected {
			continue
		}
		if was, ok := strings.CutPrefix(m.RejectReason, grade.KeptStackMinimumPrefix); ok {
			kept = append(kept, was)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return fmt.Sprintf("%d kept despite: %s", len(kept), strings.Join(kept, "; "))
}

// ditherReport converts a channel's metrics into the pointing-pattern diagnosis. Frames Siril
// could not register (FWHM 0) carry no offset and are excluded; nil when too few frames remain.
func ditherReport(metrics []grade.Metric) *dither.Report {
	var shifts []dither.Shift
	for _, m := range metrics {
		if m.FWHM > 0 {
			shifts = append(shifts, dither.Shift{X: m.ShiftX, Y: m.ShiftY})
		}
	}
	return dither.Analyze(shifts)
}

// appendDitherAdvice surfaces ONE run-level warning when a channel's pointing pattern leaves
// fixed-pattern residuals correlated in the stack (static pointing or pure linear drift). The
// pattern is a property of the session, so repeating it per channel would be noise.
func appendDitherAdvice(res *Result) {
	for _, ch := range res.Channels {
		if ch.Dither.WalkingNoiseRisk() {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s: %s", ch.Filter, ch.Dither.Note))
			return
		}
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

// nightToken renders a capture-night key as a path/name suffix ("_n2023-02-27"); "" stays "" so
// single-night names are byte-identical to the pre-sessionization ones. Night keys are date-only,
// so they survive sanitize unchanged.
func nightToken(night string) string {
	if night == "" {
		return ""
	}
	return "_n" + sanitize(night)
}
