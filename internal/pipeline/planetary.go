// Planetary adapter + entry for the supervised finish. planetary.Process does the expensive lucky-
// imaging (rank → align → stack → deconvolve) and persists the stacked+deconvolved masters, so a
// candidate is a cheap re-finish (planetary.Refinish): the model tunes the stretch / wavelet sharpen /
// local contrast / saturation. ProcessPlanetary wraps planetary.Process so the manager gets the same
// flat planetary.Result whether or not the agent runs; single-stage (tierA), no re-stack.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/planetary"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// ProcessPlanetary runs the lucky-imaging pipeline, then — when the finish supervisor is opted in — re-
// tunes the finish over the persisted masters, keeping the best pass. Returns the flat planetary.Result
// (with the best outputs + the iteration timeline); soft-falls to the standard finish on any error.
// A percent stamper wraps opts.OnProgress FIRST, so every event this run emits — Siril log lines, the
// masters build, the per-frame ticks — carries the run's overall Index/Total and the job bar can never
// be reset to 0 by a Total-0 line event (the deep-sky stepper plays the same trick).
func ProcessPlanetary(ctx context.Context, opts Options) (*planetary.Result, error) {
	stamper := newPlanetaryStamper(opts.OnProgress)
	opts.OnProgress = stamper.forward // before sirilLines/planetaryCalibSource capture opts
	extras := &planetary.RunExtras{
		InputDirs: opts.scanRoots(),
		Calib:     planetaryCalibSource(opts),
		OnPercent: stamper.setPercent,
	}
	r, err := planetary.Process(ctx, opts.Runner, opts.FfmpegBin, opts.InputDir, opts.WorkDir, opts.OutputDir,
		opts.Preset.Planetary, extras, opts.sirilLines("planetary"))
	if err != nil {
		return nil, err
	}
	opts.PriorObject = r.Object // key for the supervisor's cross-run memory (warm start)
	if superviseOn(ctx, opts) && len(r.Masters) > 0 {
		if final, serr := superviseFinishPlanetary(ctx, opts, r); serr != nil {
			r.Notes = append(r.Notes, "supervised planetary finish failed, using standard finish: "+serr.Error())
		} else {
			r.Outputs = final.Outputs
			r.Iterations = final.Iterations
			r.Notes = append(r.Notes, final.Notes...)
		}
	}
	// Milestone previews + the durable run record, saved under the run dir (output/<object>/<runID>).
	if r.OutBase != "" {
		outDir := filepath.Dir(r.OutBase)
		captureStackedMasters(ctx, opts, outDir, r.Masters)
		captureFinalPNG(ctx, opts, outDir, &postprocess.Result{Outputs: r.Outputs})
		r.StagePreviews = collectStagePreviews(outDir) // persist the milestone timeline for reload
		// Persist run.json so the run is reopenable, shows in the gallery, and a post-run refine + full-S3
		// push resolve this run dir — matching ProcessOSC/ProcessComet. The flat planetary.Result is
		// projected onto the pipeline.Result contract the UI + refine read.
		projected := &Result{
			InputDir: opts.InputDir, OutputDir: outDir, Object: r.Object, RunID: r.RunID,
			Final: &postprocess.Result{
				Mode: "planetary", Outputs: r.Outputs, Notes: r.Notes, Iterations: r.Iterations,
			},
			StagePreviews: r.StagePreviews,
		}
		stampFinishQuality(projected) // objective clipping guardrails on every run
		writeRunJSON(outDir, projected)
	}
	return r, nil
}

// calMasterFrameCap bounds how many calibration frames feed one planetary master. Lunar captures often
// carry hundreds of 30+ MB cal TIFFs (1,100 flats ≈ 75 GiB once Siril-converted); 64 frames put the
// master's noise at frame-σ/8 — negligible against a ~50-frame lucky stack — for ~4 GiB of build scratch.
const calMasterFrameCap = 64

// planetaryStamper drives the planetary job bar. The manager computes the bar from
// Progress.Index/Total and OVERWRITES it on every event — a Siril log line (Total 0, Step set) would
// reset it to 0 — so the stamper holds the run's latest overall percent and stamps Index/Total:1000
// onto every forwarded event. It also serializes emissions: per-frame ticks arrive from parallel
// workers and the job progress sink chain is not goroutine-safe.
type planetaryStamper struct {
	mu    sync.Mutex
	index int
	emit  func(Progress)
}

func newPlanetaryStamper(emit func(Progress)) *planetaryStamper {
	return &planetaryStamper{emit: emit}
}

// setPercent records the run's overall completion (0..100) and pushes a bar update.
func (s *planetaryStamper) setPercent(p float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := int(p * 10)
	if s.emit == nil || idx == s.index {
		return
	}
	s.index = idx
	s.emit(Progress{Step: "planetary", Index: idx, Total: 1000})
}

// forward relays any pipeline event, stamping the held Index/Total onto step-only events so they can't
// zero the bar. Events that already carry a Total (a nested stepper) pass through untouched.
func (s *planetaryStamper) forward(pr Progress) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.emit == nil {
		return
	}
	if pr.Total == 0 {
		pr.Index, pr.Total = s.index, 1000
	}
	s.emit(pr)
}

// planetaryCalibSource wires the standard master machinery into a planetary run when the preset opts
// in: the capture's calibration sets are frame-capped, masters build (or reuse) through the same
// dispatcher as deep-sky, and the whole library is offered to the matcher so a later session without
// cal frames still calibrates. scratchDir lives under the run's planetary scratch root (wiped at the
// NEXT run's start, like all planetary scratch) — library-less runs keep their masters THERE, so it
// must never be removed mid-run. With a library configured, masters persist to the library dir instead.
func planetaryCalibSource(opts Options) *planetary.CalibSource {
	if opts.Preset == nil || !opts.Preset.Planetary.Calibrate || opts.Runner == nil {
		return nil
	}
	return &planetary.CalibSource{
		Exclude: opts.CalibExclude,
		Force:   opts.ForceCalibration,
		Build: func(ctx context.Context, inv *inspect.Inventory, scratchDir string) ([]calib.Master, []string, error) {
			workAbs, err := filepath.Abs(opts.WorkDir)
			if err != nil {
				return nil, nil, err
			}
			if err := fsutil.EnsureDir(scratchDir); err != nil {
				return nil, nil, err
			}
			masters, warns, err := buildRunMasters(ctx, opts, capCalibSets(inv, calMasterFrameCap), scratchDir, workAbs,
				func(step string) func(siril.Progress) { return opts.sirilLines(step) })
			if err != nil {
				return nil, warns, err
			}
			return appendLibraryMasters(ctx, opts, masters), warns, nil
		},
	}
}

// capCalibSets returns a copy of the inventory whose calibration sets are capped to at most n frames
// each — evenly spaced over the set, deterministic — so master builds stay disk/time-bounded. Light
// sets (and small cal sets) pass through untouched.
func capCalibSets(inv *inspect.Inventory, n int) *inspect.Inventory {
	out := *inv
	out.Sets = make([]inspect.Set, len(inv.Sets))
	for i, s := range inv.Sets {
		if s.Key.Type == inspect.Light || len(s.Frames) <= n {
			out.Sets[i] = s
			continue
		}
		capped := s
		capped.Frames = make([]*inspect.Frame, 0, n)
		step := float64(len(s.Frames)) / float64(n)
		var total int64
		for j := 0; j < n; j++ {
			fr := s.Frames[int(float64(j)*step)]
			capped.Frames = append(capped.Frames, fr)
			total += fr.ExposureMs
		}
		capped.Count = len(capped.Frames)
		capped.TotalIntegrationMs = total
		out.Sets[i] = capped
	}
	return &out
}

// appendLibraryMasters merges the persisted library masters into the freshly built list (dedup by
// path), so matching also sees masters from prior sessions whose raw cal frames are no longer around.
func appendLibraryMasters(ctx context.Context, opts Options, masters []calib.Master) []calib.Master {
	if opts.Library == nil {
		return masters
	}
	all, err := opts.Library.ListMasters(ctx)
	if err != nil {
		return masters
	}
	seen := make(map[string]bool, len(masters))
	for _, m := range masters {
		seen[m.Path] = true
	}
	for _, m := range all {
		if !seen[m.Path] {
			masters = append(masters, m)
		}
	}
	return masters
}

// superviseFinishPlanetary re-tunes the planetary finish under the agent, keeping the best pass.
func superviseFinishPlanetary(ctx context.Context, opts Options, r *planetary.Result) (*postprocess.Result, error) {
	pr := &planetaryRenderer{
		opts: opts, masters: r.Masters, sharpen: r.Sharpen,
		outDir: filepath.Dir(r.OutBase), outBase: r.OutBase,
		formats: opts.Preset.Planetary.Formats, orig: *opts.Preset,
	}
	if err := pr.ready(ctx); err != nil {
		return nil, err
	}
	return superviseFinish(ctx, opts, pr, filepath.Dir(r.OutBase))
}

// planetaryRenderer re-finishes a planetary run from its persisted stacked+deconvolved masters.
type planetaryRenderer struct {
	opts    Options
	masters map[string]string
	sharpen bool
	outDir  string
	outBase string   // canonical final base (<outDir>/<object>_stack) the winner is written to
	formats []string // the run's output formats (png/tif) for the final render
	orig    mode.Preset
}

func (c *planetaryRenderer) ready(ctx context.Context) error {
	if c.opts.Runner == nil {
		return fmt.Errorf("siril unavailable")
	}
	if len(c.masters) == 0 {
		return fmt.Errorf("no planetary masters to re-finish")
	}
	return c.opts.Runner.Available(ctx)
}

func (c *planetaryRenderer) firstTier() tier      { return tierA }
func (c *planetaryRenderer) maxTier(Options) tier { return tierA }

func (c *planetaryRenderer) render(ctx context.Context, working mode.Preset, _ tier, outBase string) (renderResult, []string, error) {
	// PNG only for the per-iteration render — that is all the metrics + model need to score.
	if _, err := planetary.Refinish(ctx, c.opts.Runner, c.outDir, c.masters, c.sharpen, working.Planetary.Finish, []string{"png"}, outBase); err != nil {
		return renderResult{}, nil, err
	}
	return renderResult{Png: outBase + ".png"}, nil, nil
}

func (c *planetaryRenderer) prompt(working mode.Preset, _ tier) supervisePrompt {
	return supervisePrompt{
		system:    planetarySystemPrompt,
		objective: planetaryObjective(),
		state:     finishState(working.Planetary.Finish),
		tiered:    false,
	}
}

func (c *planetaryRenderer) applyPatch(working mode.Preset, raw json.RawMessage, affordable tier) (mode.Preset, tier, bool) {
	next, t, changed := applyPlanetaryParamPatch(working, raw)
	if t > affordable {
		// Revert the unaffordable re-stack fields so the working preset never drifts from the render.
		next.Planetary.BestPercent = working.Planetary.BestPercent
		next.Planetary.APAlign = working.Planetary.APAlign
		next.Planetary.DoubleStack = working.Planetary.DoubleStack
		next.Planetary.Calibrate = working.Planetary.Calibrate
		next.Planetary.DeconvFWHM = working.Planetary.DeconvFWHM
		next.Planetary.DeconvIters = working.Planetary.DeconvIters
		next.Planetary.DeconvAlpha = working.Planetary.DeconvAlpha
		next.Planetary.DrizzleScale = working.Planetary.DrizzleScale
		next.Planetary.AlignPoints = working.Planetary.AlignPoints
		t = tierA
		changed = next.Planetary.Finish != working.Planetary.Finish
	}
	// Earthshine is a user opt-in: the agent may tune an enabled gain but never switch it on.
	if working.Planetary.Finish.EarthshineGain == 0 && next.Planetary.Finish.EarthshineGain != 0 {
		next.Planetary.Finish.EarthshineGain = 0
		changed = t == tierC || next.Planetary.Finish != working.Planetary.Finish
	}
	return next, t, changed
}

func (c *planetaryRenderer) params(p mode.Preset) map[string]float64 {
	return finishParams(p.Planetary.Finish)
}

// finalize re-renders the winning finish into the canonical output base with the run's full formats.
func (c *planetaryRenderer) finalize(ctx context.Context, _ Options, best *superviseIter,
	records []postprocess.IterationRecord, history []string, _ string) (*postprocess.Result, error) {
	res, err := planetary.Refinish(ctx, c.opts.Runner, c.outDir, c.masters, c.sharpen, best.preset.Planetary.Finish, c.formats, c.outBase)
	if err != nil {
		return nil, err
	}
	notes := append([]string{
		fmt.Sprintf("local AI agent planetary finish: %d iteration(s), best score %.1f", len(records), best.score),
	}, history...)
	return &postprocess.Result{
		Mode:       "planetary",
		Outputs:    res.Outputs,
		Notes:      append(notes, res.Notes...), // e.g. the earthshine applied/skipped status
		Iterations: records,
	}, nil
}

// planetaryPatch is the model's proposed change to the planetary run: the finish knobs (tierA
// Refinish, seconds) plus the stack/deconvolution knobs (tierC — a full re-stack).
type planetaryPatch struct {
	Stretch    *float64 `json:"stretch,omitempty"`
	Highlight  *float64 `json:"highlight,omitempty"`
	// ShadowLift slides the ght symmetry point 0.18→0.04 to open shadow tones; tierA (Refinish).
	ShadowLift *float64 `json:"shadow_lift,omitempty"`
	Sharpen    *float64 `json:"sharpen,omitempty"`
	Clahe      *float64 `json:"clahe,omitempty"`
	Saturation *float64 `json:"saturation,omitempty"`
	Headroom   *float64 `json:"headroom,omitempty"` // pre-stretch scale-down so the bright disk doesn't burn
	// LimbBalance compresses the smooth illumination field (bright limb) while preserving local
	// crater contrast; tierA (Refinish re-renders it from the pristine masters).
	LimbBalance *float64 `json:"limb_balance,omitempty"`
	// Earthshine lift of the Moon's unlit side. A user opt-in: the supervisor may tune a non-zero
	// gain but never switch it on (guarded in planetaryRenderer.applyPatch + the warm-start seed).
	EarthshineGain *float64 `json:"earthshine_gain,omitempty"`
	// EarthshineFeather is the terminator protection margin (fraction of the disc radius);
	// tierA — the composite re-renders in seconds. Harmless no-op while the gain is 0.
	EarthshineFeather *float64 `json:"earthshine_feather,omitempty"`
	// TrueLum re-imposes the deconvolved L as the exact composite luminance (colour runs; default
	// on). tierA — it only changes the finish flow.
	TrueLum *bool `json:"true_lum,omitempty"`

	// Tier C — re-stack from the source frames.
	BestPercent *int     `json:"best_percent,omitempty"`
	APAlign     *bool    `json:"ap_align,omitempty"`
	DoubleStack *bool    `json:"double_stack,omitempty"`
	Calibrate   *bool    `json:"calibrate,omitempty"`
	DeconvFWHM  *float64 `json:"deconv_fwhm,omitempty"`
	DeconvIters *int     `json:"deconv_iters,omitempty"`
	DeconvAlpha *float64 `json:"deconv_alpha,omitempty"`
	// DrizzleScale re-stacks onto a 1 / 1.5 / 2 output grid (snapped); ×scale² memory/time.
	DrizzleScale *float64 `json:"drizzle_scale,omitempty"`
	// AlignPoints overrides the dense distortion-grid density as a TOTAL point count (snapped to an
	// N×N grid, N=√v in 10..48); 0 = auto. tier C — it re-measures every frame's warp field.
	AlignPoints *int `json:"align_points,omitempty"`
}

func clampPlanetaryFinish(f siril.PlanetaryFinish) siril.PlanetaryFinish {
	f.Stretch = clampf(f.Stretch, 0.1, 1.0)
	f.Highlight = clampf(f.Highlight, 0.5, 0.98)
	f.ShadowLift = clampf(f.ShadowLift, 0, 1) // 0 = off (historical curve); 1 = SP fully into the shadows
	f.Sharpen = clampf(f.Sharpen, 0, 2.5)
	f.Clahe = clampf(f.Clahe, 0, 4)
	f.Saturation = clampf(f.Saturation, 0, 1.5)
	f.Headroom = clampf(f.Headroom, 0, 1) // 0 or 1 → no scaling; in-between reserves highlight room
	f.LimbBalance = clampf(f.LimbBalance, 0, 1)
	// ≤0 stays a clean "off"; when enabled the lift is bounded to a plausible ashen-light range.
	if f.EarthshineGain <= 0 {
		f.EarthshineGain = 0
	} else {
		f.EarthshineGain = clampf(f.EarthshineGain, 0.2, 2)
	}
	// 0 keeps the package default (0.006); a set feather is bounded like the knob range.
	if f.EarthshineFeather != 0 {
		f.EarthshineFeather = clampf(f.EarthshineFeather, 0.002, 0.02)
	}
	return f
}

func finishParams(f siril.PlanetaryFinish) map[string]float64 {
	return map[string]float64{
		"stretch": f.Stretch, "highlight": f.Highlight, "shadow_lift": f.ShadowLift,
		"sharpen": f.Sharpen,
		"clahe":   f.Clahe, "saturation": f.Saturation, "headroom": f.Headroom,
		"limb_balance":    f.LimbBalance,
		"earthshine_gain": f.EarthshineGain, "earthshine_feather": f.EarthshineFeather,
	}
}

func finishState(f siril.PlanetaryFinish) map[string]any {
	return map[string]any{
		"stretch": f.Stretch, "highlight": f.Highlight, "shadow_lift": f.ShadowLift,
		"limb_balance": f.LimbBalance,
		"sharpen":      f.Sharpen,
		"clahe":   f.Clahe, "saturation": f.Saturation, "headroom": f.Headroom,
		"earthshine_gain": f.EarthshineGain, "earthshine_feather": f.EarthshineFeather,
		"true_lum": f.TrueLum,
	}
}

func planetaryObjective() string {
	return "Produce a crisp, natural planetary/lunar image: fine surface detail (craters, cloud bands) " +
		"resolved and sharp without ringing halos or an over-sharpened, crunchy look; bright craters/limb " +
		"not blown to white; natural local contrast; and, for the Moon, subtle real mineral colour (not " +
		"garish). Change one control at a time, only when the image clearly needs it. Set done=true once " +
		"further sharpening or contrast would only add artefacts."
}

const planetaryIntro = `You are an expert planetary/lunar image-processing assistant. You are shown a rendered Moon or planet image (whole frame + a 100% centre crop) plus objective measurements, and you iteratively tune the finish to bring out real surface detail without introducing artefacts.

`

const planetaryKnobMenu = `You may tune these finish controls (each re-renders in seconds):
- stretch (0.1..1.0): overall brightness/contrast stretch. Higher lifts the mid-tones.
- highlight (0.5..0.98): highlight-protection point; higher keeps bright craters/limb from clipping to white.
- shadow_lift (0..1): opens the shadow tones (crater floors, the terminator side) by sliding the stretch focus toward black. 0 = the historical curve; raise it if dark areas are crushed/featureless; too high flattens mid-tone contrast and lifts noise at the disc edge.
- limb_balance (0..1): compresses the smooth illumination gradient of the lit surface (the burnt-white limb band) while local crater contrast stays intact. Raise it when the bright limb is washed out; 0 disables.
- sharpen (0..2.5): wavelet detail boost. 1.0 is the default; raise for more crater/band detail, lower if it rings or looks crunchy.
- clahe (0..4): local contrast / surface relief. Higher is punchier; too high looks harsh.
- saturation (0..1.5): colour saturation (the Moon's real mineral colour). 0 = grey; ~0.8 = natural.
- earthshine_gain (0.2..2, or 0 = off): brightness of the revealed earthshine (the Moon's unlit side). Only tunable when the run enabled it — you cannot switch it on.
- earthshine_feather (0.002..0.02): width of the protected margin at the lit boundary, as a fraction of the disc radius; larger keeps the earthshine lift farther from the lit edge.
- true_lum (true/false): colour runs re-impose the sharp deconvolved L as the exact output luminance after the RGB compose. Default true; disable only if it introduces colour artefacts.
- drizzle_scale (1, 1.5 or 2 — tier C re-stack): output super-resolution grid; ×scale² memory/time. Lower to 1 only if the run is resource-bound or the seeing never supports the finer grid.
- align_points (100..2304 total, or 0 = auto — tier C re-stack): number of stacking reference points for the atmospheric-distortion grid (snapped to N×N, N = √v in 10..48). More points track finer seeing waves at more CPU; the run form's estimator suggests a value from the image.`

const planetarySystemPrompt = planetaryIntro + planetaryKnobMenu + supervisorDefectRules
