# comet

## What & when

A **moving comet** shot with the mono per-filter rig: because the comet drifts against the stars
during the session, a single stack must smear either the comet or the stars. Comet mode stacks the
same calibrated frames **twice** — once star-aligned, once comet-aligned — and recomposites them so
the final image has a sharp coma/tail *and* pinpoint stars. Inputs are per-filter mono lights +
darks/flats/bias, like deepsky; frames need `DATE-OBS` timestamps (the motion track is fitted in
time). A genuine one-shot-colour comet is out of scope (v1).

## Detection & inputs

Same `inspect.ScanMany` classification as [deepsky](deepsky.md#detection--inputs) (headers, tokens,
wheel slots, `info.txt` legends, pixel stats), with one deliberate difference: comet mode does
**not** call `ExcludeBayer` — older ASICAP mono captures carry a spurious `BAYERPAT` header yet are
real mono-through-filter frames, so every filtered light is kept
(`pipeline.ProcessComet` in `internal/pipeline/comet.go`). The comet's position can be forced via
the preset's manual override (`CometX1/Y1/X2/Y2`, registered-frame pixel coordinates in the first
and last star-aligned frame); otherwise it is auto-detected.

## Algorithm, end to end

Sources of truth: `pipeline.ProcessComet` (`internal/pipeline/comet.go`) and `internal/comet`
(pure detection/fit/align/translate). Scripts from `internal/siril/scripts.go`.

1. **Masters** — `calib.BuildMasters` (same Winsorized master stacks as deepsky).
2. **Calibrate per set, merge into one sequence** (`calibrateAndMergeComet`). Each light set is
   calibrated *without* registration — `siril.CalibrateOnlyScript`: `link light -out=.`,
   `calibrate light -dark=<D> -flat=<F> -bias=<B> -cc=dark -prefix=pp_` — and every calibrated
   frame is symlinked into one merged sequence, keeping filter + `DATE-OBS` per frame.
3. **One global star alignment, pinned to the session-middle frame** —
   `siril.CalibrateStarAlignToRefScript`: `setref light <mid>`, `register light -2pass`,
   `seqapplyreg light`. All frames of all filters land in **one coordinate system**, so the
   comet's per-frame position is a single track and the per-channel star/comet masters need no
   later re-alignment. The middle reference minimizes drift (`comet.MiddleFrameIndex`).
4. **Grade** — `gradeMergedComet` reuses the deepsky grader over the merged sequence; on any
   grading error nothing is rejected (soft-fail — stack everything registered).
5. **Locate the comet and fit its motion track** (`cometTrack`):
   - the preset's **manual 2-point track** wins when fully specified (`comet.NewTrack`);
   - otherwise `detectComet` centroids the coma in up to **40 time-spread** registered frames via
     `comet.DetectFile` → **`DetectMultiScale`** (`internal/comet/detect.go`): the frame is
     box-blurred at three coma scales (radius 12 / 24 / 48 px — a compact bright coma locks at a
     small radius, a large diffuse one at a large radius), the most significant blurred peak wins
     (≥ 4σ above the original plane's MAD noise floor — deliberately permissive), and the
     flux-weighted centroid in a 40-px window is the observation;
   - **`comet.FitBestTrack`** (`internal/comet/fit.go`) fits a robust **linear** track x(t), y(t)
     (iterative least squares, dropping residual outliers beyond 4·MAD, ×4 rounds); for sessions
     longer than ~2 h with ≥ 6 detections it also fits a **quadratic** and keeps it only when it
     cuts the median residual by > 20 % (real curvature, not noise-chasing);
   - **consistency acceptance**: the track is trusted only with ≥ 4 surviving detections **and**
     at least two-thirds of the detections on the fit — a "track" through scattered noise would
     smear the comet stack worse than star-aligned-only. On rejection the run degrades to a
     star-aligned-only image with an explicit warning.
6. **Dual per-channel stacks** (`stackChannelsDual`), from the same globally-aligned frames:
   - **star stack** — `siril.StackAlignedScript`:
     `stack s rej winsorized 3 3 -norm=addscale -output_norm -out=star_master_<tag>` — the
     symmetric rejection clips the *moving* comet out of the star image;
   - **comet stack** — each surviving frame is translated so the coma lands at the mid-time
     position `p_mid` (`translateChannel` → `comet.TranslateFile`, sub-pixel bilinear), then
     `siril.StackCometScript` stacks with **asymmetric** rejection:
     `stack s rej winsorized 4 1.8 -norm=addscale -output_norm -out=comet_master_<tag>` — the coma
     is consistent frame-to-frame (a tight σ-high 1.8 never touches it) while the star trails
     marching through are bright one-or-two-frame HIGH outliers, which 1.8 rejects where the
     symmetric 3/3 left residual streaks; σ-low stays loose (4) so the faint tail's noisy low
     samples are never clipped;
   - **optional per-frame StarNet** (`Preset.CometPerFrameStarnet`, off by default): de-star every
     comet-aligned frame *before* the stack (`starlessCometFrames` — batched FITS↔TIFF around one
     StarNet pass per frame) for a zero-residual comet layer at the cost of minutes.
7. **Colour combine per side** (`combineComet`):
   `rgbcomp <R> <G> <B> [-lum=<L>] -out=star_color_lin`, `load`, `subsky <deg>`,
   `autostretch -linked -2.8 <bg>`, optional `satu <s> 0`, saved as `.fits`/`.tif`/`.png`
   (`star_color`, `comet_color`). Before the comet-side combine, `alignCometMasters`
   cross-registers each channel's comet master **on the coma itself** (windowed correlation at
   `p_mid`, window 70 px, ±16 px — `comet.AlignToReference`) to remove the small per-filter
   centroid bias the track fit leaves (the old visible R/G/B colour separation).
8. **Star-layer recomposite** (`compositeWithStarnet`):
   - StarNet removes stars from both colour images (`comet_starless`, `star_starless`);
   - the star layer is isolated — `siril.PixelMathScript`: `pm "$star_color$ - $star_starless$"`
     → `star_layer`;
   - the final composite **adds** it over the starless comet, clipped:
     `pm "min(1, $comet_starless$ + $star_layer$)"` → `comet_final` — ADD (not `max()`) preserves
     the faint tail *under* star halos, where max() would replace it with the star pixel.
9. **Record** — stage previews are collected and `writeRunJSON` writes the durable `run.json`
   (engine build stamp). Note: the run-level `finish_quality` stamp
   (`internal/pipeline/finishquality.go`) is wired into the deepsky/nebula finish only; on a
   comet run the deterministic finish metrics are computed per-iteration inside the supervised
   loop, not stamped on the run record.

## Preset knobs & defaults

`mode.For(mode.Comet)` (`internal/mode/preset.go`) **reuses the deepsky preset** (it runs the same
channel calibration/grading machinery) with three changes: `StarReduce = 0.5` (ensures StarNet is
wired — it separates the star layer), `Saturation = 0` (no boost on the comet composite by
default; the supervisor may raise it), and the comet-only knobs below. Agent-tunable knobs are the
comet patch set (`cometPatch` in `internal/pipeline/supervise_comet.go`, applied by
`applyCometParamPatch` in `internal/pipeline/params_patch.go`).

| Knob | Default | What it does | Agent |
|------|---------|--------------|-------|
| `BackgroundLevel` | 0.06 (deepsky) | autostretch target of both colour combines | A (`background_level`, 0.03–0.2) |
| `BackgroundDegree` | 1 (deepsky) | `subsky` degree in the combines | A (`background_degree`, 1–4) |
| `Saturation` | **0** | `satu` on the combines | A (`saturation`, 0–0.6) |
| `CometX1/Y1/X2/Y2` | 0 (auto-detect) | manual comet position in the first/last star-aligned frame | — |
| `CometPerFrameStarnet` | false | de-star every comet-aligned frame pre-stack | C (`per_frame_starnet`) |
| `Grade.*` (roundness/FWHM/background/star-count) | deepsky defaults | frame rejection | C (`roundness_floor`, `fwhm_sigma`, `background_sigma`, `star_count_frac`) |
| `TrailMaskK` | 3.0 (deepsky) | cross-frame transient mask | C (`trail_mask_k`) |
| `StarReduce` | 0.5 | wires StarNet for the layer separation | — |

Tier A knobs re-combine in seconds; Tier C knobs need a re-stack and are honoured via
`retry_run_tuned restack=true`.

## Soft-fail fallbacks

| Condition | Behavior |
|-----------|----------|
| No timestamped registered frames / detection too sparse or inconsistent | comet alignment skipped — **star-aligned-only** result, warning suggests a manual position |
| StarNet++ absent | the two rejection-cleaned stacks are composited directly — `pm "max($comet_color$, $star_color$)"` — with a warning that faint residuals may remain |
| StarNet fails on the comet image | export `comet_color` ("comet-aligned, StarNet failed") |
| StarNet fails on the star image | export `comet_starless` ("star layer failed") |
| Star-layer extraction or the final composite fails | export the best intermediate, note recorded |
| No star image could be combined | the supervised/standard finish returns nothing to composite — warning; per-channel stacks remain on disk |
| Per-frame StarNet errors (`CometPerFrameStarnet`) | that channel stacks **with** stars (the asymmetric rejection still cleans trails), warning |
| A channel fails to calibrate/stack | warning; the other channels continue |
| Grading fails | nothing rejected — all registered frames stack |
| Supervisor unreachable / errors | standard `finishComet` (byte-identical) |

## AI supervisor

`superviseFinishComet` (`internal/pipeline/supervise_comet.go`) plugs the shared loop into a
**single-stage** renderer: the expensive calibrate → dual-stack work is done, so each candidate is
a cheap `combineCometFinish` re-combine of the persisted `star_master_*`/`comet_master_*` with the
working `background_level` / `background_degree` / `saturation` (Tier A; per-mode budgets
Tier B 0 / Tier C 1 — the Tier-C knobs apply on a re-stack path such as
`retry_run_tuned restack=true`).

Deterministic scoring uses the deepsky/comet guardrails (`scoreFinishMode` in
`internal/pipeline/supervise_score.go`): full colour-cast/clipping penalties plus the star-tint
guard. Iteration history, best-image comparison, warm start keyed on the target, plateau stop,
autonomy-by-default, series campaigns and the chat tools (`get_mode_params`,
`view_result_image`, `retry_run_tuned` — `internal/agent/tools_params.go`) are the shared
machinery described in [deepsky](deepsky.md#ai-supervisor). The winner is promoted to
`comet_final.*`. A post-run refine (`refineComet` in `internal/pipeline/refine.go`) rebuilds the
master maps from disk; the persisted comet masters are already coma-aligned, so re-alignment is
skipped (zero `p_mid` signals it).

## Outputs & artifacts

Run directory `output/<object>/<runID>/`:

| Artifact | Content |
|----------|---------|
| `comet_final.{fits,tif,png}` | the recomposited final (sharp comet + star layer) |
| `star_master_<filter>.fits` / `comet_master_<filter>.fits` | the dual per-channel linear stacks (refine inputs) |
| `star_color.*` / `comet_color.*` | the stretched colour combines of each side |
| `comet_starless.*`, `star_starless.*`, `star_layer.fits` | StarNet intermediates |
| `final_iter<N>.*` | per-iteration supervised re-combines |
| `previews/*.png` | milestone timeline (a star-aligned frame, per-channel star masters, final) |
| `run.json` | run record: channels (star-stack counts), masters, detection summary, warnings (including the track-fit report: "motion track fitted from k/n detections", quadratic when curved), final outputs/notes/iterations, engine stamp |

## Config/env

| Variable | Role |
|----------|------|
| `SIRIL_BIN` | calibration, registration, stacking, pixel math, combines |
| `STARNET_BIN` | the star-layer separation (soft-fail to the rejection composite) |
| `GRAXPERT_BIN` | effectively unused: the comet stack path never runs GraXpert (the inherited `BackgroundAI` only influences the `subsky` degree choice and the up-front tool warning) |
| `ASTRO_FOCAL_MM` / `ASTRO_PIXEL_UM` / `ASTRO_SPCC_*` | not used by the comet combines (no SPCC here — the combines use `subsky` + linked autostretch) |
| `ASTRO_LLM_*` | the opt-in finish supervisor |
