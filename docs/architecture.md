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
| `internal/llm` | Optional, opt-in: drives a host-run OpenAI-compatible vision model to auto-tune the finish (the supervisor loop; soft-fails when the server is down). |
| `internal/planetary` | SER/AVI/MP4/MOV lucky-imaging path. |
| `internal/livestack` + `internal/source` | Incremental live-stacking session + its watched source (local dir or S3 via `minio-go`). |
| `internal/nightscape` | Milky-Way / one-shot-color foreground+sky composite recipe. |
| `internal/store` | Postgres access via **raw `pgx/v5`** (no ORM/sqlc); schema applied from embedded, versioned `*.up.sql` migrations (`store.Migrate`, tracked in `schema_migrations`). |
| `internal/job` | In-process worker pool + job lifecycle. |
| `internal/api` | HTTP handlers (Go 1.22 `http.ServeMux` method routing) + SSE progress; the `/api/sky/*` planner endpoints. |
| `astro` · `skycat` · `skyplan` | Ephemeris, sky-object catalog, and visibility/event scoring behind the **planner** (`/api/sky/*`). |
| `internal/report` | JSON + markdown run reports. |

## Why no Redis / Celery

Jobs are persisted in Postgres and executed by a Go in-process worker pool. Siril emits progress on
stdout, which the runner parses and republishes to the browser over Server-Sent Events. A single host
binary keeps the moving parts minimal.

## Deliberate deviation

Running the engine and Go tests on the host is an intentional exception to the house "everything in a
container" rule, forced by the host-Siril/host-GIMP dependency (and the optional GraXpert/StarNet++
CLIs, which run the same way). It is documented in `CLAUDE.md`.
