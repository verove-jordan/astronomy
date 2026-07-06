# Architecture

AstroStack drives **host-installed Siril and GIMP** to sort, grade, calibrate and stack
astrophotography captures. Because those are macOS app bundles that cannot run in a Linux container,
the engine runs on the host and only the support services are containerized.

```
┌─────────────────────────── HOST (macOS) ───────────────────────────┐
│  astrostack (Go)                          siril-mcp (Go, stdio)      │
│   • CLI: inspect / process / video         • MCP tools for Claude    │
│   • HTTP API + SSE progress  (stdlib)       (shares internal/ pkgs)  │
│   • in-process job worker pool                                       │
│        │ exec                 │ exec               │ exec            │
│        ▼                      ▼                    ▼                 │
│   siril-cli              gimp-console-2.10        ffmpeg             │
│   (Siril.app)           (via vendored GIMP MCP)  (video frames)      │
│        │ TCP localhost:5432                                          │
└────────┼────────────────────────────────────────────────────────────┘
         ▼
┌──────────────── docker compose ────────────────┐
│  db (Postgres, named volume, healthcheck)        │
│  frontend (Vue → nginx)            [profile web] │
│  adminer (DB UI)                  [profile tools] │
└──────────────────────────────────────────────────┘
```

On the same host and in the same way (`exec` + parse stdout), the engine also drives the **optional**
tools — **GraXpert** and **StarNet++** (AI background extraction / star removal) and, only when a run
opts in, a local OpenAI-compatible **vision-model server** (`:1234`, e.g. mlx-vlm via `just
run-ia-model`) that auto-tunes the finish. All three **soft-fail**: absent or unreachable, the run
falls back to the pure Siril/GIMP path. The browser also calls the engine's `/api/sky/*` **planner**
endpoints (tonight's targets, GoTo alignment, events, weather, light-pollution), which use only keyless
public data services by default.

## Components

| Package | Responsibility |
|---------|----------------|
| `internal/config` | Environment configuration. |
| `internal/fits` | Read FITS headers + pixels (`codeberg.org/astrogo/fitsio`). |
| `internal/inspect` | Walk a directory, classify each file (light/dark/flat/bias/dark-flat/video), group into sets. Bare-filename legacy captures are labeled from an `info.txt` sidecar (`manifest.go`) that lists the per-sub-run filter order + gain. |
| `internal/siril` | `SirilRunner`: generate `.ssf`, exec `siril-cli`, parse `progress:`/`log:` + `seqstat` CSV. |
| `internal/grade` | Per-frame quality metrics + rejection rules; trail handling. |
| `internal/calib` | Build master calibration frames; match the right masters to each light set; calibration library. |
| `internal/pipeline` | Orchestrate inspect → masters → calibrate → grade → register → stack → combine; soft-fail AI steps in `enhance.go`. |
| `internal/postprocess` | LRGB+Ha channel combine, color calibration, stretch; optional GIMP touch-ups. |
| `internal/graxpert` | Optional host CLI: GraXpert AI background-gradient extraction / denoise (`GRAXPERT_BIN`). |
| `internal/starnet` | Optional host CLI: StarNet++ v2 star removal for star-reduced finishing (`STARNET_BIN`). |
| `internal/llm` | Optional, opt-in: drives a host-run OpenAI-compatible vision model to auto-tune the finish for **every stacking mode** — deep-sky/nebula composite, comet colour composite, milkyway grade, planetary sharpen — via per-mode `candidateRenderer` adapters (`internal/pipeline/supervise_*.go`); the shared render→judge→re-tune loop soft-fails when the server is down. |
| `internal/planetary` | SER/AVI/MP4/MOV/stills lucky-imaging path: native-res disk-masked sharpness ranking, multi-point ZNCC alignment, AP-weighted sigma-clipped stack, RL deconvolution. |
| `internal/comet` | Pure comet primitives: multi-scale coma detection, robust linear/quadratic track fit, starless ZNCC alignment, sub-pixel translate (driven by `pipeline.ProcessComet`). |
| `internal/mode` | Capture modes (deepsky/nebula/milkyway/planetary/livestack/comet) → the `Preset` that retunes the whole pipeline. |
| `internal/livestack` + `internal/source` | Incremental live-stacking session + its watched source abstraction (local dir or S3 via `minio-go`). |
| `internal/nightscape` | Milky-Way / one-shot-color foreground+sky composite recipe. |
| `internal/rawconv` | Camera-raw → 16-bit TIFF develop for Siril ingestion: LibRaw `dcraw_emu` preferred (photometric, `-t 0`, exact sRGB), macOS `sips` fallback; also raw thumbnails. |
| `internal/weather` | Astronomy weather: Open-Meteo + 7Timer! + air quality + NOAA SWPC merged into per-site forecasts and the chunked multi-point cloud grid (soft-fail, disk-cached). |
| `internal/darksky` | Dark-site finder: grid an area for low light pollution, score horizon openness (terrain + canopy). |
| `internal/elevation` | Terrain elevation provider + horizon-profile sampling (keyless Open-Meteo elevation, cached). |
| `internal/s3store` | Small reusable minio-go v7 client (list/stat/upload/download/delete, byte progress). |
| `internal/transfer` | S3 sync engine: upload / sync / download / `removeLocal` (verify-then-delete — content-MD5/ETag where possible, abort on any unverified file). |
| `internal/s3conn` | Builds S3 clients from UI-managed connections (default connection → env resolution). |
| `internal/secret` | AES-256-GCM encryption for stored S3 secrets (`ASTRO_ENCRYPTION_KEY` or an auto-generated key file kept outside the backup roots). |
| `internal/backup` | Snapshot/restore of the precious non-bulk state: `pg_dump`, master library, light-pollution atlas, browser-side app state. |
| `internal/buildinfo` | Engine build identity injected via `-ldflags` (`Version`/`BuiltAt`) — stamped into `/api/health` and every `run.json`. |
| `internal/toolhealth` | Deep environment probes (Siril/GIMP/GraXpert/StarNet/raw developer/LLM + offline-catalogue presence) behind `/api/environment`. |
| `internal/store` | Postgres access via **raw `pgx/v5`** (no ORM/sqlc); schema applied from embedded, versioned `*.up.sql` migrations (`store.Migrate`, tracked in `schema_migrations`). |
| `internal/job` | In-process worker pool + job lifecycle. |
| `internal/api` | HTTP handlers (Go 1.22 `http.ServeMux` method routing) + SSE progress; the `/api/sky/*` planner endpoints. |
| `astro` · `skycat` · `skyplan` | Ephemeris, sky-object catalog, and visibility/event scoring behind the **planner** (`/api/sky/*`). |
| `internal/report` | JSON + markdown run reports. |

## Weather & sky overlays

The Tonight page's animated cloud map is served by `GET /api/sky/weather/grid`
(`internal/api/weather.go` → `weather.Provider.Grid` in `internal/weather/provider.go`): a
`GRID_SIZE × GRID_SIZE` (default 32×32, `ASTRO_WEATHER_GRID_SIZE`) lat/lon sample box around the
site, returned as one float frame per forecast hour per layer. The default `clouds` layer expands
to its **per-altitude bands** — `clouds_low` / `clouds_mid` / `clouds_high` — which the browser
composites into an intensity-true cloud raster. Because 1024 coordinates can't ride in one URL,
the Open-Meteo multi-point request is fetched in **chunked GETs** (a few in flight at once,
`internal/weather/openmeteo.go`); any failed chunk fails the fetch and the **stale-cache
soft-fail** takes over (last cached frames + a warning). The disk cache is **versioned**
(`gridCacheVersion` in `internal/weather/provider.go`, part of the cache key) so a semantic or
geometry change ignores stale cubes instead of mis-rendering them. On the client, the coarse grid
is bicubically interpolated onto a viewport-sized **canvas overlay** (a Leaflet `imageOverlay`
backed by a canvas, `frontend/src/composables/useWeatherGridLayer.ts`) with play/scrub over the
timesteps. Weather is overlay + panel only — it never changes visibility scores.

## Run provenance & environment health

Two mechanisms make "which code produced this image, and could the tools actually run?" answerable
at a glance:

- **Build provenance** (`internal/buildinfo`): `Version`/`BuiltAt` are injected at build time via
  `-ldflags` (git describe + timestamp; un-stamped `go run`/test binaries read "dev"). The identity
  is exposed at **`/api/health`** (`engine.version` / `engine.built_at`) and stamped into **every
  `run.json`** (`Result.Engine`) — so a result produced by a stale Docker engine is identifiable
  instead of masquerading as current code. Rebuild the container engine with `just stack-build`
  after pulling changes.
- **Environment health** (`internal/toolhealth`, served at **`/api/environment`**): *deep* probes,
  not mere binary lookups — Siril version, GIMP binary, StarNet binary, the raw developer kind
  (`dcraw_emu` vs the `sips` tone-curve fallback), the LLM server, the offline plate-solve
  catalogue situation (local Gaia astrometric file + xp_sampled chunk count → the effective
  `-catalog` value), and the **GraXpert deep probe** — a real tiny extraction run in the
  background, so a present-but-broken install (typically a missing ONNX runtime;
  fix: `pipx inject graxpert onnxruntime`) reads as broken instead of silently degrading
  gradients. The report is cached ~5 minutes and each problem carries a human-readable,
  run-impacting warning the UI can show before a run.

## Why no Redis / Celery

Jobs are persisted in Postgres and executed by a Go in-process worker pool. Siril emits progress on
stdout, which the runner parses and republishes to the browser over Server-Sent Events. A single host
binary keeps the moving parts minimal.

## Deliberate deviation

Running the engine and Go tests on the host is an intentional exception to the house "everything in a
container" rule, forced by the host-Siril/host-GIMP dependency (and the optional GraXpert/StarNet++
CLIs, which run the same way). It is the fastest path for daily macOS dev and is documented in
`CLAUDE.md`.

## Fully containerized mode (`stack`)

For portability and Linux-server deploys, the same code also runs **entirely in Docker** — because the
tool paths are all env vars, no Go changes are needed, only Linux builds of the tools baked into an
`engine` image. One `compose.yaml` serves both modes via profiles:

- `just stack` → `db` + **`engine`** (Go `serve` + Linux **Siril 1.4.x AppImage / GIMP 2.10 /
  GraXpert / ffmpeg** baked in, `docker/engine.Dockerfile`) + `frontend`. The engine reaches Postgres
  at `db:5432` and self-migrates. The engine persists **absolute** filesystem paths (in Postgres +
  `run.json`), so the stack bind-mounts `input/` (read-only), `library/`, `output/` and `work/` at their
  **same absolute host paths** and runs with the repo root as CWD (`working_dir: ${PWD}`) — pre-existing
  Library/Runs/Tasks/reuse rows resolve unchanged and host-dev ⇄ stack stay interchangeable. nginx
  templates its `/api` upstream (`API_UPSTREAM=engine:8080`).
- The finish-supervisor **VLM is decoupled and opt-in** — `stack` never pulls it. On **Linux+GPU** the
  `ai` profile (Ollama, `nvidia-container-toolkit`) serves it in-container (`ASTRO_LLM_URL=http://ai:11434/v1`);
  on **macOS** Docker has no Metal, so the model stays native (`just run-ia-model`) and the container
  points back at the host. The engine talks a stable OpenAI-compatible contract either way.

Trade-offs: the engine image builds for the **host architecture** (arm64 on Apple Silicon, amd64 on
Linux), so Siril/GIMP run **natively — no emulation**. Siril has no arm64 build, so the Dockerfile
branches on `TARGETARCH`: amd64 gets the pinned **1.4.3 x86_64 AppImage** (extracted from its squashfs
without executing the AppImage runtime), arm64 gets the **distro package (~1.2.x)**. The WCS/parity logic
in `reuse_process.go` assumes 1.4.3, so prefer a native amd64 host (or host-dev on macOS) when exact
multi-session parity matters. That distro Siril also ships its deep-sky **object catalogue in a legacy
semicolon `.txt` format** (RA in hours, split N/S sign column) the engine's CSV parser can't read, so the
tonight planner and the name→coord resolver fall back to a **catalogue snapshot compiled into the engine
binary** (`internal/skycat/catalogue/*.csv` via `go:embed`; `skycat.Load` prefers the on-disk Siril
catalogue and drops to the embed only when none is readable). Suggested targets therefore work on every
arch regardless of the installed Siril, while the on-disk catalogue is still used wherever it *is* readable
(the macOS host + the amd64 AppImage, whose CSVs live in the `catalogue/` subdir `ASTRO_SIRIL_CATALOG_DIR`
points at). **StarNet++** is not baked in (licence not redistributable) — mount it +
set `STARNET_BIN`; it soft-fails to full stars otherwise. The one thing that cannot run in a container on
macOS is the **VLM** (no GPU/Metal) — keep it native there.

## S3 storage (import / process / results + sync + backup)

S3 is a first-class store alongside the local filesystem, so large captures/results can live in the cloud
and local disk stays free — without ever losing data. **Credentials** come from a **UI-managed connection**
or the host environment (`ASTRO_S3_*`); the **bucket + prefix** are chosen per-request in the UI. The mirror
convention under `<prefix>` is `data/<relToDataDir>` (captures), `output/<relToOutputDir>` (results) and
`backup/<stamp>/` (snapshots).

- **Connections (`internal/secret` + `internal/store/s3conn.go` + `internal/s3conn`)** — the UI (Processing →
  **Storage**, `views/StorageView.vue`) connects to any S3-compatible store by entering endpoint + access
  key + secret. Connections persist in Postgres (`s3_connections`) with the **secret access key AES-256-GCM
  encrypted at rest** — the master key is `ASTRO_ENCRYPTION_KEY` or an auto-generated key file kept *outside*
  the backup roots. The secret is decrypted only to build a client and **never returned to the UI**
  (`SecretEnc` is `json:"-"`). One connection is the **default**, and the pipeline's S3 config resolves
  *default connection → env* (`s.s3Config(ctx)` / `m.s3ConfigResolved(ctx)`), so a UI connection transparently
  drives import/process/results/backup. The same view is a full **object manager** (browse buckets/objects,
  upload, download, delete, create folder/bucket — `internal/s3store/manage.go`).
- **`internal/s3store`** — a small reusable minio-go v7 client (list/stat/upload/download with byte
  progress/delete/put+get bytes). Deals in raw `(bucket, key, localPath)`; the mirror mapping lives in the
  callers.
- **`internal/transfer`** — the sync engine: `upload` / `sync` (upload only what's missing, by rel-key +
  size) / `download` / `removeLocal` (**verifies each file is present + same-size on S3, then deletes
  local — aborting the whole folder if any file is unverified**, so "free local" never loses data).
- **Transfers, backups and restores are modeled as jobs** (`RunRequest.Transfer/Backup/Restore`,
  intercepted in `Manager.execute` before mode parsing) so they inherit the SSE progress / Cancel /
  persistence stack and a dedicated worker lane (`xferQueue`, never starving pipeline runs). `Event` gains
  live-only `bytes_done/bytes_total`.
- **Local-first, S3-fallback serving** (`internal/api` `ensureServable`, `s3OutputRuns`,
  `remoteDataDirExists`) — after "Free local", previews/results/thumbnails and the Runs gallery transparently
  download-on-demand from the mirror into a regenerable cache. The frontend tags every file/preview/thumb
  URL and the runs/processed lists with the active bucket/prefix (`services/api.ts` `s3Suffix`/`withS3`), so
  previews work everywhere with zero per-component changes.
- **Process modes** (`StorageMode`) — *full-local* (default, keep) or *full-S3*: `run()` pulls the capture
  folders from S3 → runs the pipeline unchanged (the engine stays local-only: Siril/GIMP write absolute
  paths) → backs up inputs+outputs → frees local (verified). A failed push fails the job (nothing freed); a
  partial free is non-fatal (data is safe on S3).
- **`internal/backup`** — snapshot the precious state that isn't a big file: Postgres (`pg_dump -Fc` →
  `pg_restore --clean` on restore), the calibration-master library (tar), the light-pollution atlas, and the
  **browser-only app state** (favorites/setups/prefs + AI-chat IndexedDB) — the latter can only be gathered
  UI-side (`frontend/src/utils/appstate.ts`), posted as `appstate.json`, and re-imported on restore.
  Components soft-fail individually; a `manifest.json` records what was stored + the roots. Secrets in
  `.env` are excluded by default. Captures/results are backed up via the per-folder sync instead.
