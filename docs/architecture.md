# Architecture

AstroStack drives **host-installed Siril and GIMP** to sort, grade, calibrate and stack
astrophotography captures. Because those are macOS app bundles that cannot run in a Linux container,
the engine runs on the host and only the support services are containerized.

```
┌─────────────────────────── HOST (macOS) ───────────────────────────┐
│  astrostack (Go)                          siril-mcp (Go, stdio)      │
│   • CLI: inspect / process / video         • MCP tools for Claude    │
│   • HTTP API + SSE progress  (chi)          (shares internal/ pkgs)  │
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

## Components

| Package | Responsibility |
|---------|----------------|
| `internal/config` | Environment configuration. |
| `internal/fits` | Read FITS headers + pixels (`codeberg.org/astrogo/fitsio`). |
| `internal/inspect` | Walk a directory, classify each file (light/dark/flat/bias/dark-flat/video), group into sets. |
| `internal/siril` | `SirilRunner`: generate `.ssf`, exec `siril-cli`, parse `progress:`/`log:` + `seqstat` CSV. |
| `internal/grade` | Per-frame quality metrics + rejection rules; trail handling. |
| `internal/calib` | Build master calibration frames; match the right masters to each light set; calibration library. |
| `internal/pipeline` | Orchestrate inspect → masters → calibrate → grade → register → stack → combine. |
| `internal/postprocess` | LRGB+Ha channel combine, color calibration, stretch; optional GIMP touch-ups. |
| `internal/planetary` | SER/AVI/MP4/MOV lucky-imaging path. |
| `internal/store` | Postgres access (`pgx/v5` + `sqlc`), models. |
| `internal/job` | In-process worker pool + job lifecycle. |
| `internal/api` | chi router, handlers, SSE progress. |
| `internal/report` | JSON + markdown run reports. |

## Why no Redis / Celery

Jobs are persisted in Postgres and executed by a Go in-process worker pool. Siril emits progress on
stdout, which the runner parses and republishes to the browser over Server-Sent Events. A single host
binary keeps the moving parts minimal.

## Deliberate deviation

Running the engine and Go tests on the host is an intentional exception to the house "everything in a
container" rule, forced by the host-Siril/host-GIMP dependency. It is documented in `CLAUDE.md`.
