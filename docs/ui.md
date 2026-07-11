# The web UI

A Vue 3 single-page app (left rail navigation) with two areas: the **session planner** (Tonight /
GoTo / Calendar — see [planner.md](planner.md)) and the **Processing hub**. Plus **AstroAgent**, a
chat page over the local vision model. Routes live in `frontend/src/router/index.ts`.

## Processing hub (`/processing`) — six tabs

### Import (`/processing/import`)

The main page: browse the data dir (plus removable drives and S3 mirrors), **multi-select capture
folders**, and *Inspect* them into one merged inventory — stat cards per frame type, channel
mapping (overridable when detection confidence is low), light/calibration/file tables, and
warnings. Then configure the run:

- **Preset** — a catalog of 16 built-in "best params per situation" recipes (galaxy, faint galaxy,
  star cluster, reflection/emission/planetary nebula, SHO/HOO/Foraxx narrowband, moon, planet,
  comet, three milkyway looks) plus your own saved presets (Save… persists to the DB). A preset
  prefills mode/format/palette/toggles/advanced params; everything stays editable.
- **Mode · Output · Storage** — the five processing modes; image/video/both; *Full local* or
  *Full S3 (free local after)* (see [storage-s3.md](storage-s3.md)).
- **Toggles + Advanced parameters** — SPCC, denoise, Ha handling, wheel-transition drop, palette,
  and the per-mode advanced knob editor; *Run with local AI agent* opts into the
  [finish supervisor](agent.md).
- **Cross-session reuse** — prior sessions of the same target are auto-discovered and folded in
  (deselectable per session).
- **Calibration preview** — which library masters would match each light set, before launching.

**Run pipeline** starts immediately; **Add to queue** appends to a strictly-sequential lane. The
*Processing history* panel below lists every past job with a one-click **Use again**.

### Live (`/processing/live`)

Incremental live stacking during capture — watch a local folder or S3 prefix, see the stack grow
with a live preview, then **Stop & finalize** to run the full pipeline. See
[modes/livestack.md](modes/livestack.md).

### Tasks (`/processing/tasks`)

The job list (running + history) and the **job detail** view:

- While running: live progress over SSE — progress bar, current step, live CPU/RAM of the running
  tool, a rolling log, live preview, and the milestone preview timeline as it grows. **Pause**
  parks a resumable checkpoint (mid-stack pause keeps the finished channels; transient S3 errors
  auto-pause and auto-resume). **Cancel** stops the run — a cancel during finishing keeps the
  partial result and marks the job *cancelled*.
- When finished: the full result panels — final image/video, per-channel stats (frames in/stacked,
  matched Dark/Flat/Bias, and the **Pointing** column with the dither/drift verdict), per-frame
  grade charts, masters used, calibration notes, warnings, run options (provenance), download
  links, and for supervised runs the **supervisor panel** (one card per iteration, scores +
  reasoning + the chosen best). Finished pages read the stored result directly (no event stream).
- **Stage timeline & re-run** — each processing milestone is a card; edit a stage's parameters and
  **re-run from that stage** (cheap tiered re-entry, deep-sky). **Refine** re-runs only the finish
  through the supervisor; **Retry tuned** re-processes with adjusted params.

### Runs (`/processing/runs`)

A gallery of on-disk runs (independent of the DB — anything with a `run.json`), re-rendering the
same result panels.

### Library (`/processing/library`)

The calibration-master library: darks/flats/bias/dark-flats and phone masters with their keys
(gain/offset/bin/exposure/temp), frame counts, and the **Copy library to S3** mirror action (see
[calibration.md](calibration.md) and [storage-s3.md](storage-s3.md)).

### Storage (`/processing/storage`)

S3 connections (endpoint/key/secret — secret encrypted at rest, never shown again), default
connection selection, bucket/prefix pickers, folder sync/download/**free local** (verified),
per-folder local/S3 presence, a plain bucket file manager, backup/restore, and removable-drive
import.

## AstroAgent (`/astroagent`)

A chat page over the local vision model with live tool access: ask about your data, runs, sky and
setup; attach an image for a measured critique; let it launch or tune runs — every mutating action
is **confirmation-gated**. Supervised/refine runs also surface here as steerable conversations.
See [agent.md](agent.md).

## Conventions the UI follows

- All state that matters is server-side; the browser keeps only preferences/favorites (which the
  [backup](storage-s3.md#backup--restore) captures as `appstate`).
- Every long operation is a job with SSE progress; nothing blocks the page.
- File/preview URLs transparently fall back to S3 when a local file was freed.
