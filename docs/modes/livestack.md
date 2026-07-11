# livestack

## What & when

**Live stacking during a capture session**: point AstroStack at the folder (or S3 prefix) your
capture software writes into, and watch the stack build up in near-real-time — each new sub is
calibrated once, the growing pool is re-registered and re-stacked, and a preview streams to the
UI. When you press **Stop**, the session is finalized with the full standard pipeline, so the
result is identical to processing the folder after the fact.

Start it from **Processing → Live** (`/processing/live`) — pick the watch source, mode
(deepsky-style mono or OSC) and options. Code: `internal/livestack`, `internal/source`,
`internal/pipeline/live.go`.

## Detection & inputs

The watcher polls the source every `ASTRO_LIVESTACK_POLL_SEC` (default 3 s):

- **Local folder** (`source.NewLocal`) — a file is ingested only after its size has been stable
  for `ASTRO_LIVESTACK_STABILITY_SEC` (default 2 s), so half-written frames are never read.
- **S3 prefix** (`source.NewS3`) — new objects are mirrored to a local working dir first; paths
  are confined to the configured prefix.

Frames classify through the same `internal/inspect` tiers as a batch run. Calibration frames
found in the watched tree build masters on the fly; lights arriving before the masters are pooled
raw and calibrated as soon as the masters exist.

## Algorithm, end to end

1. **Ingest** — each new light is calibrated **once** (`CalibrateLightsLive`) with the current
   masters and cached in a growing calibrated pool.
2. **Re-stack** — after every `ASTRO_LIVESTACK_RESTACK_EVERY` new lights (default 1, rate-limited
   by `ASTRO_LIVESTACK_MIN_INTERVAL_SEC`), the whole pool is re-registered and re-stacked
   (`StackLinearLive`: count-adaptive rejection, `-weight=wfwhm`), producing a downscaled preview
   for the UI — no heavy finishing while capturing.
3. **Finalize on Stop** — the session runs the **full pipeline** (`pipeline.Process`, or
   `ProcessOSC` for raw/Bayer sources) over everything collected, on a fresh context so the job
   records a proper success (not a cancellation). All the standard machinery applies: grading,
   transient mask, pointing diagnosis, colour ladder, GIMP finish, `run.json`.

## Knobs & config

| Variable | Default | Role |
|---|---|---|
| `ASTRO_LIVESTACK_POLL_SEC` | 3 | source poll interval |
| `ASTRO_LIVESTACK_STABILITY_SEC` | 2 | size-stable wait before ingesting a local file |
| `ASTRO_LIVESTACK_RESTACK_EVERY` | 1 | re-stack after every N new lights |
| `ASTRO_LIVESTACK_MIN_INTERVAL_SEC` | 0 | minimum seconds between re-stacks |

The finalize step uses the selected mode's preset — every knob in
[deepsky.md](deepsky.md#preset-knobs--defaults) applies to the final image.

## Soft-fail fallbacks

| Condition | Behavior |
|-----------|----------|
| No masters yet | raw subs pooled; calibrated as soon as masters build |
| A sub fails to calibrate/register | skipped with a note; the session continues |
| S3 source hiccup | poll retries; nothing is lost (objects are immutable) |
| Stop with too few frames | finalize still runs; grading keeps at least one frame |

## Outputs & artifacts

Identical to the finalizing mode's outputs (`output/<object>/<runID>/` with `run.json`, masters,
final image) — plus the live preview stream visible during the session in the Live tab.
