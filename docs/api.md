# HTTP API reference

The engine serves a JSON API on `API_ADDR` (default `:8080`); the web UI is its only intended
client, but every endpoint is plain HTTP and works headless (`curl`). **There is no
authentication** — the API is designed for a trusted local network; do not expose it publicly.
Routes are registered in `internal/api/api.go`.

Conventions: request/response bodies are JSON; errors are `{"error": "..."}` with a 4xx/5xx
status; paths in query/body must live under the configured data/output roots (the server
rejects anything else); two endpoints stream Server-Sent Events (SSE).

## Health & environment

| Method + path | Purpose |
|---|---|
| `GET /api/health` | Engine status, build stamp, configured dirs |
| `GET /api/environment` | Tool availability (Siril/GIMP/GraXpert/StarNet/model), catalogue status |

## Inspection & presets

| Method + path | Purpose |
|---|---|
| `POST /api/inspect` | Classify a capture folder (frames, sets, warnings) |
| `GET /api/browse` | Browse the data dir (folders, capture hints) |
| `GET /api/mode-params` | Per-mode tunable parameter schema |
| `GET /api/presets` | Built-in + user presets |
| `POST /api/presets` · `PUT /api/presets/{id}` · `DELETE /api/presets/{id}` | Save / rename / delete a user preset |
| `GET /api/masters` · `GET /api/phone-masters` | Calibration library contents |
| `POST /api/library/s3-sync` | Push the library to the S3 mirror |
| `POST /api/reuse/preview` | What a cross-session reuse plan would fold in |
| `POST /api/calib/preview` | Which masters would match a light set |

## Jobs & series

| Method + path | Purpose |
|---|---|
| `POST /api/jobs` | Launch a processing/transfer/backup job |
| `GET /api/jobs` · `GET /api/jobs/{id}` | List / fetch (result embedded when finished) |
| `POST /api/jobs/{id}/cancel` · `/pause` · `/continue` · `/restart` | Lifecycle (pause/resume is checkpointed) |
| `POST /api/jobs/{id}/refine` | Supervised re-finish of a completed run (no re-stack) |
| `POST /api/jobs/{id}/rerun` | Re-enter from an edited stage (per-stage rerun) |
| `POST /api/jobs/{id}/denoise-final` | Extra denoise pass on the final image |
| `POST /api/jobs/{id}/free-local` | Verified free-local of the run's inputs (S3-checked deletes) |
| `GET /api/jobs/{id}/iterations` | Supervisor iterations of a run |
| `POST /api/jobs/{id}/stars` · `GET /api/jobs/{id}/stars` | Compute / fetch the run's star annotation (`stars.json`). Synchronous, cached, deliberately not a job |
| `GET /api/jobs/{id}/scene3d` | The 3D field map's manifest, built by the same annotation pass. `available: false` + `reason` when the run cannot have a scene; the star field and backdrop it points at are ordinary run files fetched through `GET /api/file` |
| `GET /api/galaxy/points` | The Milky Way point cloud the 3D map draws the Galaxy from — 180 000 stars sampled from published structure, 1.8 MB of `application/octet-stream`. Run-INDEPENDENT (only the rotation into a photograph's frame is per-run, and that is a GPU uniform), so it is generated once per process and served with an ETag and a week's `Cache-Control`. Optional `?v=` is the record layout the caller can decode: a mismatch answers **409** rather than bytes the viewer would silently reject |
| `GET /api/solarsystem/bodies` | The whole solar-system model the 3-D page animates from: every body's radius, mass, IAU pole and rotation rate, ring geometry and orbital elements, plus which surface maps this engine holds and the sources for all of it. Run-INDEPENDENT and served with an ETag; `must-revalidate` rather than a long `max-age`, because running the texture download changes the answer. Optional `?v=` answers **409** on a layout mismatch, like the galaxy cloud |
| `GET /api/solarsystem/state` | What is true at ONE instant, for one observing site: heliocentric and geocentric positions, RA/Dec, altitude/azimuth, magnitude, angular diameter, phase, elongation, orientation, axial tilt and Saturn's ring-plane tilt. `?t=` is a Unix millisecond timestamp and is **refused outside 1800–2050**, the span the orbital model is fitted for. The browser propagates the elements itself for the animation; this is what it quotes when a number is printed |
| `GET /api/solarsystem/texture` | One downloaded surface map (`?key=mars`). **404 is ordinary** — that body is shaded procedurally instead. Keys are bare words, which is the whole of the path confinement |
| `GET /api/jobs/{id}/events` | **SSE** — progress, log lines, previews, resources. Sends a snapshot first; for a finished job it closes immediately after the snapshot (clients should not stream terminal jobs) |
| `POST /api/series` · `GET /api/series` · `GET /api/series/{id}` | Goal-driven improvement campaigns |
| `POST /api/series/{id}/continue` · `/stop` | Resume / stop a campaign |

## Runs & files

| Method + path | Purpose |
|---|---|
| `GET /api/runs` | Completed runs (from `run.json` on disk) |
| `GET /api/processed` | Per-folder local/S3 presence used by the Storage view |
| `GET /api/file` | Serve an output file (local-first, S3 mirror fallback) |
| `GET /api/preview` | Downsampled linear preview buffer of a capture file |
| `GET /api/thumb` | Cached thumbnail |

## Sky & planner

| Method + path | Purpose |
|---|---|
| `GET /api/sky/targets` | Tonight's visibility-scored targets |
| `GET /api/sky/events` · `GET /api/sky/series` | Astronomical events calendar |
| `GET /api/sky/polar` | Polar-alignment reticle data |
| `GET /api/sky/align` · `GET /api/sky/align/profiles` | GoTo alignment star sequences |
| `GET /api/sky/geocode` | Place-name lookup |
| `GET /api/sky/lightpollution` (+ `/atlas` GET/POST, `/tiles/{z}/{x}/{y}`) | Sky brightness point/atlas/overlay tiles |
| `GET /api/sky/point` | Map hover readout: light pollution at one coordinate + weather **from cache only** (never fetches upstream) |
| `GET /api/sky/darksites` | Dark-site search for one night (darkness + horizon + forecast + drive distance); `night=`, `weather=0|1`, `weather_weight=` |
| `GET /api/sky/nights` | Upcoming observing nights (dark window, Moon) for the night picker |
| `GET /api/sky/canopy/atlas` (GET/POST) | Tree-canopy atlas status / build |
| `GET /api/sky/weather` (+ `/grid`, `/grid/frames`, `/tiles/{metric}/{time}/{z}/{x}/{y}`) | Astro weather panel + animated map layers |

## Capture & logbook

The sequencer lives in the engine (not the device server) because a session is a statement about a
target and a night. `/api/device/*` is a transparent reverse proxy onto the device server.

| Method + path | Purpose |
|---|---|
| `POST /api/capture/start` | Launch an auto-run (`sequence`, `path`, `object`, optional `ra_deg`/`dec_deg`, `lat_deg`/`lon_deg`, `measure_tracking`) |
| `POST /api/capture/pause` · `/resume` · `/abort` | Sequencer control (pause takes effect after the current exposure) |
| `GET /api/capture/status` · `GET /api/capture/events` | Current progress / the same progress as SSE |
| `POST /api/capture/center` | Plate-solve centring loop (expose → solve → sync → re-slew) |
| `POST /api/capture/polar/start` | Begin a camera polar alignment and take the first frame (`lat_deg`/`lon_deg`, `points`, `exposure_us`, `gain`, `no_refraction`) |
| `POST /api/capture/polar/rough` | Answer from ONE frame, assuming the tube looks down the RA axis (declination at its 90° index) — polar-scope accuracy, no rotation |
| `POST /api/capture/polar/next` | "I have turned the right-ascension axis" — take the next frame |
| `POST /api/capture/polar/adjust` · `/refresh` | Enter the live phase / take another frame while the bolts are being turned |
| `POST /api/capture/polar/stop` | End the session and clear its frames |
| `GET /api/capture/polar` · `GET /api/capture/polar/events` | Session snapshot / the same as SSE |
| `GET /api/capture/sessions` | The logbook, newest first. `limit`, `offset`, `object` (case-insensitive substring), `from`/`to` (epoch ms) → `{sessions, total}` |
| `GET /api/capture/sessions/{id}` | One session + every frame + the per-filter/type tallies (`frame_stats`) |
| `GET /api/capture/sessions/{id}/conditions` | The night's sky: hourly samples, the archived start/end forecast snapshots, and the rolled-up summary |
| `GET/POST /api/capture/sequences` · `DELETE /{id}` | Saved auto-run plans |
| `POST /api/capture/calibration/plan` | Derive a matched darks/flats/bias sequence from the lights |
| `GET/POST /api/capture/filters` | The wheel slot → filter-name map |
| `GET /api/tracking/report/{id}` · `GET /api/tracking/sessions` | Measured periodic error for a session / sessions that have tracking data |

## S3 storage

| Method + path | Purpose |
|---|---|
| `GET /api/s3/status` | Effective S3 config (never includes secrets) |
| `GET /api/s3/browse` · `POST /api/s3/import` | Browse a bucket / import a folder into the data dir |
| `POST /api/s3/transfer` | Enqueue upload / sync / download / remove-local (verified) |
| `GET/POST /api/s3/connections` (+ `PUT/DELETE /{id}`, `POST /{id}/default`, `POST /{id}/test`, `POST /test`) | UI-managed connections; secrets AES-256-GCM at rest, never returned |
| `GET/POST/DELETE /api/s3/manage/buckets` · `/objects` · `/folder` · `/object` · `/move` · `/download` · `/upload` | Plain bucket file-manager |
| `GET /api/local/drives` · `/browse` · `POST /api/local/upload` | Removable-drive browsing and copy-in |

## Backup

| Method + path | Purpose |
|---|---|
| `POST /api/backup` · `GET /api/backup` | Snapshot to `<prefix>/backup/<stamp>/` / list snapshots |
| `POST /api/backup/restore` | Restore a snapshot (**destructive**: `pg_restore --clean`) |
| `GET /api/backup/appstate` | Browser-state component (favorites/setups/AI chats), UI-assembled |

## AstroAgent

| Method + path | Purpose |
|---|---|
| `GET /api/agent/status` | Model reachability |
| `POST /api/agent/chat` | Start a chat turn (tools + optional image) |
| `GET /api/agent/turns/{id}/events` | **SSE** — streamed reasoning/tool steps and supervised-run passes |
| `POST /api/agent/turns/{id}/confirm` | Approve/deny a gated mutating action (e.g. Tier-C re-stack) |
| `POST /api/agent/turns/{id}/message` | Steer a live turn with free text |
