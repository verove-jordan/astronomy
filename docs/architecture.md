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
| `internal/calib` | Build master calibration frames (+ the dark **defect map** / bad-pixel scan); match the right masters to each light set; calibration library + deep cross-session pools. |
| `internal/transient` | Cross-frame satellite/plane-trail + cosmic-ray masking on the registered subs, validated against fixed-pattern noise. |
| `internal/photom` | Photometric normalization across mixed-session groups (percentile-curve fit; currently off by default). |
| `internal/dither` | Pointing-pattern diagnosis from registration offsets (dithered / drift / static) — the walking-noise advisory. |
| `internal/noise` · `internal/imgops` · `internal/optics` | Noise measurement/starlet denoiser, shared image ops, flat-defect QC. |
| `internal/pipeline` | Orchestrate inspect → masters → calibrate → grade → register → stack → combine; soft-fail AI steps in `enhance.go`; palettes, supervisor, per-stage rerun. |
| `internal/preset` | The built-in "best params per situation" catalog (16 recipes) merged with user presets. |
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
| `internal/libmirror` | The calibration library's S3-mirror convention + on-demand puller seam. |
| `internal/job` | In-process worker pool (parallel / sequential / transfer lanes), job lifecycle incl. pause/resume + cancel semantics, per-input-dir locking. |
| `internal/agent` · `internal/turns` | AstroAgent chat loop (confirmation-gated tools) + the live conversation transport shared with supervised runs. |
| `internal/api` | HTTP handlers (Go 1.22 `http.ServeMux` method routing) + SSE progress; the `/api/sky/*` planner endpoints. |
| `astro` · `skycat` · `skyplan` | Ephemeris, sky-object catalog, and visibility/event scoring behind the **planner** (`/api/sky/*`). |
| `internal/report` | JSON + markdown run reports. |

## Colour palettes (deep-sky finish)

The deep-sky GIMP finish resolves a user-selectable **colour palette** — the channel→RGB mapping — in
`internal/pipeline/palette.go` (`resolvePalette`), consumed by `prepGimpInputs`:

| Palette | R | G | B | Notes |
|---|---|---|---|---|
| `natural` (default) | R | G | B | broadband LRGB + Hα screen + SPCC; `""` ≡ natural (byte-identical to the pre-palette pipeline) |
| `hargb` | R | G | B | natural with Hα mandatory |
| `hoo` | Hα | OIII | OIII | narrowband bicolour |
| `sho` (Hubble) | SII | Hα | OIII | narrowband |
| `hos` (CFHT) | Hα | OIII | SII | narrowband |
| `foraxx` (Webb-style) | Hα | √(Hα·OIII) | OIII | dynamic green via Siril pixel-math |
| `mono` | L → Hα → first | — | — | single-channel |

The narrowband palettes assign emission lines straight to R/G/B, so they **disable the Hα screen and
SPCC** and stretch unlinked; natural/hargb keep the SPCC ladder. A palette missing its required filters
falls back down a chain to one that resolves (ultimately mono) and records a `run.json` note — so a run
can request SHO today and simply render natural until OIII/SII data exists. The palette is a **Tier-B**
knob (`palette` in the supervisor/agent whitelist), so it can be switched post-run from the stage
timeline or Refine as a cheap re-finish (rebuild the combine from the persisted channel masters, no
re-stack). A pure **star cluster** (OpenNGC globular/open) additionally gets a gentler natural-colour
finish profile (`applyClusterProfile`): full-opacity L luminance, low saturation, star-core
desaturation + chroma blur, so a dense field reads as natural white-ish stars, not solid colour discs.

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
backed by a canvas — `frontend/src/composables/useFrameTileLayer.ts`, registered through the
modular layer registry `useMapLayers.ts`) with play/scrub over the timesteps. Weather is overlay +
panel only — it never changes visibility scores.

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

### Which mode per environment

| Environment | Command | Engine + Siril/GIMP | AI model (VLM) | Use it for |
|---|---|---|---|---|
| **macOS — daily dev** | `just up` + `just dev` + `just web` | **native on host** (fast) | native mlx: `just run-ia-model` | everything — the normal workflow |
| **macOS — full container** | `just stack` | container (**native arm64**) — Siril/GIMP run natively (Siril ~1.2.x from the distro) | native mlx on host | a full local stack; use amd64 or host-dev for exact 1.4.3 parity |
| **Linux + NVIDIA GPU** | `just stack-ai` + `just ai-pull` | container (**native amd64**) — full processing | container (Ollama, GPU) | **true 100 % dockerized**, incl. the VLM |
| **Linux — no GPU** | `just stack` | container (native amd64) — full processing | skip, or point at any OpenAI-compatible server | headless processing without a GPU |

### Ports

| Port | Service | Mode |
|---|---|---|
| `5432` | Postgres | all |
| `8080` | engine API | host-dev (`just dev`) **or** container (`just stack`) — one at a time |
| `8082` | frontend (nginx) | `stack` / `web` |
| `11434` | Ollama VLM | `ai` (Linux + GPU) |
| `1234` | native mlx VLM | macOS host (`just run-ia-model`) |
| `8081` | Adminer | `tools` |
| `5173` | Vite dev server | host-dev (`just web`) |

### Stack configuration (`.env`)

Beyond the host-dev variables, the containerized stack reads (host tool paths like `SIRIL_BIN` are
**baked into the engine image** and don't apply here):

| Variable | Default | Description |
|---|---|---|
| `API_UPSTREAM` | `host.docker.internal:8080` | nginx `/api` target; `just stack` sets it to `engine:8080`. |
| `ENGINE_PORT` | `8080` | Host port the containerized engine's API is published on. |
| `UID` / `GID` | *(unset → 10001)* | Linux: run the engine as the UID/GID owning the data dirs (`id -u`/`id -g`). |
| `ASTRO_LLM_URL` / `ASTRO_LLM_MODEL` | host mlx / — | VLM endpoint + model id (see the table above). |
| `OLLAMA_TAG` / `OLLAMA_PORT` | `0.6.8` / `11434` | Ollama image tag (verify one for your driver) + port. |

**Your existing data & runs keep working.** The engine stores **absolute** paths in Postgres +
`run.json`, so the stack mounts your `./input`, `./library`, `./output`, `./work` at their **same
absolute host paths** and runs with the repo root as CWD. Previous Library masters, Runs, Tasks and
cross-session reuse resolve unchanged, and you can switch between host-dev and the stack freely.
Keep captures under `./input` (or symlink them there); `input` is mounted read-only.

## S3 storage (import / process / results + sync + backup)

S3 is a first-class store alongside the local filesystem, so large captures/results can live in the
cloud and local disk stays free — without ever losing data. Credentials come from a UI-managed
connection (secret **AES-256-GCM encrypted at rest**, never returned to the UI) or the
`ASTRO_S3_*` env fallback; bucket + prefix are per-request UI state. The mirror convention under
`<prefix>` is `data/` (captures), `output/` (results), `library/` (calibration masters, pulled
back on demand) and `backup/<stamp>/` (snapshots: db dump, library tar, atlas, browser app-state).
Transfers, backups and restores are ordinary **jobs** on a dedicated worker lane; "free local"
**verifies every file on S3 before deleting anything**; freed previews/results serve local-first
with a transparent S3 fallback; a *full-S3* run pulls inputs, processes locally, pushes and frees
(low-disk mode stages one channel wave at a time).

Full detail — layout, connections, transfer semantics, staging, backup/restore:
**[docs/storage-s3.md](storage-s3.md)**.
