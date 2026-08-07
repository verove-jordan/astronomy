# Configuration

Everything is configured through environment variables, read once at engine start
(`internal/config/config.go`). The canonical, fully commented list lives in
[`.env.example`](../.env.example) — copy it to `.env` and edit; `just` loads `.env` automatically
(`set dotenv-load`), and Docker Compose reads the same file. **Never commit a real `.env` or any
secret.**

Precedence notes:

- **S3 credentials**: a *default connection* saved in the UI (Processing → Storage) wins over the
  `ASTRO_S3_*` env vars; the env vars are the fallback for headless/scripted use. Bucket + prefix
  are always per-request UI state. See [storage-s3.md](storage-s3.md).
- **Container overrides**: under `just stack`, compose points the engine at the repo's
  `input/ output/ library/ work/` dirs (mounted at identical absolute paths) and at
  `host.docker.internal` for the host-served AI model — see the [compose matrix](#container-stack-overrides).

## Core

| Variable | Default | Description |
|---|---|---|
| `DATABASE_URL` | `postgres://astro:astro@localhost:5432/astrostack?sslmode=disable` | Postgres DSN |
| `API_ADDR` | `:8080` | HTTP API listen address |
| `LOG_LEVEL` | `info` | Log level |
| `ASTRO_DATA_DIR` | `./data` | Root the UI browses for capture folders |
| `ASTRO_WORK_DIR` | `./work` | Scratch for intermediate FITS/sequences (also serve-cache, atlases) |
| `ASTRO_OUTPUT_DIR` | `./output` | Final stacks + run reports |
| `ASTRO_LIBRARY_DIR` | `./library` | Persistent master-calibration library (+ `catalogues/`) |
| `ASTRO_BROWSE_ROOTS` | — | Extra absolute roots the UI may browse (`:` or `,` separated), on top of removable-media defaults |
| `PREVIEW_MAX_EDGE` | `1500` | Longest edge (px) of the in-browser preview buffer |

## Host tools

The engine drives host-installed binaries (see the [host-engine exception](architecture.md#deliberate-deviation)).

| Variable | Default | Description |
|---|---|---|
| `SIRIL_BIN` | `/Applications/Siril.app/Contents/MacOS/siril-cli` | Siril CLI (≥ 1.4 required) |
| `GIMP_BIN` | `/Applications/GIMP.app/Contents/MacOS/gimp-console-2.10` | GIMP console |
| `GIMP_HOST` / `GIMP_PORT` | `127.0.0.1` / `10008` | GIMP Script-Fu server |
| `FFMPEG_BIN` | `ffmpeg` | ffmpeg (video modes, demo recorder) |
| `GRAXPERT_BIN` | `graxpert` | GraXpert — optional AI background extraction + denoise; soft-fails when absent |
| `ASTRO_GRAXPERT_URL` | — | Optional host GraXpert HTTP service (`cmd/graxpert-host`) so a containerized engine can offload to the host GPU; empty → exec `GRAXPERT_BIN` locally |
| `ASTRO_GRAXPERT_GPU` | `false` | Pass `-gpu true` to GraXpert background extraction (Apple Silicon) |
| `ASTRO_GRAXPERT_BATCH` | `0` | GraXpert denoise batch size (0 → GraXpert default) |
| `STARNET_BIN` | `starnet++` | StarNet++ v2 — optional star removal; soft-fails to full stars |

## AI finish supervisor (opt-in)

See [agent.md](agent.md). The model server is never started implicitly.

| Variable | Default | Description |
|---|---|---|
| `ASTRO_LLM_URL` | `http://127.0.0.1:1234/v1` | OpenAI-compatible base URL |
| `ASTRO_LLM_MODEL` | — | Chat/vision model id (e.g. `mlx-community/Qwen2.5-VL-32B-Instruct-6bit`) |
| `ASTRO_LLM_IMAGE_FORMAT` | `openai` | Vision wire format: `openai` \| `mlxvlm` |
| `ASTRO_LLM_TIMEOUT_SEC` | `3600` | Max wall-clock per completion (0 = unlimited) |
| `ASTRO_LLM_ASSIST_PROMPT_EXTRA` | — | Extra text appended to the AstroAgent chat system prompt |
| `ASTRO_SUPERVISE_HISTORY` | — | `off` disables the supervisor's cross-run warm-start memory |

## Resource limits

| Variable | Default | Description |
|---|---|---|
| `ASTRO_MAX_CPUS` | `10` | Cap Siril processing threads (`setcpu`) |
| `ASTRO_SIRIL_MEM_RATIO` | `0.5` | Fraction of RAM Siril may use (`setmem`) |
| `ASTRO_SIRIL_NICE` | `10` | OS niceness added to siril-cli |
| `ASTRO_MAX_WORKERS` | `0` | Job worker-pool size (0 → half the CPU count) |

## Plate-solving + SPCC

| Variable | Default | Description |
|---|---|---|
| `ASTRO_FOCAL_MM` | `740` | Focal length (mm) |
| `ASTRO_PIXEL_UM` | `3.8` | Pixel size (µm) |
| `ASTRO_SPCC_SENSOR` | `ZWO ASI1600MM` | SPCC mono sensor name (must match Siril's database) |
| `ASTRO_SPCC_RFILTER` / `_GFILTER` / `_BFILTER` | `ZWO Optimized for CMOS <Red/Green/Blue>` | SPCC filter names |
| `ASTRO_SPCC_WHITEREF` | `Average Spiral Galaxy` | SPCC white reference |
| `ASTRO_NIGHTSCAPE_OSC_SENSOR` | — | SPCC OSC sensor for milkyway mode; empty disables SPCC there |
| `ASTRO_PLATESOLVE_CATALOG` | — | Force a solve catalog; empty → Siril auto (offline `localgaia` preferred when installed) |
| `ASTRO_SIRIL_CATALOG_DIR` | derived from `SIRIL_BIN` | Siril object catalogues |
| `ASTRO_GAIA_ASTRO_CAT` | `<library>/catalogues/siril_cat_healpix8_astro.dat` | Offline Gaia astrometric extract (`just download-catalogues`) |
| `ASTRO_GAIA_XPSAMP_DIR` | `<library>/catalogues` | Offline SPCC xp_sampled chunks (`just download-catalogues-spcc`) |
| `ASTRO_LOCAL_ASNET` | `false` | Solve via a local astrometry.net instead |
| `ASTRO_SPCC_CATALOG` | — | `gaia` \| `localgaia`; empty prefers local when installed |
| `ASTRO_DEEPSTAR_CAT` | `<library>/catalogues/athyg_v32.bin` | Deep star-name catalogue (`just download-deepstars`); absent → the embedded mag-9 extract |

## Observing site + rig (planner)

| Variable | Default | Description |
|---|---|---|
| `ASTRO_LAT` / `ASTRO_LON` / `ASTRO_ELEVATION_M` | `48.8566` / `2.3522` / `0` | Observer location |
| `ASTRO_TIMEZONE` | `Europe/Paris` | IANA timezone |
| `ASTRO_APERTURE_MM` | `100` | Telescope aperture |
| `ASTRO_SENSOR_W` / `ASTRO_SENSOR_H` | `4656` / `3520` | Sensor size (px) |
| `ASTRO_EYEPIECES` | `30:68:30mm,…` | Eyepiece kit `focalMM:aFOV[:label]` |
| `ASTRO_BARLOW` | `1` | Barlow factor |
| `ASTRO_REDUCER` | `1` | Focal reducer (e.g. `0.66`); multiplies on top of the Barlow |

The planner's data-provider variables (weather, light pollution, canopy, dark-sky scoring,
routing, elevation) are documented in [planner.md](planner.md) and, exhaustively commented, in
`.env.example`; they all soft-fail to sensible defaults when unset or offline. What each default URL
actually points at, and under what licence, is in [third-party.md](third-party.md).

## Cross-session reuse

| Variable | Default | Description |
|---|---|---|
| `ASTRO_REUSE_ENABLED` | `true` | Fold prior sessions of the same target into a run |
| `ASTRO_REUSE_CONE_DEG` | `0.5` | Same-target coordinate match radius (degrees) |
| `ASTRO_REUSE_DARK_RECENCY_DAYS` | `0` | Max dark age for the deep pool (0 = unbounded) |
| `ASTRO_REUSE_TEMP_TOL_C` | `5.0` | Dark temperature tolerance (°C) |

## Live stacking

| Variable | Default | Description |
|---|---|---|
| `ASTRO_LIVESTACK_POLL_SEC` | `3` | Source poll interval |
| `ASTRO_LIVESTACK_STABILITY_SEC` | `2` | File must be size-stable this long before ingest |
| `ASTRO_LIVESTACK_RESTACK_EVERY` | `1` | Re-stack after every N new lights |
| `ASTRO_LIVESTACK_MIN_INTERVAL_SEC` | `0` | Minimum seconds between re-stacks |

## S3 (env fallback) + secrets

A UI-saved default connection overrides these; see [storage-s3.md](storage-s3.md).

| Variable | Default | Description |
|---|---|---|
| `ASTRO_S3_ENDPOINT` | — | Empty → AWS S3; else any S3-compatible endpoint (MinIO, Scaleway, …) |
| `ASTRO_S3_REGION` | `us-east-1` | Region |
| `ASTRO_S3_ACCESS_KEY_ID` / `ASTRO_S3_SECRET_ACCESS_KEY` | — | Credentials (env fallback only) |
| `ASTRO_S3_USE_SSL` | `true` | TLS |
| `ASTRO_S3_CONCURRENCY` | `0` | Parallel per-file transfers (0 → 6) |
| `ASTRO_S3_LOW_DISK` | `true` | Full-S3 runs stage one channel wave at a time (peak disk ≈ one channel) |
| `ASTRO_ENCRYPTION_KEY` | — | Base64 32-byte master key sealing UI-saved S3 secrets (AES-256-GCM) |
| `ASTRO_SECRET_KEY_FILE` | user config dir | Auto-generated key file used when no master key is set; kept **outside** the backup roots |

## Container (`stack`) overrides

Compose-only variables (read by `compose.yaml`, not by the engine): `API_UPSTREAM` (nginx `/api`
upstream; `just stack` sets `engine:8080`), `ENGINE_PORT` (8080), `WEB_PORT_PROD` (8082),
`WEB_PORT` (5173, Vite dev), `VITE_API_BASE`, `ADMINER_PORT` (8081), `POSTGRES_*`,
`OLLAMA_TAG`/`OLLAMA_PORT` (the optional `ai` profile), `UID`/`GID` (engine container user),
`ASTRO_DRIVES_DIR` (host removable-media root, default `/Volumes`). Under `stack`, the engine's
`ASTRO_DATA_DIR` points at `./input` and the LLM URL at `http://host.docker.internal:1234/v1`.

Host-orchestration variables used by scripts/recipes rather than the engine: `ASTRO_LLM_PORT`
(model server port for `just run-ia-model`), `HF_TOKEN` (gated model downloads),
`ASTRO_GRAXPERT_PORT` (host GraXpert service), and the `ASTRO_LIGHTPOLLUTION_*_URL` /
`ASTRO_CANOPY_*` atlas-refresh inputs consumed by `scripts/update-*.sh`.
