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
multi-session parity matters. **StarNet++** is not baked in (licence not redistributable) — mount it +
set `STARNET_BIN`; it soft-fails to full stars otherwise. The one thing that cannot run in a container on
macOS is the **VLM** (no GPU/Metal) — keep it native there.
