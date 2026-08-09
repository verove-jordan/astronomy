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
- **3D** — a view chip beside Final and the channel previews, on any run whose field solved. The
  depth slider starts at 0 %, where the scene is pixel-for-pixel the photograph; opening it spreads
  every detected star along its own line of sight at its own distance. **Fly out** frames everything
  the scene holds: it opens the depth, swings the camera off the axis *and* opens the lens, because no
  two of those alone show anything — from Earth the picture looks the same at every depth, and a
  field's cone is 87 times deeper than it is wide, so the lens that photographed its tip cannot see
  along it. The reset button winds all three back to the photograph. Drag to orbit,
  shift-drag or two fingers to pan, scroll or pinch to zoom (the gain rises with how fast the gesture
  moves, so a slow scroll places the camera precisely and a flick crosses decades). Hovering a star
  gives the same catalogue card the 2D overlay shows, plus its distance, how that distance was
  obtained, its true angular size and its space velocity. Stars are drawn at their blackbody colour
  and brighten as you approach them. Catalogued objects take a real shape where one can be
  derived — an inclined disc, an expanding shell, a modelled emission volume — each labelled with
  whether its geometry is measured, assumed or modelled, and citing its source when it has one.
  Measured parallaxes and colour-derived estimates are drawn differently and counted separately, one
  toggle hides the estimates, and the frame's own cross-validation score is shown whenever it says
  they are unreliable. A motion layer draws where each star with a measured velocity will be after a
  chosen span of time. A **Milky Way** layer draws the Galaxy around the field — 180 000 stars sampled
  from published structure, with the arms, bar, discs and halo where the measurements put them — and its
  own zoom slider flies from inside your field out to the whole disc, and on past it to a vantage that
  holds the Galaxy and the run's own galaxy in one frame at true relative scale. The readout beside it
  is how much sky is in frame, from tens of parsecs to megaparsecs, and decade rings mark the gap. The
  legend says plainly that the structure is a model while the stars' distances are measured, and that
  dust is not modelled at all.
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

## Capture (`/capture`)

Live camera, mount and filter-wheel control, the auto-run sequencer, and — in the right-hand
column — **Polar alignment**: a four-step procedure that measures where the mount's polar axis really
points from plate-solved frames, and then draws a ring on the live image to drive the crosshairs into.
It never commands the mount; you turn the right-ascension axis by hand between frames, which is what
lets it work on any mount at all. Full procedure and accuracy figures in `docs/mount.md`.

## Logbook (`/logbook`)

Every capture session, past and current — the answer to "what did I shoot, when, through what, and
under what sky?", which is what a later stacking decision is actually made from.

The **list** is one row per session (`GET /api/capture/sessions`, filterable by object and date, with
the unpaged total): observing night, object, status, frames done/planned, per-filter chips,
integration, duration, and the night's sky condensed to a single 0–100 score with a four-dot glyph.
The score is the **median hourly weather verdict** the provider already computed — not a new scoring
formula. A running session's row is tinted and refreshes on a slow poll; the live view stays on the
capture page.

The **detail** (`/logbook/:id`) opens one night in full:

- **What was shot** — per filter and frame type, aggregated in Postgres from `capture_frames`
  (`store.CaptureFrameStats`): frame count, integration, exposure/gain/bin ranges and the sensor
  temperature min/avg/max. Ranges collapse to a single value when nothing changed mid-run.
- **Capture order** — one band per filter across the session's clock, a mark per sub. Lined up with
  the conditions chart below it, this is what answers "did the L set finish before the cloud, or is
  half of it from the bad hour?".
- **Conditions** — the summary cards (cloud/seeing/transparency/humidity/temp/wind medians with their
  ranges, the Moon's phase + highest altitude + *closest* approach to the target, target altitude and
  best airmass, sky brightness, dew risk, Kp) and an hourly chart. A toggle overlays the forecast made
  at the start against what was then measured. Times are shown in the **session's own** timezone,
  derived from the site stored on the row.
- **Mount tracking**, when the run was measured.

Sessions captured before this shipped show "not recorded" and cannot be backfilled — see
[architecture.md](architecture.md#capture-conditions-the-logbook) for why.

## AstroAgent (`/astroagent`)

A chat page over the local vision model with live tool access: ask about your data, runs, sky and
setup; attach an image for a measured critique; let it launch or tune runs — every mutating action
is **confirmation-gated**. Supervised/refine runs also surface here as steerable conversations.
See [agent.md](agent.md).

## In-app help

Every page carries a discreet **help** button beside its heading. It opens a guided tour of that
page: a carousel of real screenshots on the left, an explanation on the right, arrow keys or the
chevrons to step, Esc to close. It **never starts on its own** — it is there when someone wants it
and invisible otherwise.

The tour deliberately shows *pictures* of the page rather than spotlighting live elements. A tour
that highlights real controls can only describe the page as it currently is — nothing selected, a
panel collapsed, a list still loading, no job run yet — and the steps a newcomer most needs are
exactly the ones whose elements are not on screen.

- Registry: `frontend/src/constants/tour.ts`, keyed on the **route name**, holding only step keys.
- Copy: the `tour` namespace in `frontend/src/i18n/{en,fr}.json`. `tour.spec.ts` fails the build if
  a locale is missing a string, if a tour names a route that no longer exists, or if a new named
  route ships with no tour at all.
- Screenshots: `frontend/public/tour/<locale>/<page>-<step>.webp`, regenerated by **`just tour-shots`**
  against a running app (see `tools/demo/scenarios/tour-shots.yaml`). The focus highlight is baked
  into the image, so the modal never needs to know where a control is. A step with no shot yet
  degrades to its caption, so the set can be filled in gradually — but re-run the recipe when the UI
  changes, or the pictures quietly go stale.

## Conventions the UI follows

- All state that matters is server-side; the browser keeps only preferences/favorites (which the
  [backup](storage-s3.md#backup--restore) captures as `appstate`).
- Every long operation is a job with SSE progress; nothing blocks the page.
- File/preview URLs transparently fall back to S3 when a local file was freed.
