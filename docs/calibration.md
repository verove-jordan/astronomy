# Calibration: masters, the library, and defect maps

How AstroStack turns raw darks/flats/bias into the masters that calibrate every light — and why
noise keeps dropping as sessions accumulate. Code: `internal/calib` (building, matching, defect
scan), `internal/siril/scripts.go` (the generated Siril scripts).

## Master building

Each calibration *set* (same type + gain + offset + binning + exposure + temperature bucket) is
stacked into one master FITS:

- **darks / bias / dark-flats** — `convert <seq>` then `stack <seq> <rejection> -nonorm`
  (`StackMasterScript`). `convert` (not `link`) means a calibration set captured as **16-bit TIFF**
  (SharpCap lunar darks) stacks exactly like FITS.
- **flats** — optionally bias-calibrated first (`calibrate <seq> -bias=<master>` — a matching
  dark-flat wins over a bias), then `stack … -norm=mul` (multiplicative normalization, correct for
  flat fields). Every master flat also runs an **optical QC** pass (`internal/optics.AnalyzeFlat`):
  dust donuts, blotches, saturation and vignetting are reported as run warnings with a JSON/PNG
  sidecar.

Masters are named by everything that makes them reusable —
`master_<TYPE>[_<filter>][_<exposure>ms]_g<gain>o<offset>_b<bin>[_<temp>C].fits` — and saved to
the **library** (`ASTRO_LIBRARY_DIR`).

### Count-adaptive rejection

Every stack picks its pixel-rejection algorithm from the frame count (`siril.Rejection`):

| Frames | Rejection | Why |
|---|---|---|
| ≤ 7 | `rej percentile 0.2 0.1` | σ estimates are meaningless on a handful of samples |
| 8 – 49 | `rej winsorized 3 3` | the proven default for medium stacks |
| ≥ 50 | `rej generalized 0.3 0.05` (GESD) | markedly better on outlier tails — kills the correlated leftovers (walking noise, trail remnants) a 3σ clip misses |

The light-frame stack sizes the choice to the **surviving** (graded-in) frame count. All
processing is pinned to 32-bit float (`set32bits`), so dark subtraction keeps negative pixels and
the rejection statistics stay unbiased.

## Deep cross-session pools

With the database on, every scan is catalogued, and `BuildDeepMasters` pools raw calibration
frames **across sessions** instead of freezing per-session masters:

- **bias** — sensor-only, pooled freely (no temperature or recency bound);
- **darks** — pooled per camera config + exposure, temperature within `ASTRO_REUSE_TEMP_TOL_C`
  (default ±5 °C) and inside the optional recency window (`ASTRO_REUSE_DARK_RECENCY_DAYS`);
- **flats / dark-flats** — deliberately **session-local** (dust and vignetting belong to one
  night's optical train).

Master noise adds to every calibrated light in quadrature and falls as 1/√N of the pool — so a
library that keeps accumulating darks and offsets directly lowers the noise floor of every future
run.

Pool hygiene and reuse:

- **Reuse signatures** — a master carries a `.sig` sidecar (sha256 of each pool frame's
  path|size|mtime). An unchanged pool reuses the on-disk master instead of re-stacking (minutes
  saved on large pools); writes are atomic (temp + rename) so concurrent runs sharing the library
  never see a half-written master.
- **`dropMissing`** — catalogued frames whose file was freed to S3 are skipped with a counted
  warning (one ghost path would sink the whole Siril stack).
- **`dropNonFITS`** — anything that isn't a FITS file is excluded from a pool with a counted
  warning (a processed image that once slipped into the catalog must never be stacked as
  calibration).

## Dark defect map (bad-pixel map)

Classic cosmetic correction (`-cc=dark`) can only see pixels that are hot or cold **in the
master** — it is blind to *unstable* (random-telegraph / flickering) pixels whose master value
looks normal, and those are exactly the pixels that survive dark subtraction as random residuals
and smear into **walking noise** on drifting, undithered sequences.

When a master dark is built (and lazily for pre-existing library masters), the raw dark pool is
scanned per pixel (`calib.ScanDarkDefects`):

1. **Temporal statistics** — mean and sigma of each pixel across all darks in the pool.
2. **Local baseline** — both maps are compared to a 5×5 separable local median, so large-scale
   structure (amp glow, vignetting) never reads as defects.
3. **Flagging** — hot/cold: mean 3σ off its local baseline (robust MAD scale); **unstable/RTS**:
   temporal sigma 6σ above its local baseline.
4. **Safety** — needs ≥ 8 darks (temporal σ is untrustworthy below that); the list is capped at
   0.5 % of the sensor keeping the strongest detections, so a pathological scan stays harmless.

The result is written beside the master in Siril's `find_hot` format —
`library/master_DARK_…_defects.lst` (`P x y H|C`, orientation-aware) — and mirrors to S3 with the
library. At calibration time the matched dark's map is applied per frame as
**`calibrate … -cc=bpm <defects.lst>`**, replacing `-cc=dark`; without a map the classic
`-cc=dark` still runs. On the reference ASI1600MM Pro this finds ~1,500 genuinely unstable pixels
on top of the warm/cold population.

## Matching rules (which masters calibrate a light set)

`calib.MatchForLight`:

| Master | Rule |
|---|---|
| dark | same gain/offset/bin **and same exposure**, nearest temperature within ±5 °C; deepest pool wins ties |
| dark (no exact exposure) | a same-camera dark of a *different* exposure + a bias → Siril **dark optimization** (`-opt`) scales its thermal signal onto the lights |
| flat | same filter preferred; else *any* session flat (most dust sits on the sensor window, common to every filter) — noted in the run |
| bias | same gain/offset/bin, deepest pool |
| bad-pixel map | the matched dark's `_defects.lst` sidecar when present (pulled from the S3 library mirror on demand) |

Every choice, fallback and skip is recorded as a human-readable note in `run.json`
(`channels[].selection.notes`).

## Phone (iPhone DNG) calibration masters

Milky-way captures calibrate through a **separate** library (`phone_calib_masters` table,
`internal/calib/phone.go`): masters keyed by **ISO + exposure + sensor dimensions + camera model**
(not gain/offset/bin), because they are applied **per-pixel in Go, in linear light** by the
nightscape recipe — never by Siril. Masters are dimension-guarded (a mismatched-resolution master
is dropped, never applied), built automatically when a milkyway run includes cal frames, and
reused by later runs. Kept apart so the deep-sky matcher can never pick a phone master. See
[modes/milkyway.md](modes/milkyway.md).

## Capture-side advice the engine gives you

Calibration removes the *deterministic* part of the noise; what remains at fixed sensor positions
only goes away if the sky moves randomly against the sensor between subs. The pipeline diagnoses
this per run (the **Pointing** verdict — see [pipeline.md](pipeline.md)): a session classified
*drift* or *static* gets a run warning recommending **random dithering** (~10 px between subs) —
with dithering, the adaptive rejection removes residual fixed-pattern noise entirely.
