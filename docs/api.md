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
| `GET /api/sky/darksites` | Dark-site search (darkness + horizon + drive distance) |
| `GET /api/sky/canopy/atlas` (GET/POST) | Tree-canopy atlas status / build |
| `GET /api/sky/weather` (+ `/grid`, `/grid/frames`, `/tiles/{metric}/{time}/{z}/{x}/{y}`) | Astro weather panel + animated map layers |

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
