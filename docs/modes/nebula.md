# nebula

## What & when

Large, faint **emission objects** (HII regions, supernova remnants, planetary nebulae) shot with
the same mono LRGB+Ha rig as deepsky. Pick `nebula` over `deepsky` when the subject is extended
emission rather than a galaxy/cluster: grading is more lenient (faint-signal subs with fewer stars
must survive), the finish is **Ha-forward**, and **StarNet++ star reduction** is on by default so
the nebulosity is not buried under the star field.

## Detection & inputs

Identical to [deepsky](deepsky.md#detection--inputs): the same `inspect.ScanMany` classification
(headers → filename/folder tokens → EFW wheel slot → `info.txt` legend → pixel statistics →
signal-based channel detection), the same set grouping, the same `info.txt` sidecar support for
bare-filename legacy captures, the same Bayer-frame exclusion.

## Algorithm, end to end

Nebula runs the **same engine path** as deepsky — `pipeline.Process` in
`internal/pipeline/pipeline.go`, same ordered steps and same Siril commands (see
[deepsky.md → Algorithm](deepsky.md#algorithm-end-to-end)). The preset changes what those steps do:

1. **Grade** — lenient thresholds (roundness floor 0.50, FWHM/roundness/background sigma 3.0, star
   count fraction 0.4): nebula subs legitimately have few stars and softer stats, so the
   median+MADσ rules keep more of them.
2. **Cross-frame transient mask** — same `TrailMaskK = 3.0` pre-stack cleanup.
3. **Per-channel linear finishing** — GraXpert background extraction on (`BackgroundAI`), denoise
   `-vst` chroma 0.85 / lum 0.30.
4. **Finish prep** — the finish `subsky` degree is 2 (falls to the gentle degree 1 when GraXpert
   ran). **No combined-RGB GraXpert pass and no AI colour denoise**
   (`CombinedBackgroundAI`/`ColorDenoiseAI` are off in this preset — the deepsky homogeneous-sky
   passes are tuned for broadband galaxies); the colour-calibration ladder (SPCC → star-field gains
   → neutralization, `internal/postprocess/colorcal.go`) and the calibrated-only linked stretch are
   identical. Stretch target background 0.09 — a touch brighter than deepsky, keeping faint
   nebulosity visible.
5. **Ha handling** — the reason this mode exists:
   - Ha is RBF-flattened before the screen (`HaRBF`, `siril.SubskyRBFCmd`) so a residual gradient
     cannot paint a red wash;
   - `HaBlackPoint = 0.07` — a lighter clip than deepsky (Ha *is* the subject; only the sky
     pedestal is zeroed);
   - `HaScreen = 0.50` — a stronger red screen;
   - `HaExcludeStars` median-removes point sources so stars don't tint orange.
6. **GIMP compose** (`internal/gimp/compose.go`) — the value `Curve` lifts faint nebulosity
   (`0.20→0.27`, `0.5→0.58` control points); there is **no `LumCurve`** (no galaxy core to shape),
   but the L core-highlight shoulder (0.64/0.76) and the star-safe highlight shoulder (0.85/0.96)
   are the same as deepsky.
7. **StarNet++ star reduction** — `StarReduce = 0.5` by default: the flattened composite is
   de-starred and the stars are screened back at 50 % (`reduceStarsAI` in
   `internal/pipeline/enhance.go`), producing `final_reduced.{tif,png}` alongside the full-stars
   final.
8. **Finish-quality stamp** — every run is measured and warned exactly as deepsky
   (`internal/pipeline/finishquality.go`).

## Preset knobs & defaults

From `mode.For(mode.Nebula)` in `internal/mode/preset.go`. Only the values that differ from
[deepsky](deepsky.md#preset-knobs--defaults) are listed; everything else (agent tunability, tiers,
patch keys in `internal/pipeline/params_patch.go`) is shared — nebula uses the same
deepsky/nebula patch set.

| Knob | nebula | deepsky | Why |
|------|--------|---------|-----|
| `Grade.RoundnessFloor` | 0.50 | 0.55 | keep soft faint subs |
| `Grade.RoundnessSigma`/`FWHMSigma` | 3.0 | 2.5 | lenient outlier rejection |
| `Grade.StarCountFrac` | 0.4 | 0.5 | nebula fields have few stars |
| `BackgroundDegree` | 2 | 1 | stronger polynomial when GraXpert is off |
| `HaScreen` | 0.50 | 0.42 | Ha-forward blend |
| `HaBlackPoint` | 0.07 | 0.12 | Ha is the subject — clip only the pedestal |
| `Saturation` | 0.10 | 0.12 | slightly gentler |
| `Curve` | faint-nebulosity lift | gentle S | subject shape |
| `LumCurve` | *(none)* | galaxy lift | no galaxy core |
| `DenoiseLum` | 0.30 | 0.50 | preserve faint structure |
| `ChromaBlur` / `CropFrac` | 0 / 0 | 0 / 0.035 | no edge crop by default |
| `CombinedBackgroundAI` | **false** | true | no 2nd combined-RGB pass |
| `ColorDenoiseAI` | **false** | true | no AI denoise on the combined RGB |
| `StarReduce` | **0.5** | 0 | star-reduced finish by default |
| `BackgroundLevel` | 0.09 | 0.06 | keep faint emission visible |

## Soft-fail fallbacks

Identical table to [deepsky](deepsky.md#soft-fail-fallbacks), with these mode-specific notes:

| Condition | Behavior |
|-----------|----------|
| GraXpert absent/unhealthy | Siril `subsky 2` handles the gradient (this preset's degree); the combined-RGB RBF pass does not apply (disabled by preset) |
| StarNet++ absent or fails | the default star reduction silently degrades to **full stars** — surfaced by the up-front `aiToolWarnings` warning and a finish note |
| SPCC fails | star-field gains → neutralization + unlinked stretch, exactly as deepsky |
| GIMP absent | Siril `rgbcomp` finish (no Ha screen layering, no star reduction) |

## AI supervisor

Same loop, tiers, budgets, history/warm-start, scoring and agent tools as
[deepsky](deepsky.md#ai-supervisor) — the deepsky renderer (`internal/pipeline/supervise_deepsky.go`)
serves both modes, `scoreFinishMode` applies the same deepsky/nebula colour guardrails + star-tint
guard, and the knob whitelist is the shared tiered deepsky/nebula set. The objective text sent to
the model embeds this preset's own background target (0.09) so the agent optimizes for the
brighter nebula sky, and a tuned `star_reduce` (Tier B) re-runs StarNet on the winning composite.

## Outputs & artifacts

Same run-directory layout as [deepsky](deepsky.md#outputs--artifacts). Because `StarReduce`
defaults to 0.5, a healthy run additionally always contains `final_starless.tif` and
`final_reduced.{tif,png}`; `run.json` carries the same `engine` stamp and `finish_quality`
snapshot.

## Config/env

Identical to [deepsky](deepsky.md#configenv). `STARNET_BIN` matters more here (the preset enables
star reduction); GraXpert health (`pipx inject graxpert onnxruntime`) affects only the per-channel
extraction since the combined pass is disabled in this preset.
