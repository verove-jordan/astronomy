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
ranking, multi-point alignment, AP-weighted stacking and real deconvolution.

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

## Algorithm, end to end

1. **Rank frames by sharpness** — `frameSharpness`/`diskSharpness`
   (`internal/planetary/planetary.go`): **full-resolution** Laplacian variance measured **over the
   lit disk only** (pixels above `bg + 0.25·(peak−bg)`), normalized by the disk's own dynamic
   range squared (scale-invariant). Ranking at native resolution is what makes "keep the best N %"
   select on real crater-level detail instead of coarse contrast.
2. **Keep the best `BestPercent`** (default 15) — `rejectLeastSharp`; every frame's score and
   kept/rejected verdict lands in the per-frame report.
3. **Surface-align with one resample** — `warpToSharpest` / `warpFrameToRef`
   (`internal/planetary/align.go`). The reference is the sharpest frame (written unresampled).
   Each other frame is measured, then resampled **exactly once**:
   - **seed**: brightness-weighted disk centroid difference;
   - **coarse**: ZNCC on 4×-downsampled blurred planes, ±16 small-px search (≈ ±64 full-res px of
     drift — clouds or a clipped limb can fool the centroid); skipped on small frames where the
     window couldn't cover the search;
   - **fine**: one full-res seeded ZNCC with **parabolic sub-pixel** refinement, ±8 px
     (`comet.AlignSeeded` — the shared starless aligner);
   - **multi-point (AP)** (`internal/planetary/apalign.go`, `Options.APAlign`): a **10×10
     alignment-point grid** (`apGridN`); each on-disk AP (disk mask at 0.25 of the dynamic range)
     measures its own absolute local shift (seeded at the global drift, ±6 px, window 5 % of the
     smaller axis); **outlier rejection** resets any AP > 3 px from the median shift back to the
     global baseline (a mislocked correlation must not bend the field); off-disk APs keep the
     global shift so the field stays continuous across the dark limb; the grid is 3×3-smoothed;
   - **warp**: a single **Catmull-Rom bicubic** resample of the original pixels through the
     bilinearly-interpolated displacement field (`warpByGrid` in `internal/planetary/warp.go`;
     edge-clamped, ≈ 0.98 MTF retained at half-Nyquist vs ≈ 0.53 for the old three-pass bilinear
     chain).
4. **Sharpness-weighted, sigma-clipped stack — in Go** (`stackWeightedFileAP` in
   `internal/planetary/stack.go`; Siril cannot weight a starless stack). Each frame's global
   weight is `(sharpness/max)^3`, floored at 0.02; with `Options.APWeights`, each frame also
   carries a **per-AP quality field** — per grid cell, `(cellSharpness/cellMax)^2` floored at 0.05
   (`apWeightFields` in `internal/planetary/apalign.go`), bilinearly interpolated per pixel — so
   **every region of the master is dominated by the frames that were sharpest there**
   (AutoStakkert-style multi-point quality). Two streaming passes: weighted mean/σ, then a
   2.5σ-clipped weighted mean (rejects satellites/cosmic rays); the master is normalized so the
   99.9th percentile maps to 1.0 (matching Siril `-output_norm`).
5. **Colour path** (R, G, B present): `coRegisterMasters` re-registers each channel master onto
   the reference (L when present) by centroid + ZNCC and a single Catmull-Rom `cubicShift`; then
   **Richardson-Lucy deconvolution of the luminance only** and `smoothChroma` box-blurs R/G/B
   (radius 3) — sharp L drives the detail, smooth chroma kills colour speckle. Mono: the single
   master is deconvolved.
6. **Deconvolution** — `deconvolveMaster` → `siril.DeconvolveLuminanceScript`
   (`internal/siril/scripts.go`), run on the **linear** master before any stretch:
   `load <master>` / `makepsf manual -gaussian -fwhm=2.8` / `rl -iters=18 -tv -alpha=700` /
   `save <master>`. Defaults 2.8 / 18 / 700 (`deconvFWHMDefault/ItersDefault/AlphaDefault`) — the
   old 3 / 10 / 1800 were so over-regularized the deconv was nearly a no-op.
7. **Persist masters** — `persistPlanetaryMasters` copies the stacked+deconvolved masters to
   `master_<label>.fits` in the run dir (the re-finish inputs; no re-stack, no re-deconvolution on
   refine).
8. **Objective acceptance** — the detail master's `diskSharpness` (`MasterLapVar`) is compared to
   the best kept input frame (`BestFrameLapVar`), same scale-invariant metric on both sides:
   **pass = master ≥ 1.05× best frame**; a miss appends a warning note ("check
   alignment/selection").
9. **Finish** — `siril.PlanetaryFinishScript`:
   - colour: `rgbcomp <R> <G> <B> -lum=<L> -out=<base>` (or without `-lum`), else `load <mono>`;
   - highlight-safe stretch `ght -D=0.6 -SP=0.18 -HP=0.85` ([HP,1] stays linear — bright
     craters/highlands never clip);
   - when sharpening: `wavelet 6 2`, `wrecons 1 2.2 1.8 1.1 1 1` (the mid-layer boosts scale with
     the `Sharpen` knob), `clahe 1.2 12`;
   - colour: `satu 0.8 0` (the "mineral Moon");
   - `savepng`/`savetif` per requested format.
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
| `APAlign` | true | 10×10 multi-point warp vs a pure global shift | C (`ap_align`) |
| `APWeights` | true | per-region quality weighting in the stack | — |
| `Formats` | png, tif | output formats | — |
| `DeconvFWHM` / `DeconvIters` / `DeconvAlpha` | 0 → 2.8 / 18 / 700 | Richardson-Lucy PSF width / iterations / TV regularization | C (`deconv_fwhm` 1–6, `deconv_iters` 5–40, `deconv_alpha` 300–5000) |
| `Finish.Stretch` | 0.6 | `ght -D` overall stretch | A (`stretch`, 0.1–1.0) |
| `Finish.Highlight` | 0.85 | `ght -HP` highlight protection | A (`highlight`, 0.5–0.98) |
| `Finish.Sharpen` | 1.0 | wavelet mid-layer boost scalar | A (`sharpen`, 0–2.5) |
| `Finish.Clahe` | 1.2 | CLAHE clip limit (local relief) | A (`clahe`, 0–4) |
| `Finish.Saturation` | 0.8 | mineral-colour boost (colour only) | A (`saturation`, 0–1.5) |

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
(re-finishing already-deconvolved masters avoids double-deconv). Knobs: the five finish controls
above (`stretch`/`highlight`/`sharpen`/`clahe`/`saturation`).

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
