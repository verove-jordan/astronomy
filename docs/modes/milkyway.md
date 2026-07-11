# milkyway

## What & when

Wide-field **one-shot-colour nightscapes** — a static camera (typically an iPhone shooting ProRAW
DNG, or a DSLR) capturing many identical frames of the Milky Way over a landscape foreground. The
recipe produces a composite: a star-registered, sigma-clipped **sky stack** under a single clean
**foreground** frame, colour-graded in linear light. Inputs are camera raws (DNG/HEIC/CR2/CR3/NEF/
ARW/RAF) or Siril-native stills (TIFF/PNG/JPEG); optional phone darks/bias/flats calibrate the
stack. At least 2 frames are required (`nightscape.Process`).

A folder of **Bayer CFA FITS** (an OSC astro camera, no raws to develop) does not use this recipe:
it falls through to the generic OSC stack in `pipeline.ProcessOSC` (`convert -debayer` → register →
grade → stack → GIMP curves).

## Detection & inputs

- `inspect.ListRawFramesMany` (`internal/inspect`) collects the camera-raw stills; if none exist,
  `ListFITSFramesMany` collects FITS and the generic debayer path runs instead
  (`pipeline.ProcessOSC` in `internal/pipeline/osc.go`).
- **Calibration frames mixed into the input** are auto-detected by `splitCalibrationFrames`
  (`internal/pipeline/osc.go`) → `inspect.ClassifyRawStills` (`internal/inspect/rawstill.go`):
  folder/filename tokens first (`darks/`, `bias/`/`offset/`, `flats/` are authoritative), then a
  downscaled-develop pixel-statistics pass (`classifyByStats`) for loose unlabeled piles. EXIF
  ISO/exposure/model/dimensions are read for every frame by `internal/rawmeta` (pure-Go TIFF/EXIF
  parser with an `mdls` fallback). If classification would leave no lights, everything is treated
  as a light.
- Explicit calibration folders can also be passed (`Options.DarkDir/FlatDir/BiasDir`).
- The run is named from the target folder, walking past generic buckets (`Sorted_DNG`, dates), so
  every night lands under `output/<target>/<runID>` (`smartObject`).

## Algorithm, end to end

Sources of truth: `processNightscape` (`internal/pipeline/osc.go`) → `nightscape.Process` /
`compose` / `gradeCompose` (`internal/nightscape/nightscape.go`).

1. **Photometric develop** (`rawconv.PrepareTIFF` in `internal/rawconv/rawconv.go`). Every raw is
   developed to a 16-bit RGB TIFF with LibRaw's `dcraw_emu` (preferred on every platform):
   `-T -6 -W -w -t 0 -g 2.4 12.92` — 16-bit, **no per-frame auto-brightening** (`-W`, one exposure
   scale for the whole stack), camera white balance (`-w`, shared by lights and cal frames so it
   cancels), **`-t 0` never bakes the EXIF orientation into the pixels**, and an exact sRGB
   transfer so the pipeline's `linearizeSRGB` is its exact inverse. macOS `sips` is the soft-fail
   fallback (Apple's opaque ProRAW tone curve); Siril-native stills are symlinked through
   untouched.
2. **Reference frame.** The clean-foreground reference is the explicit `ForegroundFrame` override
   (prepended, index 1) or the session-middle frame.
3. **Optional calibration** (`planCalibration`/`calibrateLights` in
   `internal/nightscape/calibrate.go`). Lights are converted to FITS (`convert light -out=.`) and
   dark/flat/bias-corrected **per-pixel in Go, in linear light, in sensor-native space** — never by
   Siril `calibrate`. Masters come from this run's cal frames or the reusable **phone calibration
   library** (`internal/calib/phone.go`, table `phone_calib_masters`, keyed ISO + exposure +
   sensor dimensions + camera model); freshly built masters are persisted
   (`internal/nightscape/library.go`). Masters are dimension-guarded — a mismatched-resolution
   master is dropped, never applied. Soft-fail: a bad cal set warns and proceeds uncalibrated.
4. **Register** — Siril, with the reference forced:
   `setref light <ref>`, `register light -2pass`, `seqapplyreg light` (the uncalibrated single-pass
   path does `convert` + the same register in one script, byte-identical to v1).
5. **Foreground prep** (`compose`): the unaligned reference frame is read, `linearizeSRGB`
   converted to linear light, hot pixels cleaned (`cleanHotPixels`, 5σ).
6. **Sky mask** — `buildSkyAlpha` from the clean linear foreground (percentile 45, dilation 15,
   blur 12), captured before any level changes.
7. **Clean sky stack** — `computeCleanSkyStack` streams the aligned frames (linearizing each) into
   a per-pixel **sky-only sigma-clipped mean**, masking the foreground so trees/buildings never
   ghost into the sky.
8. **Sky neutralize + flatten** — `neutralizeBackground` (a gentle per-channel *offset* to a common
   black, percentile 2.0 — not a per-channel clip, which speckles noisy darks), then
   `removeSkyGradient` (`internal/nightscape/flatten.go`): a **mask-aware** background model built
   from sky pixels only (grid step `max(W,H)/16`), so the dark foreground cannot bias the horizon
   level. Removes warm horizon glow / light pollution in linear light.
9. **Sky enhancement** (`enhanceSky` in `internal/nightscape/enhance.go`, soft-fail): GraXpert
   **chroma denoise** on the sky stack, and plate-solve + SPCC only when an OSC sensor is
   configured (`ASTRO_NIGHTSCAPE_OSC_SENSOR`; a phone sensor is rarely in Siril's SPCC DB, so the
   default path is neutralization). The plate scale is derived from the EXIF 35 mm-equivalent
   focal length (`ReadFocal35mm`).
10. **Green removal + chroma blur** — mild SCNR-style `removeGreenCast` on both layers, then a
    chroma-only blur on the sky so the later saturation boost can't re-amplify colour speckle.
11. **Orientation decision** (`resolveOrientation` → `orientDecision` in
    `internal/nightscape/develop.go`). The final display transform is applied **exactly once**, at
    the end of the grade: an explicit user token (`cw|ccw|180[±-flip]`) wins; `auto` opts into the
    content heuristic (portrait + bright half up); the default (`exif`) reads the source's real
    EXIF orientation tag (direct TIFF IFD0 parse — `sips -g orientation` returns nil for DNG) and
    reconciles it with how the developer treated it: `dcraw_emu -t 0` never bakes → apply the
    token; `sips` on DNG leaves sensor orientation → apply; `sips` on other formats may have baked
    it — the transposing codes (5–8) are detected by comparing source vs developed **aspect**,
    the non-transposing ones (2/3/4) are assumed baked; an unreadable tag → `none` (never guess).
12. **Persist the pre-grade linear inputs** (`persistGradeInputs`): `lin_sky.fits`, `lin_fg.fits`,
    `sky_alpha.fits`, `grade.orient` — the re-finish inputs for the supervisor and post-run refine
    (`nightscape.Regrade` re-grades in seconds with no re-develop/re-register).
13. **Grade + composite** (`gradeCompose`):
    - **`autoStretch`** (`internal/nightscape/autostretch.go`) — a data-driven linked asinh
      stretch over **sky pixels only**: black/white points are luminance percentiles, the black
      point climbs until the background can land on the target, bounded by a **structure guard**
      (it may never rise into the upper third toward the median, so dim Milky-Way regions compress
      instead of clipping to black), and the asinh intensity is *solved* so the background lands
      exactly on `targetBg` (the Brightness control / the Look's own target). The Brightness choice
      also scales the highlight ceiling (0.7–1.35×) so "darker" dims the core too;
    - `asinhStretch` on the foreground (the Look's `AsinhIntensityFG`);
    - `compositeLayers` blends sky over foreground through the alpha mask;
    - `boostSaturation` (Look saturation × the `saturation_scale` knob), `splitTone`
      (shadow/highlight tints, Look-dependent), `compressHighlights` (knee/ceiling shoulder that
      keeps the core golden instead of clipped white);
    - `orient` applies the resolved orientation (once);
    - **export** — `final.png` written with a deterministic **triangular dither**
      (`to8Dithered`) so the smooth stretched sky never bands in 8-bit, plus a 1400-px
      `final_preview.png` and the linear `composite.fits` / `stacked_sky.fits` /
      `foreground_reference.fits`.
14. **Result mapping** — `processNightscape` maps the recipe result onto the standard
    `pipeline.Result`, captures stage previews and writes `run.json` (engine build stamp).

The three **looks** (`internal/nightscape/nightscape.go`, `looks` map) share the artifact-free
grade and differ only in stretch/saturation aggressiveness:

| Look | Target bg | Highlight knee/ceiling | Saturation | Character |
|------|-----------|------------------------|------------|-----------|
| `natural` (default) | 0.05 | 0.30 / 0.38 | 1.10 | a faithful developed phone frame |
| `iphone` | 0.07 | 0.35 / 0.70 | 1.12 | warmer, punchier, deeper sky + split-tone |
| `deepsky` | 0.09 | 0.40 / 0.78 | 1.30 | bold and dramatic |

## Preset knobs & defaults

From `mode.For(mode.Milkyway)` in `internal/mode/preset.go`. Agent-tunable knobs are the milkyway
patch set (`nightscapePatch` in `internal/pipeline/supervise_nightscape.go`, applied by
`applyNightscapeParamPatch` in `internal/pipeline/params_patch.go`) — all Tier A (a re-grade takes
seconds).

| Knob | Default | What it does | Agent |
|------|---------|--------------|-------|
| `Look` | `natural` | render style (natural / iphone / deepsky) | A (`look`) |
| `BackgroundLevel` | 0.05 | auto-stretch target sky background; the UI/CLI brightness control maps darker/balanced/brighter → 0.035/0.05/0.07 (`mode.BrightnessTarget`) | A (`brightness`, clamped 0.03–0.2) |
| `Saturation` | 1.0 | a **scale** on the Look's own saturation (1 = as designed) | A (`saturation_scale`, 0–2) |
| `HighlightCeil` | 0 (= Look's own) | overrides the core highlight ceiling (lower = dimmer core) | A (`highlight_ceiling`, 0.3–0.95) |
| `Orientation` | `exif` | final display transform (`exif`/`auto`/`none`/`cw`/`ccw`/`180`[+`-flip`]) | — |
| `ForegroundFrame` | "" | explicit clean-foreground raw (also the registration reference) | — |
| `Grade` | roundness 0.45, σ 3.5, star frac 0.3, trails **off** | lenient — used by the generic OSC path and live preview | — |
| `BackgroundDegree` | 3 | generic-OSC-path subsky degree (the nightscape recipe uses its own flatten) | — |
| `BackgroundAI` | true | GraXpert on the sky stack (chroma denoise / gradient help) | — |
| `ColorCalibration` | true | SPCC on the sky — engages only with `ASTRO_NIGHTSCAPE_OSC_SENSOR` set | — |
| `DenoiseChroma` | 0.60 (VST+DA3D) | generic-OSC-path master denoise | — |
| `Previews` | true | preview PNGs | — |

## Soft-fail fallbacks

| Condition | Behavior |
|-----------|----------|
| No raw developer (`dcraw_emu` and `sips` both missing) | each raw frame fails with a clear warning; the run errors only when **no** frame could be prepared. `/api/environment` warns up front |
| Developer is `sips` (no LibRaw) | processing continues through Apple's opaque tone curve; environment warning recommends `brew install libraw` |
| GraXpert absent/unhealthy or `--no-ai` | no AI denoise/gradient help on the sky — the mask-aware flatten + auto-levels still balance it |
| SPCC not configured (`ASTRO_NIGHTSCAPE_OSC_SENSOR` empty) or solve fails | background neutralization + green removal (a homogeneous neutral sky either way) |
| Bad/mismatched calibration set | warns and proceeds uncalibrated; dimension-guarded masters are dropped, never applied |
| EXIF orientation unreadable | `none` — no guessed rotation (explicit override exists for that case) |
| Supervisor unreachable / persisted linear inputs missing | standard nightscape finish; a refine of a pre-persistence run errors legibly |

## AI supervisor

`superviseFinishNightscape` (`internal/pipeline/supervise_nightscape.go`) plugs the shared loop
(`internal/pipeline/supervise.go`) into a **single-stage** renderer: each candidate is a
`nightscape.Regrade` over the persisted `lin_sky`/`lin_fg`/`sky_alpha` (seconds, `PreviewOnly` —
only the scored PNG is written). Knobs: `look`, `brightness`, `saturation_scale`,
`highlight_ceiling` (all Tier A; `maxTier` is A — structural changes go through the agent's
`retry_run_tuned restack=true`, a full re-process).

Mode-specific deterministic scoring (`scoreFinishMode` in
`internal/pipeline/supervise_score.go`): the shared colour/clipping guardrails **plus a
foreground-balance guard** — the bottom-rows mean luma must stay readable (penalty when crushed
below 0.015 or lifted above 0.35), so a graded sky can't win by killing the landscape. Iteration
history, best-image comparison, warm start (`finish_iterations` keyed by target), plateau stop,
series campaigns and the chat tools (`get_mode_params` / `view_result_image` /
`retry_run_tuned`) work exactly as in [deepsky](deepsky.md#ai-supervisor). The winner is
re-rendered **with full exports** into the run dir (`finalize`), so `final.png` and the linear
FITS reflect the chosen grade.

## Outputs & artifacts

Run directory `output/<object>/<runID>/`:

| Artifact | Content |
|----------|---------|
| `final.png` | the display composite (dithered 8-bit sRGB) — the primary output |
| `final_preview.png` | 1400-px preview |
| `composite.fits` | the graded composite, linear FITS |
| `stacked_sky.fits` / `foreground_reference.fits` | the oriented linear layers |
| `lin_sky.fits`, `lin_fg.fits`, `sky_alpha.fits`, `grade.orient` | pre-grade linear inputs (re-finish/refine inputs) |
| `sv/final_iter<N>/final.png` | per-iteration supervised renders |
| `previews/*.png` | milestone timeline (stacked sky, final) |
| `run.json` | standard run record: channels (one RGB entry: input/stacked frame counts), final outputs + notes (look, dimensions), warnings, stage previews, **engine build stamp**. (The run-level `finish_quality` stamp applies to the deepsky/nebula finish path only.) |

## Config/env

| Variable | Role |
|----------|------|
| `DCRAW_BIN` | LibRaw `dcraw_emu` (preferred developer; `brew install libraw` / `libraw-bin`). `SIPS_BIN` overrides the macOS fallback. |
| `ASTRO_NIGHTSCAPE_OSC_SENSOR` | SPCC sensor name for the sky stack; blank (default) uses the neutralization path |
| `GRAXPERT_BIN` | optional sky denoise/gradient help (deep health probe; `pipx inject graxpert onnxruntime` fixes a broken pipx install) |
| `ASTRO_LIBRARY_DIR` | where phone calibration masters persist (`phone_calib_masters`) |
| `ASTRO_GAIA_ASTRO_CAT` / `ASTRO_LLM_*` | offline solving / the opt-in supervisor, as in deepsky |

Note the milkyway brightness control accepts keywords or a raw 0–0.5 number
(`mode.BrightnessTarget` in `internal/mode/preset.go`).
