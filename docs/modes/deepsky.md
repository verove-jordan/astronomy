# deepsky

## What & when

Galaxies and clusters shot at native focal length with a **monochrome camera through a filter
wheel** — per-filter light frames (L/R/G/B, optionally Ha/OIII/SII) plus darks, flats, bias.
Designed for the reference rig (Takahashi FC-100 DF + ZWO ASI 1600MM Pro; optics configured via
`ASTRO_FOCAL_MM`/`ASTRO_PIXEL_UM`), but any mono FITS capture works. One-shot-colour (Bayer)
frames found in the folder are **excluded** with a warning (`Inventory.ExcludeBayer` in
`pipeline.Process`) — they belong to the OSC path.

The output is an LRGB(+Ha) composite: sharp L luminance over colour, Ha screened in red over HII
regions, photometrically colour-calibrated, with a dark neutral sky.

## Detection & inputs

`inspect.ScanMany` (`internal/inspect/inspect.go`) walks the input folder(s) — multi-select merges
several folders into one inventory — reads every FITS header and classifies each file
light/dark/flat/bias/dark-flat/video, grouping into *sets* by object, filter, exposure, gain,
offset, temperature and binning. Classification is tiered:

1. **`IMAGETYP` header** (`classifyImageType` in `internal/inspect/classify.go`), filter from
   `FILTER` (normalized by `filterToken` — Johnson `V` maps to green, compound names abbreviate to
   L/R/G/B/Ha/OIII/SII).
2. **Filename/folder tokens** (`internal/inspect/filename.go`) — a `darks/`, `flats/`, `Bias_*`
   name is authoritative when the header is silent.
3. **EFW wheel slot** (`internal/inspect/sidecar.go`, `internal/inspect/wheelslot.go`) — the
   physical filter-wheel slot from a SharpCap sidecar or the filename is ground truth for mono
   captures with no `FILTER` card; `info.txt` acts as the slot→name **legend**.
4. **`info.txt` sidecars** (`internal/inspect/manifest.go`) — legacy captures with bare filenames
   carry a hand-written `info.txt`/`info.txt.txt` listing the per-sub-run filter order plus
   gain/exposure/temp (e.g. `LLL RR GG BB Ha Ha` / `gain L200 RGB250 Ha300`). It back-fills frames
   as a **fallback only**; header/filename always win.
5. **Pixel statistics** (`classifyByStats`) — frames still unlabeled are typed from their pixel
   curve compared across the session.
6. **Signal-based channel detection** (`internal/channeldetect`) infers filters for unlabeled
   lights; the Import UI's filter mapping (`Options.FilterMapping`) can override or exclude any
   detected filter.

## Algorithm, end to end

Source of truth: `pipeline.Process` in `internal/pipeline/pipeline.go`. All Siril scripts are built
by `internal/siril/scripts.go`; every script starts with `requires 1.2.0` / `setext fits`.

1. **Scan + name the run.** `inspect.ScanMany` → dominant `OBJECT` header, else the target folder
   name (`smartObject`). Output dir is `output/<object>/<runID>`. If the header has no RA/Dec,
   the target name (and each of its tokens, for compound folders like `M81_M82_2020`) is resolved
   to coordinates via `skycat.Resolve` so plate-solving gets a position seed.
2. **Up-front AI-tool warnings.** `aiToolWarnings` (`internal/pipeline/enhance.go`) records a run
   warning when a preset-enabled AI step cannot actually run (GraXpert fails its **deep health
   probe**, StarNet++ binary missing) so the fallback is visible, not a silent no-op.
3. **Catalog.** The scanned inventory is persisted (`store.SaveInventory`) so future runs can reuse
   these frames (cross-session integration — see `docs/pipeline.md`).
4. **Master calibration** (`internal/calib`). Per calibration set, Siril stacks a master:
   - darks/bias — `siril.StackMasterScript`: `link <seq> -out=.` then
     `stack <seq> rej winsorized 3 3 -nonorm -out=<master>`;
   - flats — `siril.StackFlatScript`: optional `calibrate <seq> -bias=<master> -prefix=pp_` then
     `stack … rej winsorized 3 3 -norm=mul -out=<master>`.
   With a database, `calib.BuildOrReuseMasters` reuses library masters; with the raw-frame catalog,
   `calib.BuildDeepMasters` pools raw bias/darks across sessions into deeper masters.
5. **Reuse plan + caches.** `buildReusePlan` folds prior light frames of the same target into the
   per-filter groups; `newFlatCache` keeps each session's own flats; `newParityCache`
   (`internal/pipeline/reuse_process.go`) plate-solves one frame per session with
   `siril.ParityProbeScript` (`platesolve … -noflip`) and reads the sign of det(CD) to detect a
   mirror-flipped optical train, fixing it with `siril.MirrorFramesScript` (`load` / `mirrorx` /
   `save` per frame).
6. **Per channel — calibrate + register.**
   - Single session (`processChannel`): `siril.CalibrateRegisterScript` —
     `link light -out=.`, `calibrate light -dark=<D> -flat=<F> -bias=<B> -cc=dark -prefix=pp_`,
     `register pp_light`.
   - Heterogeneous groups (different gain/session/parity, `processChannelGroups` in
     `internal/pipeline/reuse_process.go`): each group is calibrated with its own gain-matched
     masters, parity-normalized, then registered with `siril.CalibrateRegisterFramedScript` —
     `register light -2pass -transf=homography` + `seqapplyreg light -framing=min` (common field
     of view).
7. **Grade** (`gradeChannel` in `internal/pipeline/pipeline.go` + `internal/grade`). Per-frame
   metrics come from the registered sequence's `.seq` (FWHM, wFWHM, roundness, background, star
   count) plus a pure-Go Hough **trail detector** run on a 512-px downsample of each calibrated
   frame. `grade.Grade` rejects on: roundness floor, FWHM/background median+MADσ outliers, star
   count below a fraction of the median (clouds), detected trails, plus unregistrable frames and
   the optional filter-wheel-transition first frame.
8. **Cross-frame transient mask** (`maskChannelTrails` in `internal/pipeline/trailmask.go`,
   `internal/transient`; enabled by `Preset.TrailMaskK > 0`). Across the registered subs, any pixel
   above its per-pixel median + k·MADσ (a slow-satellite trail segment, cosmic ray or hot pixel) is
   replaced by the median before stacking.
9. **Stack the survivors.** `siril.StackSelectedScript`: `select r_pp_light 1 <N>`, one
   `unselect r_pp_light <i> <i>` per rejected frame, then
   `stack r_pp_light rej winsorized 3 3 -norm=addscale -output_norm -filter-incl -out=master_<tag>`
   (the multi-group path adds `-weight=wfwhm`). A spurious `BAYERPAT` inherited from old ASICAP
   mono captures is stripped from the master (`fits.StripKeyword`).
10. **Per-channel linear finishing** (`finishStackedChannel`):
    - **GraXpert background extraction** (`extractBackgroundAI` in `internal/pipeline/enhance.go`)
      on the linear master when `Preset.BackgroundAI` is on **and** `Graxpert.Healthy` passes the
      deep probe (a present-but-broken GraXpert never captures this path);
    - **denoise** — `siril.DenoiseScript`: `denoise -vst -mod=<m>` (chroma harder than luminance:
      `DenoiseChroma` for R/G/B/Ha, `DenoiseLum` for L);
    - a quick preview PNG (`siril.PreviewScript`).
11. **Co-register channels** (`alignChannels`). The per-channel masters are symlinked in L-first
    order and registered together with `siril.AlignMastersScript` (`link ch -out=.`,
    `register ch`); results are copied to `aligned_<tag>.fits`. `channelMastersMap` substitutes
    **OIII as the blue channel** when there is no B filter (warning recorded).
12. **Finish** (`finishAligned` → `finishWithGimp` → `prepGimpInputs`, all in
    `internal/pipeline/pipeline.go`). The RGB base is built linear and calibrated before any
    stretch:
    1. `rgbcomp <R> <G> <B> -out=rgb_base`, `load rgb_base`, `subsky <deg>` — degree from
       `backgroundDegree` (`internal/pipeline/enhance.go`): **1** when GraXpert already extracted
       (gentle cleanup + safety net), else the preset degree clamped to Siril's valid [1,4].
    2. **Combined background pass** (`extractCombinedBackground` in
       `internal/pipeline/enhance.go`, gated by `Preset.CombinedBackgroundAI`): a second GraXpert
       extraction on the combined linear RGB removes the residual large-scale colour gradient the
       per-channel pass leaves, then a follow-up Siril RBF subsky
       (`subsky -rbf -smooth=0.5 -samples=30 -tolerance=1.5 -dither`) cleans what GraXpert leaves.
       **The RBF pass always runs when the AI pass fails at runtime** (previously an early return
       shipped un-flattened skies), and runs alone when GraXpert is absent/unhealthy.
    3. **AI colour denoise** (`denoiseAI`, gated by `Preset.ColorDenoiseAI` + GraXpert health):
       GraXpert's edge-preserving denoiser on the combined linear RGB, before SPCC amplifies the
       chroma noise.
    4. **Colour-calibration ladder** (`postprocess.ColorCalibrate` in
       `internal/postprocess/colorcal.go`):
       1. **plate-solve + SPCC** — `siril.ColorCalibrateScript`: optional
          `set core.catalogue_gaia_astro=…` / `set core.catalogue_gaia_photo=…` (offline Gaia),
          `platesolve [<coords>] -focal=<mm> -pixelsize=<µm> [-catalog=localgaia]`, then
          `spcc "-monosensor=ZWO ASI1600MM" "-rfilter=…" "-gfilter=…" "-bfilter=…"
          "-whiteref=Average Spiral Galaxy"` (whole-token quoting — Siril's tokenizer requires it).
          Bounded by a 4-minute timeout; the Siril log is scanned for solve-failure markers.
       2. **star-field photometric fallback** (`postprocess.StarFieldCalibrate` in
          `internal/postprocess/starcal.go`) — when SPCC cannot run, per-channel **gains** are
          estimated from the field's own stars: detect up to 500 unsaturated point sources
          (8-MADσ peaks, width-filtered against galaxy knots), take the median R/G and B/G aperture
          flux ratios, and normalize them to 1 (median star ≈ neutral), gains clamped to [0.5, 2].
          Applied in place as `pix' = (pix − bg)·gain + neutral pedestal`. Needs ≥ 20 usable stars.
       3. **background neutralization** — last resort: `siril.NeutralizeScript` (`subsky 1`,
          `rmgreen 0`), which only equalizes the sky pedestal.
       SPCC and the star-field fallback both count as *calibrated* (a trustworthy channel balance).
    5. **Stretch** — `rmgreen 0` (SCNR green removal) then `siril.AutostretchCmd`:
       `autostretch [-linked] -2.8 <targetBg>` with the dark preset background (0.06). The stretch
       is **linked only when calibration succeeded** — on the neutralization fallback an unlinked
       stretch equalizes the channels toward neutral instead of locking in the imbalance.
    6. **L and Ha layers** are stretched separately (no colour calibration): L gets
       `subsky <deg>` + unlinked autostretch at `targetBg`; Ha gets **RBF subsky**
       (`Preset.HaRBF`, default on — a degree-1 plane cannot model the asymmetric amp-glow gradient
       that would screen as a red blotch) + unlinked autostretch at a darker target
       (`max(0.03, 0.6·targetBg)`).
13. **GIMP compose** (`gimp.BuildImage` / `composeScript` in `internal/gimp/compose.go`), driven
    over the resident Script-Fu server:
    - optional **ChromaBlur** gaussian on the RGB base (LRGB only — L restores all detail);
    - **L layer**: `LumCurve` spline (galaxy brightness from the clean luminance), then the
      **core-highlight shoulder** (`coreShoulderLUT`: exact identity below `CoreHighlightKnee`,
      tanh roll-off to `CoreHighlightCeil` — dims a blown nebula core into a structured knot),
      blend mode `LAYER-MODE-LUMINANCE`;
    - **Ha layer**: optional median-blur 8 (`HaExcludeStars` — the red screen lifts only extended
      nebulosity), black-point clip at `HaBlackPoint`, green+blue channels zeroed (pure red),
      blend `LAYER-MODE-SCREEN` at `HaScreen` opacity;
    - layered **`.xcf`** saved with all layers, full frame;
    - flattened copy: value `Curve` spline; a gentle green-saturation trim (−12) **only when the
      colour was never photometrically calibrated** (`CalibratedColor` skips it — trimming green on
      an SPCC-balanced image tips it magenta); `Saturation` boost; the **star-safe highlight
      shoulder** (`HighlightKnee`/`HighlightCeil`, per-channel tanh — bright star cores never burn
      to white and keep colour) as the last tone op; `CropFrac` edge crop; export `.tif` + `.png`.
14. **StarNet++ star reduction** (`reduceStarsAI` in `internal/pipeline/enhance.go`, gated by
    `Preset.StarReduce > 0` + binary availability): stars removed from the flattened composite,
    then blended back at `StarReduce` opacity (`gimp.ReduceStars`) →
    `final_reduced.{tif,png}` + `final_starless.tif`.
15. **Finish-quality stamp** (`stampFinishQuality` in `internal/pipeline/finishquality.go`) — on
    **every** run, supervised or not: the exported PNG is measured (`measureFinish` in
    `internal/pipeline/finishmetrics.go`) and the snapshot is stored as
    `final.finish_quality` in `run.json`, with run warnings for threshold breaches
    (warm cast > 0.015, |signal cast| > 0.03, green cast > 0.02, white clip > 1 %).
16. **Persist.** Stage previews are collected from `previews/`, and `writeRunJSON` writes the
    self-contained `run.json` (stamped with the engine build, `internal/buildinfo`).

## Preset knobs & defaults

From `mode.For(mode.Deepsky)` in `internal/mode/preset.go`. The **Agent** column marks knobs
tunable through the shared param brain (`ParamsFor`/`ApplyParamPatch` in
`internal/pipeline/params_patch.go` — the deepsky/nebula patch set, used identically by the in-run
supervisor, `RunRequest.Params` and the AstroAgent chat tools), with the supervisor re-entry tier
each knob requires.

| Knob | Default | What it does | Agent |
|------|---------|--------------|-------|
| `Grade.RoundnessFloor` | 0.55 | reject frames with elongated stars below this roundness | C (`roundness_floor`) |
| `Grade.RoundnessSigma` / `FWHMSigma` / `BackgroundSigma` | 2.5 / 2.5 / 3.0 | median+MADσ rejection thresholds | C (`fwhm_sigma`, `background_sigma`) |
| `Grade.StarCountFrac` | 0.5 | reject frames with < frac·median stars (clouds) | C (`star_count_frac`) |
| `BackgroundDegree` | 1 | Siril `subsky` polynomial degree at finish (clamped 1–4) | B (`background_degree`) |
| `HaScreen` | 0.42 | Ha layer screen opacity | A (`ha_screen`) |
| `Saturation` | 0.12 | final saturation boost | A (`saturation`) |
| `Curve` | gentle S | value curve on the flattened composite | — |
| `LumCurve` | galaxy lift | curve on the L luminance layer | — |
| `CoreHighlightKnee` / `Ceil` | 0.64 / 0.76 | L-luminance shoulder taming a blown nebula core | A (`core_highlight_knee/ceil`) |
| `HighlightKnee` / `Ceil` | 0.85 / 0.96 | star-safe per-channel highlight shoulder (last tone op) | A (`highlight_knee/ceil`) |
| `DenoiseChroma` / `DenoiseLum` | 0.85 / 0.50 | Siril `denoise` modulation on the linear masters (VST + DA3D on) | C (`denoise_chroma/lum`) |
| `ChromaBlur` | 0 | GIMP colour-base blur (GraXpert denoise replaces it) | A (`chroma_blur`) |
| `CropFrac` | 0.035 | trim ragged stacking edges off the export | A (`crop_frac`) |
| `TrailMaskK` | 3.0 | cross-frame transient mask threshold (0 = off) | C (`trail_mask_k`) |
| `BackgroundAI` | true | per-channel GraXpert background extraction | C (`background_ai`) |
| `CombinedBackgroundAI` | true | 2nd GraXpert pass + RBF on the combined RGB | B (`combined_background_ai`) |
| `ColorDenoiseAI` | true | GraXpert denoise on the combined linear RGB | B (`color_denoise_ai`) |
| `StarReduce` | 0 | StarNet++ star reduction opacity (0 = full stars) | B (`star_reduce`) |
| `HaExcludeStars` | true | median-remove stars before the red Ha screen | A (`ha_exclude_stars`) |
| `DropFilterWheelTransition` | true | drop the off-brightness first frame of a wheel move | — |
| `ColorCalibration` | true | run the SPCC → star-field → neutralization ladder | B (`color_calibration`) |
| `LinkedStretch` | true | linked autostretch (honoured only when calibrated) | B (`linked_stretch`) |
| `BackgroundLevel` | 0.06 | autostretch target sky background | B (`background_level`) |
| `HaBlackPoint` | 0.12 | Ha layer black-point clip before the screen | A (`ha_black_point`) |
| `HaRBF` | true | RBF-flatten the Ha layer instead of the polynomial | — |
| `Previews` | true | per-channel + milestone preview PNGs | — |
| `Supervise` / `SuperviseMaxIters` / `SuperviseTier` / `SuperviseTargetScore` / `SuperviseConfirmRestack` | off / 0→4 / ""→C / 0→7.0 / **false** | the opt-in AI finish supervisor (see below) | — |

## Soft-fail fallbacks

| Condition | Behavior |
|-----------|----------|
| GraXpert absent, `--no-ai`, or **unhealthy** (deep probe fails — e.g. missing ONNX runtime) | per-channel extraction skipped; the combined pass runs the deterministic **RBF subsky** alone; up-front run warning (`aiToolWarnings`) |
| GraXpert healthy but a pass **errors at runtime** | note recorded; for the combined pass the **RBF subsky still runs** (`extractCombinedBackground`) |
| Plate-solve/SPCC fails, times out (4 min), or is offline | **star-field photometric gains** (`postprocess/starcal.go`); note names the applied gains |
| Star-field fallback finds < 20 usable stars or errors | **background neutralization** (`subsky 1` + `rmgreen 0`) + **unlinked** stretch; note recorded |
| StarNet++ absent or fails | full stars kept; warning; a failed blend still keeps `final_starless.tif` |
| GIMP absent or the compose fails | Siril finish via `postprocess.Combine` (`rgbcomp` + colour ladder + stretch), warning |
| Cross-channel alignment fails/incomplete | unaligned masters are composited, warning |
| Trail mask or denoise errors | channel stacks as-is, note in `Selection.Notes` |
| No B filter but OIII present | OIII used as the blue channel, warning |
| Supervisor model server unreachable / any supervised error | standard single-pass finish (byte-identical to a non-supervised run) |
| Catalog write fails | run continues, warning (no cross-session record) |
| One channel fails entirely | recorded as a channel error + warning; the other channels still combine |

## AI supervisor

Opt-in per run (Import checkbox, `process … --supervise`, `just refine <run-dir>`). Gate:
`superviseEnabled` (`internal/pipeline/supervise.go`) — preset opted in **and** the model server
(`internal/llm`, `ASTRO_LLM_URL`) answers **and** GIMP is available.

- **Loop** (`superviseFinish` in `internal/pipeline/supervise.go`): render → `measureFinish` →
  deterministic score → vision-model `critique` → apply the model's JSON patch at the cheapest
  affordable tier → repeat, keeping the best pass. Pass 0 is the tier-B baseline (the standard
  finish). Combined score = `0.6·metrics + 0.4·model`, so a clipped/cast render can never win on
  the model's vote alone.
- **Critique context** (`internal/pipeline/supervise_critique.go` +
  `internal/pipeline/supervise_history.go`): every prompt embeds the **iteration history** (last 6
  passes: per-pass param *diffs*, scores, top defects, a `← BEST so far` marker) and — when the
  current render is not the best — a thumbnail of the **best image so far** for direct visual
  comparison (`attachBestPreview`). The user's free-text run goal steers the first critique; live
  nudges (`Options.Steer`) steer later ones.
- **Deterministic scoring** (`scoreFinishMode` in `internal/pipeline/supervise_score.go`): the
  shared `scoreFinish` guardrails (black/white clip, green/warm/signal cast, background off
  target) **plus the deepsky star-tint guard** — a penalty when > 60 % of bright star cores are
  uniformly warm.
- **Warm start** (`warmStart` in `internal/pipeline/supervise_history.go`): the working preset is
  seeded from the best prior supervised pass of the **same target** (persisted per-iteration in the
  `finish_iterations` table with the full versioned preset blob; only priors with deterministic
  score ≥ 6.0 qualify), re-applied through the mode's own clamp path. `ASTRO_SUPERVISE_HISTORY=off`
  disables history + warm start.
- **Tiers & budgets** (`internal/pipeline/supervise_reentry.go`, budgets in
  `supervise_params.go`/`supervise_score.go`): Tier A re-renders only the GIMP composite from the
  cached linear prep; Tier B re-runs the linear prep (combine/gradient/SPCC/stretch); Tier C
  re-stacks from the raw frames (`Options.Reprocess` → `reStack`). Deepsky budgets: **3 × Tier B,
  2 × Tier C** on top of the iteration cap (default 4, hard max 8).
- **Stop policy**: the model's `done` is honoured only when the deterministic score clears the
  target (default 7.0, `SuperviseTargetScore`); a **plateau stop** ends the loop after two
  consecutive non-improving passes (from pass 3 on).
- **Autonomous by default**: the Tier-C re-stack confirmation gate is opt-in
  (`Preset.SuperviseConfirmRestack`, default false) — the loop runs within its budgets without
  mid-run questions.
- **Finalize**: the winning pass is promoted to `final.*`; StarNet++ runs once on the winner with
  the winner's (possibly tuned) `StarReduce`.
- **Series campaigns**: a durable "keep improving this target" campaign is an `agent_series` row
  (`internal/store/series.go`) whose attempts are ordinary jobs linked by `series_id`, driven over
  `/api/series` (`internal/api/series.go`: create / list / get / continue / stop), with
  `auto_continue`, `max_attempts`, `target_score` and best-job tracking.
- **Agent chat tools** (`internal/agent/tools_params.go`): `get_mode_params` returns the mode's
  tunable surface + knob menu; `view_result_image` attaches a run's final image (full frame +
  100 % centre crop) plus objective measurements to the chat model's context;
  `retry_run_tuned` re-processes with chosen params — `restack=true` replays the full run,
  `restack=false` re-finishes only (Tier B refine) — inheriting the target's warm-start memory.

## Outputs & artifacts

Run directory `output/<object>/<runID>/`:

| Artifact | Content |
|----------|---------|
| `final.xcf` | layered GIMP project (RGB base + L luminance + Ha screen), full frame |
| `final.tif` / `final.png` | flattened, curved, cropped export |
| `final_starless.tif`, `final_reduced.{tif,png}` | StarNet++ artifacts (when `StarReduce > 0`) |
| `master_<filter>.fits` | per-channel linear stacks (background-extracted + denoised) |
| `aligned_<filter>.fits` | co-registered channel masters (the refine/Tier-C inputs) |
| `master_<filter>_preview.png` | quick per-channel previews |
| `rgb_base.fits` | the combined, gradient-removed, colour-calibrated linear RGB |
| `previews/*.png` | milestone timeline (stacked / aligned / combined / colour-calibrated / star-reduced / final) |
| `final_iter<N>.png` | per-iteration supervised renders (supervised runs only) |
| `run.json` | the self-contained run record: input/output dirs, object, run id, per-channel results + per-frame metrics, masters used, channel detection, reuse summary, warnings, `final` (outputs, notes — including which colour-calibration rung ran — `iterations`, **`finish_quality`**), `stage_previews`, and the **`engine`** build stamp (`internal/buildinfo`, "dev" for unstamped binaries) |

`astrostack refine <run-dir>` (`pipeline.RefineExistingRun` in `internal/pipeline/refine.go`)
re-runs only the finish from the on-disk `aligned_*`/`master_*` — no re-stack.

## Config/env

See `.env.example` for the full annotated list. Most relevant to this mode:

| Variable | Role |
|----------|------|
| `SIRIL_BIN`, `GIMP_BIN`, `GIMP_HOST`/`GIMP_PORT` | the two core engines (host apps / Script-Fu server) |
| `GRAXPERT_BIN` | GraXpert CLI. The engine **deep-probes** it (a real tiny extraction) — a present-but-broken install (typically `No module named 'onnxruntime'`) is treated as absent and shown in `/api/environment`. Fix a pipx install with `pipx inject graxpert onnxruntime`. |
| `STARNET_BIN` | StarNet++ CLI (soft-fail to full stars) |
| `ASTRO_FOCAL_MM`, `ASTRO_PIXEL_UM` | rig optics for plate-solving |
| `ASTRO_SPCC_SENSOR/RFILTER/GFILTER/BFILTER/WHITEREF` | SPCC database names — must match Siril's `siril-spcc-database` entries exactly |
| `ASTRO_GAIA_ASTRO_CAT`, `ASTRO_GAIA_XPSAMP_DIR` | **offline** Gaia DR3 catalogues (one-time `just download-catalogues`); with the astrometric file present, plate-solve + SPCC need no network and `-catalog=localgaia` becomes the default |
| `ASTRO_PLATESOLVE_CATALOG`, `ASTRO_SPCC_CATALOG`, `ASTRO_LOCAL_ASNET`, `ASTRO_SIRIL_CATALOG_DIR` | solver/catalogue overrides |
| `ASTRO_REUSE_*` | cross-session reuse (cone radius, dark recency, temperature tolerance) |
| `ASTRO_MAX_CPUS`, `ASTRO_SIRIL_MEM_RATIO`, `ASTRO_SIRIL_NICE` | Siril resource caps |
| `ASTRO_LLM_URL/MODEL/IMAGE_FORMAT/TIMEOUT_SEC` | the opt-in finish-supervisor model server |
| `ASTRO_SUPERVISE_HISTORY` | `off` disables the supervisor's history block + warm start |
