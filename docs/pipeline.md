# Pipeline

How `astrostack process <dir>` turns a capture folder into a finished image.

## Deep-sky

1. **Inspect** (`internal/inspect`) — walk the directory, read each FITS header, and classify every
   file as light / dark / flat / bias / dark-flat / video. Frames are grouped into *sets* by
   object, filter, exposure, gain, offset, temperature and binning. Files without an `IMAGETYP`
   card fall back to a sampled-ADU heuristic.

2. **Master calibration** (`internal/calib`) — for each calibration set, stack a master with Siril
   (Winsorized sigma). With a database, masters are saved to a **reusable library** and an existing
   matching master is reused instead of rebuilt; a lights-only session pulls the right masters from
   the library. Matching rules: darks by exposure + temperature (±5 °C) + gain + offset; flats by
   filter; bias by gain + offset.

3. **Per channel** (`internal/pipeline` + `internal/grade`):
   - **Calibrate + register** the lights (Siril `calibrate` then `register`).
   - **Grade** each sub-frame from the registration metrics (FWHM, roundness, star count,
     background) and a pure-Go Hough **trail detector**. Frames are rejected for elongated stars,
     soft focus/seeing, clouds (few stars), or satellite/aircraft trails — robust median+MAD rules
     that never reject a tight set and never reject everything.
   - **Stack** only the survivors (`select`/`unselect` + `stack … -filter-incl`). Winsorized sigma
     also clips residual trail pixels.

4. **Co-register channels** (`internal/pipeline` → `siril.AlignMastersScript`) — the per-channel
   masters are registered together to one reference so L/R/G/B/Ha line up before compositing.

5. **Finish in GIMP** (`internal/gimp`) — Siril background-extracts + stretches each channel to a
   TIFF; the engine then drives the resident GIMP Script-Fu server (shared with the GIMP MCP) to
   build a layered image — RGB base + L in `LAYER-MODE-LUMINANCE` + Ha red-tinted in `SCREEN` —
   apply gentle curves/levels/saturation, and export an editable `.xcf` plus flattened TIFF/PNG.
   If GIMP is unavailable it falls back to the Siril `rgbcomp` finish (`internal/postprocess`).

### Optional AI enhancement (`internal/pipeline/enhance.go`)

Two open-source host tools augment the run when installed (set `GRAXPERT_BIN` / `STARNET_BIN`; skip
with `--no-ai`). Both are **soft-fail** — a missing or erroring binary logs a warning and the run
continues on the Siril/GIMP path. They are invoked, never bundled (see CLAUDE.md).

- **GraXpert background extraction** (`internal/graxpert`) — runs on each *linear* channel master
  right after stacking (and on the OSC master), removing complex light-pollution gradients with its
  AI model. Siril then applies only a gentle degree-1 `subsky` cleanup at finish (`backgroundDegree`
  is always in Siril's valid [1,4] range). Enabled per mode via `Preset.BackgroundAI`.
- **StarNet++ star reduction** (`internal/starnet`) — runs on the *flattened* GIMP composite, then
  GIMP blends the stars back over the starless image at `Preset.StarReduce` opacity
  (`result = (1-r)·starless + r·original`). Emits `final_reduced.{tif,png}` and keeps the
  `final_starless.tif` as a bonus artifact. Works for every compose mode.

Each run writes its outputs and a JSON/markdown report; with the API, the full report (including
per-frame grades) is stored on the job and rendered in the web UI's frame-review page.

### Optional AI finish supervisor (opt-in, `internal/pipeline/supervise.go` + `internal/llm`)

When a run **opts in** (the Import page's "Run with local AI agent" checkbox, `process … --supervise`,
or `astrostack refine <run-dir>`), the finish becomes a bounded optimisation loop instead of a single
pass — for **every stacking mode**, each through its own cheap re-finish (`candidateRenderer` in
`internal/pipeline/supervise_renderer.go`; per-mode details in the [mode docs](modes/README.md)). For
deep-sky, the heavy prep (channel combine, GraXpert, SPCC, stretch) runs once; then the fast GIMP
composite is re-rendered a few times with varied knobs (saturation, Ha screen/black-point, chroma blur,
crop). Each render is scored by **deterministic image metrics** *and* a local **vision model** (a
host-run, OpenAI-compatible server — e.g. mlx-vlm on `:1234`, started with `just run-ia-model`),
combined as `0.6·metrics + 0.4·model`, and the best render is kept. The critique sees a rolling
**iteration history** and the best-so-far image; a repeat run of the same target **warm-starts** from
its best prior pass. Iterations (params, scores, reasoning, the chosen flag) are persisted and shown in
the job's supervisor panel.

It is **off by default and soft-fails**: with the box unticked or the model server unreachable, the
finish is **byte-identical** to the standard pipeline. `refine` re-tunes an existing stack with **no
re-stacking** (it reconstructs the channels from the run's `aligned_*`/`master_*` on disk). See
`.env.example` (`ASTRO_LLM_*`) and the README's *AI finish supervisor* section.

### Cross-session reuse (`internal/pipeline/reuse.go`, `internal/calib/deep.go`)

Every run records its scanned frames in the Postgres **catalog** (`frames` + a `targets` table of
canonical sky objects; ingest in `store.SaveInventory`). A later run of the same target can then
reuse that history — driven by config (`ASTRO_REUSE_*`), surfaced in the import UI as an
auto-discovered list the user can deselect (`POST /api/reuse/preview`):

- **More light = more integration.** Prior light frames of the same target — matched first by RA/Dec
  **cone** (`OBJCTRA/OBJCTDEC` → degrees), falling back to normalized object name (folder name when
  the FITS has no `OBJECT`) — are folded into the stack. Each contributing session is calibrated with
  **its own** flats (its night's dust/vignetting), then all calibrated frames are co-registered and
  stacked together (`processChannelGroups`). Reused frames pass the same `grade` gate as fresh ones,
  and frames already in the current scan are de-duplicated by path so identical files never double-count.
- **Deeper calibration = less noise.** Instead of freezing a stacked master, `BuildDeepMasters` pools
  **every** matching raw bias/dark across all sessions (within a temperature + recency window) into one
  deep master — so noise keeps dropping (~1/√N) as more calibration data accrues. Bias/darks are
  sensor-only and pooled freely; **flats stay session-specific** by design.

Disable per run with the import toggle, or globally with `ASTRO_REUSE_ENABLED=false` (the catalog is
still recorded). The single-session path is unchanged when no prior data matches.

## Lunar / planetary video

`astrostack video <file>` (`internal/planetary`): extract frames (ffmpeg for MP4/MOV/MKV/AVI; SER
read by Siril; a folder of stills also works) → rank frames by **full-resolution, disk-masked
Laplacian-variance sharpness** in Go → keep the best N% → **multi-point surface alignment** (coarse
+ fine ZNCC and a 10×10 alignment-point grid, applied by a single Catmull-Rom resample) →
**sharpness-weighted, sigma-clipped stack** with per-region (AP) quality weights → Richardson-Lucy
**deconvolution** of the luminance → highlight-safe stretch + wavelet sharpen. The stack is
objectively accepted only when the master out-details the best single frame (≥ 1.05×). The old
Siril-CLI "no surface alignment — use AutoStakkert! for demanding work" caveat no longer applies:
the in-house path does native ranking, AP-weighted stacking and real deconvolution. Full detail:
[docs/modes/planetary.md](modes/planetary.md).

## Modes

`internal/mode` maps each capture mode to a `Preset` that retunes the whole pipeline. Each mode has
a dedicated document with the fixed template *what & when · detection · algorithm end-to-end ·
preset knobs · soft-fail fallbacks · AI supervisor · outputs · config* — see
[docs/modes/README.md](modes/README.md):

- **[deepsky](modes/deepsky.md)** — mono LRGB+Ha galaxies/clusters: per-channel
  calibrate/register/grade/stack, channel co-registration, combined GraXpert+RBF gradient removal,
  SPCC → star-field → neutralization colour ladder, layered GIMP finish.
- **[nebula](modes/nebula.md)** — same engine, retuned for faint emission: lenient grading,
  Ha-forward blend, StarNet++ star reduction on by default.
- **[milkyway](modes/milkyway.md)** — one-shot-color nightscapes (iPhone ProRAW/DSLR raws) via
  `pipeline.ProcessOSC` → the `internal/nightscape` foreground/sky composite recipe: photometric
  `dcraw_emu` develop, sky-only sigma-clipped stack, mask-aware flatten, data-driven auto-stretch,
  dithered export.
- **[planetary](modes/planetary.md)** — lucky imaging (`internal/planetary`): see above.
- **[comet](modes/comet.md)** — moving comet: one global star alignment to the mid frame,
  multi-scale coma detection + robust linear/quadratic track fit, dual star/comet stacks
  (asymmetric rejection on the comet side), StarNet star-layer recomposite.

The output `format` (`image`/`video`/`both`) additionally renders a Ken-Burns MP4 via
`internal/videoout` (ffmpeg).

## Tuning

Rejection thresholds (`internal/grade`, `Options`) and post-processing (`internal/postprocess`,
`Options`) have sensible defaults; both are passed through `pipeline.Options` for callers that want
to override them.
