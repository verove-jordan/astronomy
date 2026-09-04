# planetary

## What & when

Moon and planets via **lucky imaging**: capture hundreds/thousands of short exposures, keep only
the frames the atmosphere let through sharp, align them on the surface (no stars to register), and
stack so the master out-resolves any single frame. Inputs (`planetary.Process` in
`internal/planetary/planetary.go`):

- a **video** file — MP4/MOV/MKV/M4V/AVI (frames extracted by ffmpeg) or **SER** (read by Siril);
- a **folder of stills** — FITS, TIFF/PNG/JPEG, or camera raws, nested capture-software subfolders
  included;
- a folder shot through a **filter wheel** (≥ 2 distinct mono filters) becomes a colour
  **LRGB combine**; anything else is a single mono channel.

The whole alignment + stacking core is pure Go (`internal/planetary`); Siril is used for
conversion, Richardson-Lucy deconvolution and the finish. This replaced the earlier
"stack in Siril, use AutoStakkert! for demanding work" caveat — the in-house path does native-res
ranking, multi-point alignment, per-AP top-K selection stacking and real deconvolution.

## Detection & inputs

`sourceChannels` (`internal/planetary/planetary.go`):

- a **file** input routes by extension (video → ffmpeg `f_%05d.png` extraction; `.ser` → symlink;
  both then `siril.ConvertScript`: `convert vid -out=.`);
- a **folder** is classified by `inspect.ScanWithOptions`; lights grouped by filter. With ≥ 2
  distinct mono filters each group becomes a channel (FITS frames are used in place; other stills
  are staged + converted); otherwise every still under the folder (recursive, hidden dirs skipped)
  is staged as one mono sequence.
- The run/object name walks past generic capture buckets (`autorun`, `capture`, dates …) so
  `input/moon/autorun` → object `moon` (`objectName`).
- **Camera raws are debayered at conversion** (`convert vid -debayer`, `siril.ConvertDebayerScript`).
  Siril's plain `convert` decodes a CR2/NEF/DNG *without* demosaicing — correct for the deep-sky path,
  which debayers last inside `calibrate` — but this mode has no calibrate step, so an undemosaiced
  mosaic would be stacked, deconvolved and sharpened as a Bayer checkerboard. `anyCFARaw`
  (`inspect.IsCFARaw`, the one canonical extension list) decides; video and FITS inputs are untouched.
- **Long videos are decimated to a scratch budget.** A phone shooting 4K120 writes 33,000 frames in
  four minutes; extracted to PNG and Siril-converted (~50 MB of FITS per 4K colour frame) that is
  well over 100 GB for a stack that gains nothing past a few hundred well-chosen samples.
  `videoFrameBudget` is stated in PIXELS (`videoPixelBudget`, 5 Gpx, clamped to 600–6000 frames) so a
  small planetary ROI still keeps thousands of frames while a 4K clip is sampled with ffmpeg
  `framestep=N` — **evenly across the clip**, never truncated, so a hand sweep keeps its whole sweep.

## Panel mosaic — a capture that does not hold still

`internal/planetary/mosaic.go` + `canvas.go`. The lucky-imaging core registers every frame onto ONE
sharpest reference over a ±64 px search. That is right for a tracked planetary capture and wrong for
two ordinary lunar ones: a **hand-swept phone at high magnification**, where the Moon is far bigger
than the field and crosses it during the clip, and a **re-pointed DSLR burst series** covering a Moon
that overflows the frame. In both, frames minutes apart share no pixels, so there is no reference they
can all register onto — the stack collapses onto whatever overlapped the reference, or averages
unrelated surface into mush.

So the run is **measured first, then segmented**:

1. **Drift trajectory** (`trackDrift`) — consecutive frames are cross-correlated on decimated planes
   (`driftTrackDim`), each step **seeded by the previous one** (a sweep is continuous, so the search is
   a small residual rather than a brute-force hunt — the difference between tracking 1,500 frames in
   seconds and not at all). A step scoring under `driftGoodCorr` retries with a full-width search and
   then a **coarse whole-frame recovery** (`coarseRecover` → `shiftByOverlap`, scored over the two
   frames' actual OVERLAP, since a window centred on the frame cannot localize a large shift once half
   of it has fallen outside). That recovery is what keeps a re-point from being read as "barely moved"
   — an error every later frame would inherit, silently merging two pointings into one panel.
2. **The gate** — total drift within `driftSinglePanelFrac` (30%) of the frame ⇒ **one panel, the
   historical path, byte-identical master**. This is inert on a tracked capture.
3. **Segmentation** (`segmentDrift`) — otherwise the sweep is cut into panels each spanning at most
   `panelDriftFrac` of the frame (so a panel's frames always share ≥70% of their field), re-anchored
   every `panelStepFrac` so **consecutive panels overlap**. A runt tail is re-anchored on the last
   frame rather than merged into its predecessor (merging would push that panel past its drift budget
   — exactly the smear the segmentation exists to prevent). A sweep needing more than `maxPanels`
   widens its step rather than dropping its tail.
4. **Each panel stacks through the unchanged core** (`stackPanelFrames`: rank → keep best % → AP-align
   → weighted stack → double-stack), and its aligned frames are deleted as soon as its master exists.
5. **Canvas** (`assemblePanels`) — panels are placed from the trajectory, then each is refined against
   the panels already placed (incrementally, so panel N registers against N−1 rather than a panel it
   never overlapped), photometrically matched over the overlap by a robust two-percentile fit (phone
   auto-exposure walks with the lit fraction in frame), and blended with a smoothstep-feathered,
   coverage-weighted accumulation. The result is one master **larger than any input frame**.

The deep-sky mosaic (`internal/mosaic`) is deliberately NOT reused: it reprojects panels through a
plate solve onto a TAN sky grid, and the Moon has no stars to solve against — its surface *is* the
registration target.

Everything past segmentation soft-fails to the single-panel stack: a mosaic that cannot be assembled
must not cost the user the ordinary result. Knob: `panel_mosaic` (default on; named that way because
the `mosaic` wire key already means the deep-sky union canvas).

## Algorithm, end to end

1. **Rank frames by sharpness** — `frameSharpness`/`detailSNR`
   (`internal/planetary/quality.go`): **full-resolution, noise-corrected band-pass detail**
   measured **over the lit disk only** (pixels above `bg + 0.25·(peak−bg)`, eroded 4 px from the
   limb/terminator steps). The 2–4 px crater band (`box3 − box5`) is isolated, the analytically
   known share the frame's own noise contributes to that band is subtracted (σ from the MAD of
   `p − box3`), and the result is normalized by the disk's dynamic range squared
   (scale-invariant). Unlike the old Laplacian variance, a noisier frame can no longer outrank a
   sharper one — the same metric drives ranking, per-AP selection and the acceptance gate.
2. **Keep the best `BestPercent`** (default 15) — `rejectLeastSharp`; every frame's score and
   kept/rejected verdict lands in the per-frame report.
3. **Surface-align with one resample onto a CANONICAL geometry** — `warpToSharpest`
   (`internal/planetary/align.go` + `canonical.go` + `densefield.go`). The alignment reference is
   the sharpest frame, but the stack no longer inherits its frozen seeing warp: with ≥8 kept
   frames, sweep 1 measures every frame's field, the per-node **median field** isolates the
   reference's own atmospheric warp (the atmosphere averages to ~zero across frames; off-disk
   nodes anchor the DC so the limb stays continuous), and every frame — the reference included —
   is corrected onto the **distortion-free mean geometry** before the single resample. Each frame
   is measured, then resampled **exactly once**:
   - **seed**: brightness-weighted disk centroid difference;
   - **coarse**: ZNCC on 4×-downsampled blurred planes, ±16 small-px search (≈ ±64 full-res px of
     drift — clouds or a clipped limb can fool the centroid); skipped on small frames where the
     window couldn't cover the search;
   - **fine**: one full-res seeded ZNCC with **parabolic sub-pixel** refinement, ±8 px
     (`comet.AlignSeeded` — the shared starless aligner);
   - **multi-point (AP), two-level** (`internal/planetary/apalign.go` + `densefield.go`,
     `Options.APAlign`): a robust **10×10 coarse grid** (each structured on-disk AP measures its
     absolute local shift seeded at the global drift, ±6 px, window 5 % of the smaller axis;
     outlier rejection resets any AP > 3 px from the median back to the global baseline; 3×3
     smoothing) **seeds a dense ~120 px grid** (up to 32×32 — `denseGridN`) whose structured APs
     refine within ±3 px in 2 % windows — the sub-cell seeing warp a 10×10 grid cannot see
     (~465 px cells at 16 MP) no longer smears the average. **Featureless APs are vetoed at every
     density** (`vetoFeaturelessAPs` — a structure-free window returns a degenerate correlation
     that would poison the outlier median) and ride the baseline; off-disk APs keep the global
     shift so the field stays continuous across the dark limb;
   - **warp**: a single **Catmull-Rom bicubic** resample of the original pixels through the
     bilinearly-interpolated displacement field (`warpByGrid` in `internal/planetary/warp.go`;
     edge-clamped, ≈ 0.98 MTF retained at half-Nyquist vs ≈ 0.53 for the old three-pass bilinear
     chain) — landing on the **drizzle output grid** (`Options.DrizzleScale`, default **1.5×**,
     snapped to 1/1.5/2): hundreds of sub-pixel-dithered frames genuinely add resolution on the
     finer raster (fields are measured and stored in native units; only the output raster scales,
     ×scale² memory/time through warp, stack, deconv and finish; `drizzle_scale 1` = native).
4. **Per-AP-selected, sigma-clipped stack — in Go** (`stackWeightedFileAP` in
   `internal/planetary/stack.go`; Siril cannot weight a starless stack). With `Options.APWeights`,
   each grid cell ranks the kept frames by ITS OWN local sharpness and stacks only its locally-best
   ~25 % (min 6): the K-th best cell score is a soft logistic cutoff — ≈1 at/above, →0 below, **no
   floor** (`apSelectionFields` in `internal/planetary/apalign.go`) — so **every region of the
   master is built from only the frames that were sharpest there** (AutoStakkert-style per-AP
   selection; the old floored weighting let the soft majority inject ~10 % of every cell's flux and
   measurably blurred the master). The bilinear interpolation between cells doubles as the blend
   ramp; off-disk cells stay neutral so the sky/limb averages everything. The global weight
   flattens to `(sharpness/max)^1` when selection is on (`^3` floored at 0.02 otherwise). Two
   streaming passes: weighted mean/σ, then a σ-clipped weighted mean — 4σ under selection (a tight
   clip is biased against exactly the locally-sharpest samples), 2.5σ otherwise; the master is
   normalized so the 99.9th percentile maps to 1.0 (matching Siril `-output_norm`).
4b. **Double-stack reference** (`Options.DoubleStack`, ≥12 kept frames) — the ORIGINAL kept frames
   are re-registered onto the pass-1 master (`runDoubleStack`/`warpToMaster` in
   `internal/planetary/doublestack.go`): a low-noise, distortion-AVERAGED reference (the single
   sharpest frame imprints its own shot noise + seeing warp on the whole stack otherwise), with a
   **dense AP grid** (~120 px cells, 29×29 at 16 MP vs pass 1's 10×10) whose APs are **seeded by
   each frame's pass-1 field** (search only ±3 px, small windows). Still exactly one resample per
   frame; the re-stack replaces the master atomically (temp + rename) and any failure keeps the
   single-pass master with a note. Under drizzle the measurement reference is the scaled pass-1
   master brought back to the frames' native raster (one Catmull-Rom resize).
5. **Colour path** (R, G, B present): `coRegisterMasters` re-registers each channel master onto
   the reference (L when present) with the same **two-level dense field** the per-frame alignment
   uses (each channel stacked to a different instant — a global shift alone leaves colour fringes)
   and a single Catmull-Rom warp; then **Richardson-Lucy deconvolution of the luminance only** and
   `smoothChroma` smooths only the R/G/B **differences** (mean-preserving, radius 12 × drizzle
   scale) — sharp L drives the detail, smooth chroma kills colour speckle. Mono: the single master
   is deconvolved.
6. **Deconvolution** — `deconvolveMaster` → `siril.DeconvolveLuminanceScript`
   (`internal/siril/scripts.go`), run on the **linear** master before any stretch:
   `load <master>` / `makepsf manual -gaussian -fwhm=2.8` / `rl -iters=18 -tv -alpha=700` /
   `save <master>`. Defaults 2.8 / 18 / 700 (`deconvFWHMDefault/ItersDefault/AlphaDefault`) — the
   old 3 / 10 / 1800 were so over-regularized the deconv was nearly a no-op. The FWHM is a
   native-pixel quantity and scales with the drizzle grid (the same seeing blur spans scale× more
   output pixels).
7. **Persist masters** — `persistPlanetaryMasters` copies the stacked+deconvolved masters to
   `master_<label>.fits` in the run dir (the re-finish inputs; no re-stack, no re-deconvolution on
   refine).
8. **Objective acceptance** — the detail master's noise-corrected detail (`master_detail`,
   measured at native resolution — a drizzled master is resized back first) is compared to the
   best kept input frame (`best_frame_detail`), same metric on both sides: **pass = master ≥
   1.05× best frame**. The comparison always lands in the run notes; a miss appends a warning
   ("check alignment/selection").
9. **Finish** — `siril.PlanetaryFinishScript`:
   - colour: `rgbcomp <R> <G> <B> -lum=<L> -out=<base>` (or without `-lum`), else `load <mono>`.
     **With `Finish.TrueLum` (default on) the colour finish splits**: `PlanetaryComposeScript`
     (rgbcomp only) → Go `reimposeLuminance` rescales the linear composite so its mean luminance
     equals the deconvolved L exactly, chromaticity preserved (Siril's `rgbcomp -lum` leaks the
     soft un-deconvolved RGB lightness and dilutes the sharp L) → `PlanetaryToneScript` (the same
     tone chain, single shared writer so the paths can never drift);
   - highlight-safe stretch `ght -D=0.6 -SP=0.18 -HP=0.85` ([HP,1] stays linear — bright
     craters/highlands never clip); the `shadow_lift` knob slides the symmetry point into the
     shadows (`SP = 0.18·(1−s) + 0.04·s`) so crushed dark tones (crater floors, the terminator
     side) gain slope/detail — at 0 the emitted line is byte-identical to the historical `-SP=0.18`;
   - when sharpening: `wavelet 6 2`, `wrecons 1 2.2 1.8 1.1 1 1` (the mid-layer boosts scale with
     the `Sharpen` knob), `clahe 1.2 12`;
   - colour: `satu 0.8 0` (the "mineral Moon");
   - `savepng`/`savetif` per requested format.
   - **Earthshine reveal (opt-in, v2)** — with `Finish.EarthshineGain > 0` the finish becomes
     three stages: the same script saves a FITS instead, Go fits the full lunar disc
     (deterministic limb circle fit, `internal/planetary/disc.go`) and composites the unlit side
     from the linear masters (`internal/planetary/earthshine.go` + `esmask/estone/escolor.go`):
     the dark side is asinh-anchored (median → 0.10 × gain) with a **C¹ tanh shoulder toward an
     adaptive ceiling** (its P99.5 keeps Aristarchus-class features in relief instead of clipping
     flat), moderately starlet-denoised, **chroma-matched per pixel to the lit disc's own rendered
     tone**, and blended per channel as `out = finish + m·max(0, E·t − finish)` under an
     **illumination mask that is exactly 0 over the (dilated) lit surface** — the lit side is
     byte-identical by construction — and ramps up in master-value space precisely as fast as the
     tone saturates (no seam, no moat, no colour step). `Finish.EarthshineFeather` (fraction of
     the disc radius) widens the protected margin. Then `siril.ExportScript` writes the requested
     formats. Soft-fails into a run note (no disc found, full moon, dark side below the noise
     floor); `earthshine.json` records the fit, shoulder, mask and chroma-mode provenance.
     With the gain at 0 the historical single-script path is byte-identical.
10. **Run record** — `pipeline.ProcessPlanetary` (`internal/pipeline/planetary.go`) wraps the
    whole thing, captures stage previews and writes `run.json` into the run dir so the run is
    reopenable and refinable.

## Preset knobs & defaults

`mode.For(mode.Planetary)` sets `Preset.Planetary = planetary.Options{…}`
(`internal/planetary/planetary.go`); the finish tuning is `siril.PlanetaryFinish`
(`internal/siril/scripts.go`). Agent-tunable knobs are the planetary patch set
(`planetaryPatch` in `internal/pipeline/planetary.go`, applied by `applyPlanetaryParamPatch` in
`internal/pipeline/params_patch.go`).

| Knob | Default | What it does | Agent |
|------|---------|--------------|-------|
| `BestPercent` | 15 | keep this % of the sharpest frames | C (`best_percent`, 5–90) |
| `Sharpen` | true | wavelet sharpen + CLAHE in the finish (and gates the deconvolution) | — |
| `APAlign` | true | two-level multi-point warp (10×10 coarse → dense ~120 px refinement) + canonical geometry vs a pure global shift | C (`ap_align`) |
| `APWeights` | true | per-AP top-K frame SELECTION in the stack (each region stacks its locally-best ~25%) | — |
| `DoubleStack` | true | second alignment pass onto the pass-1 master (dense per-AP-seeded grid, AutoStakkert-style double-stack reference); soft-fails to the single-pass master | C (`double_stack`) |
| `Formats` | png, tif | output formats | — |
| `DeconvFWHM` / `DeconvIters` / `DeconvAlpha` | 0 → 2.8 / 18 / 700 | Richardson-Lucy PSF width / iterations / TV regularization (FWHM × drizzle scale) | C (`deconv_fwhm` 1–6, `deconv_iters` 5–40, `deconv_alpha` 300–5000) |
| `DrizzleScale` | 1.5 | super-resolution output grid (1 / 1.5 / 2, snapped); ×scale² memory/time | C (`drizzle_scale`, 1–2) |
| `AlignPoints` | 0 (auto) | dense distortion-grid density as a TOTAL reference-point count (per-axis N = √v, 10–48; auto = ~120 px cells capped at 32×32 — only the explicit knob may reach 48×48, cell ≈73 px on a 3520 px frame, still ≥ the ~70 px dense ZNCC patch) | C (`align_points`, 100–2304 or 0 = auto) |
| `Finish.Stretch` | 0.6 | `ght -D` overall stretch | A (`stretch`, 0.1–1.0) |
| `Finish.Highlight` | 0.85 | `ght -HP` highlight protection | A (`highlight`, 0.5–0.98) |
| `Finish.ShadowLift` | 0 (off) | slides the `ght` symmetry point into the shadows (`SP = 0.18·(1−s) + 0.04·s`): dark tones gain slope/detail, `[HP,1]` stays linear; 0 emits the historical `-SP=0.18` byte-identically | A (`shadow_lift`, 0–1) |
| `Finish.LimbBalance` | 0 (off; moon preset 0.55) | Go pre-stretch stage: compresses the smooth ILLUMINATION field of the lit surface (normalized-convolution base + tanh shoulder) so the bright limb keeps crater detail instead of burning white — local contrast is untouched, sky and terminator byte-identical | A (`limb_balance`, 0–1) |
| `Finish.Sharpen` | 1.0 | wavelet mid-layer boost scalar | A (`sharpen`, 0–2.5) |
| `Finish.Clahe` | 1.2 | CLAHE clip limit (local relief) | A (`clahe`, 0–4) |
| `Finish.Saturation` | 0.8 | mineral-colour boost (colour only) | A (`saturation`, 0–1.5) |
| `Finish.EarthshineGain` | 0 (off) | reveal the Moon's unlit side (earthshine v2): shoulder-toned, chroma-matched dark side under a hard-zero lit mask — the lit surface stays byte-identical | A (`earthshine_gain`, 0.2–2 or 0; user opt-in — the agent may tune it but never enable it) |
| `Finish.EarthshineFeather` | 0.006 | terminator protection margin, as a fraction of the disc radius (drives the lit-mask dilation) | A (`earthshine_feather`, 0.002–0.02) |
| `Finish.TrueLum` | true | colour runs re-impose the deconvolved L as the exact composite luminance (fixes the `rgbcomp -lum` leak) | A (`true_lum`) |

Tier A knobs re-render in seconds (`planetary.Refinish`); Tier C knobs require a re-stack and are
honoured via the agent's `retry_run_tuned` with `restack=true`.

## Soft-fail fallbacks

| Condition | Behavior |
|-----------|----------|
| A frame fails to read (corrupt) | skipped; the sequence stays gap-free and the rest stack |
| Acceptance miss (master < 1.05× best frame) | warning note in the result — the run still completes |
| Supervisor unreachable / supervised finish errors | standard finish; on refine, a plain `planetary.Refinish` runs instead (a refine never fails because of the agent) |
| Refine of a run predating the run-directory layout (no `run.json`) | a legible error asking to re-process (`refinePlanetary` in `internal/pipeline/refine.go`) |
| Unsupported input extension | hard error naming the accepted inputs |
| ffmpeg missing (video input) | the extraction fails with ffmpeg's own error; SER/stills paths are unaffected |

## AI supervisor

`superviseFinishPlanetary` (`internal/pipeline/planetary.go`) plugs the shared loop into a
single-stage renderer: each candidate is a `planetary.Refinish` over the persisted
stacked+deconvolved masters (PNG-only per iteration) — **no re-stack, no re-deconvolution**
(re-finishing already-deconvolved masters avoids double-deconv). Knobs: the finish controls above
(`stretch`/`highlight`/`shadow_lift`/`sharpen`/`clahe`/`saturation`, plus the earthshine pair and
`true_lum`).

Mode-specific deterministic scoring (`scoreFinishMode` in
`internal/pipeline/supervise_score.go`): the sky-colour penalties are dropped (a mineral Moon has
no "cast"), white clip is penalized (blown highlands/craters), black clip only beyond 30 % of
pixels (a dark sky border is normal — only a crushed disk hurts), and a **detail bonus/penalty
relative to pass 0** (`DetailIndex` gain ×4, capped ±2) rewards real acutance without letting it
buy back a blown render. Per-mode budgets: iteration cap as usual, Tier B 0 / Tier C 2
(`superviseBudgets` in `internal/pipeline/supervise_score.go`; in-run the renderer caps at
Tier A — the Tier-C budget applies to re-stack paths like `retry_run_tuned`). Iteration history,
best-image comparison, warm start, plateau stop, series campaigns and the chat tools work exactly
as in [deepsky](deepsky.md#ai-supervisor); `finalize` re-renders the winner into the canonical
`<object>_stack.*` with the run's full formats.

## Outputs & artifacts

Run directory `output/<object>/<runID>/`:

| Artifact | Content |
|----------|---------|
| `<object>_stack.png` / `.tif` | the finished image (per `Formats`) |
| `master_<L/R/G/B/mono>.fits` | persisted stacked+deconvolved linear masters (refine inputs) |
| `final_iter<N>.png` | per-iteration supervised renders |
| `previews/*.png` | milestone timeline (stacked masters + final) |
| `run.json` | projected run record (object, run id, final outputs/notes/iterations, stage previews, engine stamp) |

The **flat planetary result** returned to the job manager (and stored as the job result) carries
the full per-frame report (`frames[]`: index, file, filter, sharpness score, kept) and the
acceptance numbers `best_frame_lapvar` / `master_lapvar` (`planetary.Result` in
`internal/planetary/planetary.go`). Note these two fields live in the **job result**, not in the
on-disk `run.json` (which holds the projected `pipeline.Result`).

## Config/env

| Variable | Role |
|----------|------|
| `SIRIL_BIN` | conversion, RL deconvolution, finish |
| `FFMPEG_BIN` | video-frame extraction (MP4/MOV/MKV/AVI inputs) |
| `ASTRO_MAX_CPUS`, `ASTRO_SIRIL_MEM_RATIO` | Siril resource caps (the Go stack is in-process) |
| `ASTRO_LLM_*` | the opt-in finish supervisor |

Plate-solving/SPCC and GraXpert/StarNet do not apply to this mode.
