# Pipeline

How `astrostack process <mode> <format> <dir>` (or a job launched from the web UI) turns a capture
folder into a finished image. This page is the spine — each stage links to a deeper document.
Entry point: `pipeline.Process` (`internal/pipeline/pipeline.go`); every Siril script is generated
by `internal/siril/scripts.go` and pinned to **32-bit float** processing (`set32bits`).

## Deep-sky (the core path)

1. **Inspect** (`internal/inspect`) — walk the folder(s) recursively and classify every file as
   light / dark / flat / bias / dark-flat / video, grouped into *sets* by object, filter,
   exposure, gain, offset, temperature and binning. Classification is tiered — FITS headers →
   filename tokens → directory tokens → filter-wheel slots/sidecars → `info.txt` manifests →
   pixel-curve statistics — and ingests 16-bit TIFF stills while excluding processed outputs.
   Details: [modes/deepsky.md](modes/deepsky.md#detection--inputs).

2. **Master calibration** (`internal/calib` — [calibration.md](calibration.md)) — stack a master
   per calibration set with **count-adaptive rejection** (percentile ≤ 7 frames, winsorized 8–49,
   GESD ≥ 50). With a database, masters live in a reusable **library** and raw bias/darks pool
   **across sessions** into ever-deeper masters (noise ∝ 1/√N), with `.sig` signatures skipping
   unchanged re-stacks. Building a master dark also scans its raw pool for a **defect map** —
   hot, cold and *unstable/RTS* pixels — written as `<master>_defects.lst`.

3. **Calibrate + register per channel** — lights are calibrated with the matched masters (darks by
   exact exposure ±5 °C, else a scaled dark via `-opt`; cosmetic correction via the dark's
   **bad-pixel map**, `-cc=bpm`, falling back to `-cc=dark`), then registered (two-pass
   homography). Cross-session groups are each calibrated with **their own session's flat**,
   **parity-normalized** (a mirror-flipped optical train is detected by plate-solve det(CD) and
   physically flipped, with verification), and co-registered to the common field of view
   (`-framing=min`).

4. **Grade** (`internal/grade`) — per-frame registration metrics (FWHM, roundness, background,
   star count) plus a pure-Go Hough **trail detector**. Robust median+MAD rules reject elongated
   stars, soft frames, clouds and trailed subs — and never reject everything.

5. **Cross-frame transient mask** (`internal/transient`) — before stacking, pixels that spike in
   one frame against the per-pixel median across the registered subs (slow satellite trails,
   cosmic rays) are replaced by the median, with a line-aware pass for a trail's faint wings.
   Each line candidate is validated **window by window along its corridor** against the other
   frames: fixed-pattern walking noise lights the same windows in the same large fraction of
   frames (rejected), while a marching satellite or a **reused sky track** (geostationary belt,
   satellite trains) lights each window in only a few frames (accepted — the whole-corridor test
   this replaced rejected every candidate on a belt-reused track). A **recurring-corridor pass**
   additionally detects belt tracks on the coverage-aware *mean* residual — where per-frame-faint
   segments sum coherently exactly as they would in the stack — and repairs each frame's lit
   windows (from the median, or local background where most frames share the window). Under a
   memory budget (`ASTRO_TRAIL_MASK_MEM_GB`, else derived from the machine's available memory) a
   large full-canvas sequence is masked **streamed** — a bounded evenly-spaced frame basis stays
   resident and each sub is processed one at a time — instead of holding every frame in RAM.

6. **Stack the survivors** — `select`/`unselect` from the grading, then
   `stack … <adaptive rejection> -norm=addscale -output_norm -weight=wfwhm -filter-incl`. The
   rejection algorithm is sized to the **survivor count** (GESD engages on big stacks) and subs
   are weighted by star sharpness.

7. **Pointing diagnosis** (`internal/dither`) — the registration offsets classify the session as
   *dithered / drift / static / mixed*. Drift and static leave fixed-pattern residuals correlated
   (walking noise); the run warns once and recommends capture-time random dithering. The verdict
   is in `run.json` and the Tasks UI's **Pointing** column.

8. **Per-channel linear post** — optional GraXpert AI background extraction on each linear master,
   then measured, chroma-weighted denoise (Siril `denoise -vst`; R/G/B defer their strongest pass
   to the combined RGB).

9. **Co-register channels + palette** — channel masters align to one reference
   (common-FOV crop), then the **palette engine** maps filters onto RGB: natural LRGB(+Ha),
   HaRGB, or narrowband HOO/SHO/HOS/Foraxx (with a fallback chain when filters are missing).

10. **Combine + colour** — linear `rgbcomp`, a second combined-RGB gradient pass
    (GraXpert + RBF subsky), AI colour denoise, then the **colour-calibration ladder**:
    plate-solve + **SPCC** (offline Gaia catalogues supported) → star-field photometric gains →
    background neutralization. **Stretch headroom** caps linear highlights so star cores keep
    colour, then a dark-target autostretch (linked only when the colour is truly calibrated).

11. **Finish in GIMP** (`internal/gimp`) — a layered composite (RGB base + L in luminance mode +
    the emission screens: Ha in red, [OIII] in teal, [SII] deep-red or gold) saved as `.xcf`,
    then a flattened export with curves, saturation,
    star-core desaturation and star-safe highlight shoulders. Star clusters get a gentler
    dedicated profile; a gated **star-quality auto-fix** repairs burnt/discoloured stars by
    re-entering the cheapest stage. If GIMP is unavailable, the Siril `rgbcomp` finish
    (`internal/postprocess`) takes over.

12. **Persist** — outputs + a self-contained `run.json` (per-frame grades, calibration notes,
    pointing verdict, finish-quality metrics, engine build stamp) and a milestone preview
    timeline. A stage checkpoint enables **per-stage reruns** — edit one stage's parameters in
    the UI and re-enter from there (`internal/pipeline/rerun.go`).

Jobs launched from the UI can be **paused and resumed** (mid-stack pause reuses the finished
channels), **cancelled** (a cancel during finishing keeps the partial result, status `cancelled`),
and queued strictly one-at-a-time with "Add to queue".

### Optional AI enhancement (`internal/pipeline/enhance.go`)

Two open-source host tools augment the run when installed (set `GRAXPERT_BIN` / `STARNET_BIN`; skip
with `--no-ai`). Both are **soft-fail** — a missing or erroring binary logs a warning and the run
continues on the Siril/GIMP path. They are invoked, never bundled (see CLAUDE.md).

- **GraXpert background extraction** (`internal/graxpert`) — runs on each *linear* channel master
  right after stacking (and on the OSC master), removing complex light-pollution gradients with its
  AI model; a second pass cleans the combined RGB. Siril then applies only a gentle `subsky`
  cleanup at finish. Enabled per mode via `Preset.BackgroundAI`/`CombinedBackgroundAI`.
- **StarNet++ star reduction** (`internal/starnet`) — runs on the *flattened* GIMP composite, then
  GIMP blends the stars back over the starless image at `Preset.StarReduce` opacity
  (`result = (1-r)·starless + r·original`). Emits `final_reduced.{tif,png}` and keeps the
  `final_starless.tif` as a bonus artifact. Works for every compose mode.

### Optional AI finish supervisor (opt-in)

When a run **opts in** (the Import page's "Run with local AI agent" checkbox, `process …
--supervise`, or `astrostack refine <run-dir>`), the finish becomes a bounded optimisation loop —
render → deterministic metrics + local **vision-model** critique (combined `0.6·metrics +
0.4·model`) → re-enter at the cheapest tier (A composite / B linear prep / C re-stack) → keep the
best pass. Off by default; with the model unreachable the finish is byte-identical to a standard
run. Full detail: [agent.md](agent.md).

### Cross-session reuse (`internal/pipeline/reuse.go`, `internal/calib/deep.go`)

Every run records its scanned frames in the Postgres **catalog** (`frames` + a `targets` table of
canonical sky objects; ingest in `store.SaveInventory`). A later run of the same target reuses that
history — driven by config (`ASTRO_REUSE_*`), surfaced in the import UI as an auto-discovered list
the user can deselect (`POST /api/reuse/preview`):

- **More light = more integration.** Prior light frames of the same target — matched first by
  RA/Dec **cone**, falling back to normalized object name — are folded into the stack. Each
  contributing session is calibrated with **its own** flats, then all calibrated frames are
  co-registered and stacked together (`processChannelGroups`). Reused frames pass the same grading
  gate as fresh ones and are de-duplicated by path.
- **Capture nights are first-class.** Every dated frame carries its observing-night key
  ("YYYY-MM-DD", local-noon bucketed — `inspect.NightKey`). A multi-night scan splits the light
  **and flat** sets per night (dust moves between nights; per-night flats never merge or persist to
  the library), groups are labeled by night everywhere (import breakdown, live per-night sub-steps,
  run.json `channels[].groups`), and `POST /api/calib/plan` previews the joined per-night
  calibration mapping before the run. A single-night scan is byte-identical to the
  pre-sessionization behavior (proven by the `TestProcess_SingleNightGolden` live golden).
- **All channels stack on one anchor canvas.** A grouped run resolves an **anchor night** (most
  channels covered → most frames → latest; current capture outranks priors) and routes **every**
  channel — even single-group ones — through the grouped path: the merged registration is computed
  in two passes, reviewed in Go (the anchor group's middle frame is pinned with `setref`, each
  frame's homography is checked against the anchor field and physically absurd transforms — false
  star matches — are excluded from the stack), then applied with `-framing=current` so every
  channel master lands on the anchor night's full field. Each night's measured field rotation and
  overlap stream into the journal and land in run.json (`rotation_deg`/`overlap_frac`). The old
  `-framing=min` intersection is gone — one drifted night or a single bad transform used to
  collapse a channel master to a sliver, leaving mixed master dimensions that killed the colour
  combine while the job still reported success; a multi-channel run that produces no final image
  now **fails honestly** (artifacts and the stage checkpoint are kept for a rerun). Proven live by
  `TestProcess_TwoNightAnchoredGeometry` (a ~35°-rotated second night contributing only L/R).
- **A single-frame night cannot kill a channel.** Siril has no one-image sequences (`link` writes
  no `.seq` for a lone file, so every sequence command aborts): a group holding one frame is
  calibrated with `calibrate_single` instead, and a channel whose *total* integration is one
  calibrated frame skips registration/stacking and promotes that frame to the channel master with
  the shared linear finishing — one usable frame beats a dead channel, and the degraded path is
  warned in the journal (same rules in the single-session and livestack paths).
- **Photometric normalization before the merge.** Groups shot at different exposure/gain/
  temperature are measured (`internal/photom`) and mapped onto the reference group's linear scale
  before co-registration — ON by default for deep-sky modes. The scale ladder: a measurable curve
  is fitted (Theil–Sen); a flat (sky-dominated) curve is seeded from the header exposure/gain
  prediction — the ZWO `10^(Δgain/200)` factor applies when both sides confirm the convention via
  INSTRUME **or** the ASICAP `SWCREATE` (old ASICAP writes no INSTRUME card), equal known gains
  cancel under any convention, and differing gains with no confirmed convention yield **no
  prediction** rather than a silently-wrong exposure-only ratio; the measured **sky-background
  ratio** then stands in (and also cross-checks a seed — a >3× disagreement means the backgrounds
  win). A pre-apply **no-clip gate** degrades any transform that would push a healthy sky below
  zero at 3σ (bg-matched, then offset-only, with a warning) — mis-scaled nights can no longer
  stack as black frames. **One reference night for all channels**: the photometric reference is
  picked by the registration anchor's own ranking, so every channel shares one scale regime.
  Per-group records (scale/offset/residual/method + before/after previews) stream live and land in
  run.json.
- **Per-night relative grading.** In a merged stack the RELATIVE rejection rules (roundness/FWHM/
  background/star-count vs the population median) are scoped per capture night
  (`grade.GradeGrouped`): each night is judged against its own medians, so the sharpest night's
  statistics can no longer evict every other night wholesale. Absolute floors (trail detection,
  the roundness floor) stay global, as does the Siril stack-minimum restore. Per-night
  stacked/rejected counts land in run.json (`groups[].stacked_frames`) and a night contributing
  nothing warns live.
- **Per-frame background flatten before the merged registration** (`FlattenBg`, deep-sky default):
  each calibrated frame's own degree-1 sky gradient is subtracted (Siril `seqsubsky`, mean level
  preserved) before the 2-pass register — the nights' gradients lie at their own field rotations,
  so unflattened they step at footprint boundaries in the master (background seams). Soft-fails
  to the unflattened sequence.
- **Coverage-aware colour combine** (`CoverageCrop`, deep-sky default): each channel's stacked
  coverage of the anchor canvas is rasterized from the reviewed registration homographies
  (persisted as `covered_frac` + a `coverage_<tag>.png` mask); the combine inputs are cropped to
  the largest rectangle **every** channel covers at ≥30% of its stacked depth (masters and
  aligned files stay untouched — cropped copies `combine_<tag>.fits`). A collapsed intersection
  (<35% of the canvas) falls back to the full field with a warning. Kills the regional
  green/magenta casts and black wedges of mixed-coverage merges.
- **Deeper calibration = less noise.** `BuildDeepMasters` pools **every** matching raw bias/dark
  across sessions into one deep master — see [calibration.md](calibration.md). Darks/bias stay
  night-agnostic on purpose (closed-shutter thermal signatures).

Disable per run with the import toggle, or globally with `ASTRO_REUSE_ENABLED=false` (the catalog
is still recorded). The single-session path is unchanged when no prior data matches.

## Lunar / planetary video

`astrostack video <file>` (`internal/planetary`): extract frames (ffmpeg for MP4/MOV/MKV/AVI; SER
read by Siril; a folder of stills also works) → rank frames by **full-resolution, disk-masked
Laplacian-variance sharpness** in Go → keep the best N% → **multi-point surface alignment** (coarse
+ fine ZNCC and a 10×10 alignment-point grid, applied by a single Catmull-Rom resample) →
**sharpness-weighted, sigma-clipped stack** with per-region (AP) quality weights → Richardson-Lucy
**deconvolution** of the luminance → highlight-safe stretch + wavelet sharpen. The stack is
objectively accepted only when the master out-details the best single frame (≥ 1.05×). Full
detail: [docs/modes/planetary.md](modes/planetary.md).

## Modes

`internal/mode` maps each capture mode to a `Preset` that retunes the whole pipeline; a
**preset catalog** (16 built-in situation recipes + user-saved presets, `internal/preset` +
`/api/presets`) prefills the launch form. Each mode has a dedicated document with the fixed
template *what & when · detection · algorithm end-to-end · preset knobs · soft-fail fallbacks ·
AI supervisor · outputs · config* — see [docs/modes/README.md](modes/README.md):

- **[deepsky](modes/deepsky.md)** — mono LRGB+Ha galaxies/clusters: per-channel
  calibrate/register/grade/stack, channel co-registration, combined GraXpert+RBF gradient removal,
  SPCC → star-field → neutralization colour ladder, layered GIMP finish, palettes.
- **[nebula](modes/nebula.md)** — same engine, retuned for faint emission: lenient grading,
  Ha-forward blend, StarNet++ star reduction on by default.
- **[milkyway](modes/milkyway.md)** — one-shot-color nightscapes (iPhone ProRAW/DSLR raws) via
  `pipeline.ProcessOSC` → the `internal/nightscape` foreground/sky composite recipe: photometric
  develop, sky-only stack, per-pixel phone calibration in Go, mask-aware flatten, data-driven
  auto-stretch, dithered export.
- **[planetary](modes/planetary.md)** — lucky imaging (`internal/planetary`): see above.
- **[comet](modes/comet.md)** — moving comet: one global star alignment to the mid frame,
  multi-scale coma detection + robust track fit, dual star/comet stacks (asymmetric rejection on
  the comet side), StarNet star-layer recomposite.
- **[livestack](modes/livestack.md)** — watch a folder or S3 prefix during capture, calibrate each
  new sub once, incrementally re-stack with a live preview, and run the full pipeline on Stop.

The output `format` (`image`/`video`/`both`) additionally renders a Ken-Burns MP4 via
`internal/videoout` (ffmpeg).

## Tuning

Rejection thresholds (`internal/grade.Options`), post-processing (`internal/postprocess.Options`)
and every mode knob (`internal/mode.Preset`) have sensible defaults; the launch form's
**Advanced parameters**, saved **presets**, the per-stage **rerun** editor and the agent's param
patches all feed the same whitelisted patch surface (`internal/pipeline/params_patch.go`).
