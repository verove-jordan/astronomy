# Stacking & rejection

How AstroStack turns N registered frames into one master, and how to steer it.

The choice lives in **Processing → Advanced parameters → Stacking & rejection**, and on a finished
run in the per-stage rerun editor (it is a Tier-C change: only a re-stack can reflect it). Everything
on that panel is a key in the run's `params` JSON, so it is captured by presets, shown in the job's
param chips, and recorded in `run.json`.

The catalogue is defined once, in Go, in **`internal/stackalg`**. The API serves it
(`GET /api/mode-params` → `stack_menu`) and the UI renders whatever it is given — there is no second
list of algorithms anywhere in the frontend.

---

## Which modes this covers

| Mode | Combined by | Panel |
|---|---|---|
| deepsky, nebula, livestack, mosaic, comet | Siril `stack` (or the Go combiner) | yes |
| planetary | Go lucky-imaging stack (`internal/planetary`) | no — see `best_percent`, `drizzle_scale`, `align_points` |
| sun | Go solar stack (`internal/solar`) | no — see `keep_percent`, `clip_sigma`, `drizzle` |
| milkyway | Go nightscape composite (`internal/nightscape`) | no — see `look`, `brightness` |

Planetary and solar frames are *lucky-imaging* stacks: they are selected and weighted by sharpness,
per alignment point, and their combination is inseparable from that machinery. Offering a
"Winsorized vs GESD" choice there would be a knob that does nothing.

## The default: automatic, by frame count

Left alone, the engine picks the rejection from the number of frames that actually survive grading.
This is `stackalg.AutoReject`, and it is unchanged from before the panel existed:

| Frames | Rejection | Why |
|---|---|---|
| ≤ 7 | `percentile 0.2 0.1` | a measured sigma is meaningless on a handful of samples |
| 8 – 49 | `winsorized 3 3` | the proven all-round default |
| ≥ 50 | `generalized 0.3 0.05` (GESD) | markedly better on the outlier tails of a deep stack |

The panel badges whichever of these applies to your deepest channel, so "automatic" is never opaque.

## Combination methods

| Method | What it does | Expect |
|---|---|---|
| **Average** (default) | adds and divides | optimal signal-to-noise under Gaussian noise; the right answer for essentially every deep-sky stack |
| **Median** | middle value per pixel | robust with no tuning, but noise falls as if you had ~64 % of your frames |
| **Sum** | pure addition, no rejection, no normalization | every photon kept — and every trail, cosmic ray and aircraft too |
| **Maximum** | brightest sample per pixel | star-trail and meteor composites, where transients are the subject |
| **Minimum** | darkest sample per pixel | a diagnostic: what is present in *every* frame |
| **Trimmed mean** *(Go engine)* | drops a fixed fraction at both ends, averages the rest | the average's depth with the median's tolerance, and completely predictable |

Siril accepts no rejection, normalization or weighting on sum/min/max — the panel disables those
controls rather than emitting flags Siril would ignore.

## Rejection algorithms

| Algorithm | Parameters | What it does | When it is the right answer |
|---|---|---|---|
| **No rejection** | — | keeps everything | you want every transient (or you have already cleaned the frames) |
| **Percentile clipping** | low/high **fractions** | drops a fixed share at each end | tiny stacks, where a measured sigma is noise |
| **Sigma clipping** | κ low/high | iterative mean ± κ·σ | general use; a bright outlier inflates the σ it is judged against |
| **Median sigma clipping** | κ low/high | the same, centred on the median | asymmetric contamination |
| **Winsorized sigma clipping** | κ low/high | pulls extremes to the edge *before* estimating σ | **the default for 8–50 frames** — removes trails without eating faint signal |
| **Linear fit clipping** | κ low/high | fits a robust line through each pixel's samples | **a sky that moved**: rising moon, drifting light pollution, a walking gradient |
| **GESD** | outlier **fraction**, **significance** | a formal repeated extreme-value test | **the default past 50 frames** — catches the correlated leftovers a fixed 3σ clip misses |
| **MAD clipping** | k low/high | clips at k × median absolute deviation | very ugly data; blunter than Winsorized |
| **Robust Chauvenet** *(Go)* | significance | Chauvenet's criterion with robust statistics | adapts its aggressiveness to the stack depth on its own |
| **Auto-adaptive weighted** *(Go)* | — | iterates a per-sample weight to convergence | no hard threshold, so no threshold artefacts (DeepSkyStacker's signature mode) |
| **Entropy-weighted** *(Go)* | — | weights by local information content | variable seeing, where you want the frames that actually resolve detail there |

**The two parameters do not always mean sigmas.** For percentile clipping they are kept fractions;
for GESD they are an outlier fraction and a significance level. The panel relabels the fields for the
algorithm in force, and each algorithm's usable range is applied when the command is built — the
value you store is always exactly what you typed.

## Normalization and weighting

Normalization brings frames onto one photometric footing before they are combined:
`addscale` (default — levels background *and* contrast), `add`, `mul` (the physically correct choice
for flats), `mulscale`, or `none` (correct for bias/dark masters, where the pedestal *is* the signal).

> Siril accepts `-norm=additive` and then **silently ignores it**. That is why normalization is a
> closed enum here: only `add`, `addscale`, `mul` and `mulscale` are real.

Weighting decides how much each frame counts: `wfwhm` (default — sharpness), `noise`, `nbstars`,
`nbstack`, or none.

## Engines

`auto` runs Siril unless the chosen algorithm is one Siril does not implement, in which case the run
switches to the Go combiner (`internal/stacknative`). You can pin either engine explicitly. The
engine choice is **consent-gated**: a warm-started rerun will never resurrect it on its own.

The Go combiner runs over the frames Siril has **already registered**, so registration, drizzle and
interpolation stay Siril's job and only the pixel combination changes hands. Memory is bounded by
streaming 64-row bands rather than holding the sequence: a 60-frame ASI1600 sequence peaks around
70 MB in flight against 3.9 GB for the whole thing. It parallelizes across bands and honours
cancellation.

It does **not** soft-fail. If a native stack cannot run, the channel fails with a clear error rather
than quietly falling back to a different algorithm — a master must never claim an algorithm that did
not produce it.

**Parity.** For the algorithms both engines implement, `TestParity_NativeMatchesSiril` stacks one
identical sequence with each and requires the masters to agree on sky level (within one noise
sigma), sky noise (within 35 % — the residual is Siril's IKSS scale estimator against our MAD) and
star peak (within 5 %). They are independent implementations and never agree bit for bit; what has
to match is the astronomy. `TestParity_BothEnginesRemoveTheTrail` plants a satellite across one sub
and requires both masters to erase it.

## Calibration masters — one recipe per frame type

The lights are not the only stack a run performs: the bias, darks, flats and dark-flats each get
their own master, each stacked separately. Their pools differ by an **order of magnitude** — 200 bias
frames and 5 flats want opposite algorithms — so the panel gives each frame type its own recipe, and
each resolves its own count-adaptive recommendation from its own pool depth.

Only the types the inspected capture actually holds are offered; a row for flats you did not shoot is
noise.

| Frame type | Keys | Normalization | Notes |
|---|---|---|---|
| Bias / offset (the same frame) | `master_bias_{combine,reject,low,high}` | none, fixed | The read-out pedestal. Usually your deepest pool, so GESD often beats the default. |
| Darks | `master_dark_*` | none, fixed | Thermal signal and hot pixels. The rejection decides which flickering (RTS) pixels reach the defect map. |
| Flats | `master_flat_*` | multiplicative, fixed | Vignetting and dust. Small pools — a gentle test beats an aggressive one. |
| Dark-flats | `master_dark_flat_*` | none, fixed | The flats' own dark; it calibrates the flats, so it stacks like a dark. |

**The normalization is not exposed here — it is physics, not taste.** Bias and dark stack
un-normalized because their pedestal *is* the signal (levelling it would erase what is being
measured); a flat stacks multiplicatively because only its relative shape matters.

**A non-default recipe builds its master under its own filename.** Masters live in a shared library
keyed by camera settings, so a master stacked with, say, GESD carries a short recipe fingerprint
(`…_g200o10_b1_-10C_s7f2a91`). It can therefore never overwrite — or be silently reused in place of —
the default-options master your other runs depend on, and re-running with the same recipe reuses the
variant normally. Default options add no suffix at all, so existing library masters keep their names.

The standalone **"Build masters → library"** job honours these knobs too — it is where they matter
most.

## Comet mode

A comet run stacks twice. The star-aligned half uses the ordinary `stack_*` settings; the
comet-aligned half has its own (`comet_stack_*`) because its rejection is deliberately
**asymmetric** — the coma sits still while the stars march through it, so σ-high is tight (1.8) to
erase the trails and σ-low is loose (4) so the faint tail's noisy samples survive.

## Verifying a change

Every clause the panel can emit is exercised against the real `siril-cli` by
`TestSirilLive_StackClauseGrammar` (`internal/siril/syntax_live_test.go`), which asserts Siril's own
end-of-stack summary reports the algorithm and normalization that were asked for — because a
mis-spelled flag fails *silently*, not loudly.

`TestStackClause_DefaultsAreByteIdentical` pins that the defaults still render the exact command the
engine emitted before any of this was configurable.

## Keys

`stack_engine`, `stack_combine`, `stack_reject`, `stack_reject_low`, `stack_reject_high`,
`stack_trim_frac`, `stack_norm`, `stack_fast_norm`, `stack_weight`, `stack_rejection_maps`,
`stack_feather`, `stack_local_norm`, `stack_local_norm_degree`; per calibration frame type
`master_{bias,dark,flat,dark_flat}_{combine,reject,low,high}`; and for comet mode
`comet_stack_reject`, `comet_stack_low`, `comet_stack_high`.

Each is documented in the Advanced-parameters glossary (`paramDocs.<key>`, en + fr).
