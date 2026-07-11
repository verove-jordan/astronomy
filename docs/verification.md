# Verification recipes

End-to-end, per-mode checks with **objective pass criteria** — run these after a significant
pipeline change to prove the change works on real data, not just in unit tests. All commands assume
the standard host setup (`just up` + `.env` from `.env.example`); sample capture data lives under
`input/`.

> **Docker engine:** everything below describes the *code you built*. To verify the containerized
> stack you **must rebuild the engine image first** — `just stack-build` — otherwise the container
> keeps running the previous build (recognizable by its `engine` stamp; see
> [Provenance](#provenance) below).

## deepsky

```bash
just process deepsky image input/m31/autorun     # or any mono LRGB folder (e.g. an M101 session)
```

Open the run's `output/<object>/<runID>/run.json` and check:

- **Colour calibration actually ran** — `final.notes` must contain either
  `SPCC color calibration applied` or
  `SPCC unavailable — star-field photometric fallback (gains R=… B=… from N stars)`
  (`postprocess.ColorCalibrate` in `internal/postprocess/colorcal.go`). A note ending in
  `background-neutralization fallback` means both photometric rungs failed — investigate
  plate-solve/SPCC config before trusting colour.
- **No warm cast** — `final.finish_quality.warm_cast` ≤ **0.015** (the shared threshold in
  `internal/pipeline/finishquality.go`; a breach also appears as a
  `finish quality: warm sky cast` warning).
- **Gradient removal never silently skipped** — if `final.notes` contains a
  `combined GraXpert … skipped` note it must be paired with an RBF note on the same line
  (`— RBF subsky fallback applied`, or an explicit `combined RBF subsky skipped: …` error). A
  GraXpert skip with no RBF note means the combined-gradient pass shipped nothing
  (`extractCombinedBackground` in `internal/pipeline/enhance.go` — regression guard).
- Sanity: `warnings` contains no `finish quality:` entries; `final.png` shows a neutral dark sky
  and varied (not uniformly orange) star colours.

### Noise & calibration (same run)

- **Adaptive rejection engaged** — the Siril log (job log tail, or `-v` on the CLI) must show the
  algorithm matching each pool's size: `Pixel rejection ........... GESDT clipping` with
  `outliers=0.300 significance=0.050` for a ≥ 50-frame pool (e.g. a 100-offset bias master),
  `winsorized sigma clipping` for 8–49, `percentile clipping` for ≤ 7
  (`siril.Rejection` in `internal/siril/scripts.go`).
- **Dark defect map built and applied** — after a run whose dark pool has ≥ 8 raw darks:
  `library/master_DARK_*_defects.lst` exists next to the master, and the log shows
  `Cosmetic correction from Bad Pixel Map: …_defects.lst` for every calibrated channel
  (`calib.ScanDarkDefects` + `-cc=bpm` in `internal/siril/scripts.go`). With < 8 darks the scan is
  skipped and `-cc=dark` appears instead.
- **Pointing verdict recorded** — `run.json` `channels[].dither.pattern` is one of
  `dithered|mixed|drift|static` (≥ 5 registered frames), the Tasks job page shows the **Pointing**
  column, and a `drift`/`static` session carries exactly one run warning recommending dithering
  (`internal/dither`, `appendDitherAdvice`).
- **Preset catalog anti-drift** — `go test ./internal/preset` (locks the 16 built-in recipes
  against accidental default changes).

## milkyway

```bash
just process milkyway image input/MilkyWay/13_05_2026/DNG    # any input/MilkyWay/*/DNG session
```

- **Orientation** — `final.png` must display in the same orientation as the source photo opened in
  Preview/Photos (the EXIF decision in `internal/nightscape/develop.go` `orientDecision` is applied
  exactly once). A sideways or upside-down result is a fail; re-run with an explicit
  `orientation` override only to diagnose, not to pass.
- **Host vs Docker parity** — run the same input through the host engine and the `stack` engine
  (after `just stack-build`): with `dcraw_emu` on both sides the develop + grade are photometric
  and deterministic, so the two `final.png` should be visually identical (compare hashes;
  registration requires the same Siril version — use an amd64 engine or accept the arm64 distro
  Siril ~1.2 caveat in `docs/architecture.md`).
- **Background level** — the sky background should sit near the preset target **0.05**
  (`Preset.BackgroundLevel`, "balanced"). Objective check: in the AstroAgent chat run
  `view_result_image` on the job — the grounding report prints the measured `Background`
  (`pipeline.ResultImagePayload`); expect ≈ 0.05 ± 0.02 for the default brightness.
- **No banding** — inspect the smooth sky gradient at 100 %: the dithered 8-bit export
  (`to8Dithered` in `internal/nightscape/nightscape.go`) must show fine grain, never posterized
  contours.

## planetary

```bash
just process planetary image input/moon/autorun
```

- **The stack must out-detail the best single frame** — the objective acceptance
  (`internal/planetary/planetary.go`): `master_lapvar` ≥ **1.05 ×** `best_frame_lapvar`. Both
  fields are in the **job result** (Tasks → job detail, or `GET /api/jobs/<id>`; they are part of
  the flat planetary result, not the on-disk `run.json`). On a CLI run, the equivalent check is
  the **absence** of the
  `warning: stacked master sharpness (…) below 1.05x the best single frame` note.
- Sanity: the finished `<object>_stack.png` resolves visibly finer crater detail than any single
  `vid_*.fits` frame; no ringing halos (over-deconvolution) on the limb.

## comet

```bash
just process comet image input/C2019/c2019_y4
```

- **Track fitted** — `run.json` warnings contain
  `comet: motion track fitted from k/n detections` with k ≥ 4 and k ≥ ⅔·n
  (`cometTrack` in `internal/pipeline/comet.go`). A `comet alignment skipped` warning fails the
  scenario (unless you are testing the star-aligned-only degrade path).
- **Trail residuals gone** — compare the comet master (`comet_master_<filter>.fits`, stacked with
  the asymmetric winsorized **4 / 1.8** rejection, `siril.StackCometScript`) against a control
  stack of the same comet-aligned frames with the symmetric 3 / 3 rejection
  (`siril.StackAlignedScript` over the run's `work/…/comet_<filter>/c_*.fits`): the marching star
  trails visible in the 3/3 control must be absent from the 4/1.8 master.
- **Tail preserved** — the faint tail must survive both the asymmetric stack (σ-low 4 never clips
  it) and the final composite: `comet_final.png` must show the tail continuing **under** star
  halos (the `min(1, comet + stars)` ADD composite — a `max()` regression replaces tail with star
  pixels there).
- Sanity: pinpoint stars (no comet smear in the star layer), no R/G/B colour separation on the
  coma (the `alignCometMasters` cross-registration).

## Agent mode (supervised runs + chat loop)

Start the model (`just run-ia-model`), then launch any run with the supervisor (Import → "Run with
local AI agent", or `--supervise`). Verify in the job log / supervisor panel:

- a **warm-start line** when the target has prior supervised passes:
  `warm start: seeded from the best prior pass of this target (job N, tier X, score S — …)`
  (`warmStart` in `internal/pipeline/supervise_history.go`);
- per-pass **history lines** `iter N [tier] score X (metrics Y, model Z) — …`, and iteration cards
  with params/defects/scores, the winner marked chosen;
- the loop respects budgets and stops on plateau or `done` + target score
  (`internal/pipeline/supervise.go`).

Then exercise the **chat closed loop** (AstroAgent):

1. `get_mode_params` for the run's mode → returns defaults + the knob menu
   (`internal/agent/tools_params.go`);
2. `view_result_image` on the finished job → the model receives the full frame + 100 % centre crop
   and the objective measurement report;
3. `retry_run_tuned` with a small param change (`restack:false`) → a new refine job appears,
   inherits the target's warm-start memory, and its result reflects the change.

## Weather grid

```bash
curl -s 'http://localhost:8080/api/sky/weather/grid' | jq '{nx:.grid.nx, ny:.grid.ny, layers:(.grid.layers|keys), steps:(.grid.timesteps|length)}'
```

Pass: `nx = ny = 32` (default `ASTRO_WEATHER_GRID_SIZE`) and the layers include **`clouds`,
`clouds_low`, `clouds_mid`, `clouds_high`** (the composite expansion in
`internal/weather/provider.go` `expandGridLayers`), with a non-empty `timesteps`. On the Tonight
map, the cloud overlay animates with play/scrub and shows the per-altitude composite.

## S3 (transfer safety)

Local MinIO one-liner:

```bash
docker run --rm -d --name minio -p 9000:9000 -p 9001:9001 \
  -e MINIO_ROOT_USER=astro -e MINIO_ROOT_PASSWORD=astrosecret \
  minio/minio server /data --console-address ":9001"
```

Point a connection at it (Processing → Storage: endpoint `localhost:9000`, no SSL — or
`ASTRO_S3_*` env), create a bucket, **sync** a capture folder up, then:

- **removeLocal refusal** — corrupt one local file *keeping its size*
  (e.g. `dd if=/dev/zero of=<file> bs=1 count=16 conv=notrunc`), then run "Free local" on the
  folder. Pass: the job **aborts with
  `remove-local aborted — <file> is not safely backed up on S3 (nothing deleted)`** and no local
  file is removed — the verifier compares content MD5 (upload metadata / single-part ETag), so a
  same-size-different-bytes object can never cost the only good copy
  (`verifyMirrored` in `internal/transfer/removelocal.go`).
- Restore the file (re-sync) and re-run "Free local": it now deletes, and previews/results still
  serve via the S3 fallback.

## Provenance

- `curl -s localhost:8080/api/health | jq .engine` → `{version, built_at}` from
  `internal/buildinfo` (ldflags-injected; `"dev"` means an un-stamped `go run` binary).
- Every run's `run.json` carries the producing build in `engine`; the **Runs gallery and run
  result panels show it as a chip** (`frontend/src/components/Common/EngineChip.vue`): grey when
  it matches the serving engine, **amber** (with a "built with an older engine" tooltip) when the
  run was produced by a **stale** build — the tell for "the fix didn't work" images from an
  un-rebuilt engine. The chip hides for `dev` runs.
- Containerized: after any code change, `just stack-build` then re-run — the new runs' chips must
  show the new version, and `/api/environment` must be green for every tool the scenario needs
  (including the GraXpert ONNX deep probe).
