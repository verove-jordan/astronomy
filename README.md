# AstroStack

Point it at a folder of astrophotography captures and it automatically sorts, grades, calibrates,
and stacks them into the best possible final image — for deep-sky LRGB/Ha imaging and lunar/planetary
video. Built for a Takahashi FC-100 DF + ZWO ASI 1600MM Pro mono rig.

## What it does

AstroStack inspects a capture directory, figures out **what's in it** (light frames per filter
L/R/G/B/Ha, plus darks, bias/offsets, flats, dark-flats — any of which may be missing), reads each
frame's FITS metadata, **discards the bad sub-frames** (elongated stars from tracking/wind/focus,
satellite/plane trails, clouds), automatically picks and applies the **right master calibration
frames**, then registers and stacks per channel and combines them (LRGB + Ha) into a finished image.
It drives **Siril** for the heavy lifting and **GIMP** for optional final touch-ups, exposes a
**Siril MCP** so Claude can run the pipeline conversationally, and ships a **web UI** to review frame
grades and results. Lunar/planetary **video** (SER/AVI/MP4/MOV) is processed with a lucky-imaging path.

Siril and GIMP are host-installed macOS apps, so the Go engine runs on the host and drives them
directly; Docker Compose provides Postgres. See [docs/architecture.md](docs/architecture.md).

## Quickstart

```bash
cp .env.example .env
just setup            # Go deps, dev tools (air/migrate/sqlc/golangci-lint), Siril MCP binary
just up               # start Postgres (Docker)
just migrate          # create the schema

# one-shot, fully automatic:  just process <mode> <format> <path>
just process deepsky  image ~/Astro/M31          # mono LRGB+Ha → layered .xcf + tif/png
just process nebula   image ~/Astro/NGC7000      # Ha-forward, faint nebulosity
just process milkyway image ~/Photos/MilkyWay    # iPhone ProRAW/HEIC (one-shot-color)
just process planetary video ~/Astro/moon.mp4    # lucky imaging
just process deepsky  both  ~/Astro/M31          # also render a Ken-Burns .mp4

# or use the web UI:
just dev              # API on http://localhost:8080  (host; drives Siril/GIMP)
just web              # UI  on http://localhost:5173
```

Open <http://localhost:5173>, point it at a capture folder, and launch a run.

## Prerequisites

Only a few things on the host (macOS):

| Tool | Install | Why |
|------|---------|-----|
| Docker + Compose | Docker Desktop | runs Postgres |
| `just` | `brew install just` | task runner |
| Go 1.23+ | `brew install go` | the engine, CLI, Siril MCP |
| Node 20+ / pnpm | `brew install node pnpm` | the web UI |
| Siril | `brew install --cask siril` | calibration / registration / stacking |
| GIMP | `brew install --cask gimp` | optional post-processing (via the GIMP MCP) |
| ffmpeg | `brew install ffmpeg` | MP4/MOV video frame extraction |
| OpenCV *(optional)* | `brew install opencv` | only for the optional GoCV trail detector |

## Usage

| Recipe | What it does |
|--------|--------------|
| `just setup` | First-run setup (deps, dev tools, MCP binary). Idempotent. |
| `just up` / `just down` | Start / stop Postgres. |
| `just migrate` | Apply DB migrations. |
| `just inspect DIR` | Print the classified inventory of a capture folder (no processing). |
| `just process MODE FORMAT PATH` | Full auto pipeline. MODE: `deepsky`·`nebula`·`milkyway`·`planetary`; FORMAT: `image`·`video`·`both`. Input (FITS dir / iPhone-raw dir / video file) is auto-detected. |
| `just video FILE` | Shortcut for `process planetary video` (lucky imaging). |
| `just dev` | Run the API server on the host with hot reload. |
| `just web` | Run the Vue web UI dev server. |
| `just mcp-siril` | Run the Siril MCP in the foreground (manual testing). |
| `just tools` | Start Adminer (DB UI) at `http://localhost:8081`. |
| `just check` | Lint + test (the pre-push gate). |
| `just clean` | Remove containers, volumes, and build artifacts (destructive; confirms). |

`just process` accepts pass-through flags after the path, e.g. `just process deepsky image ~/Astro/M31 -v --out ~/done`.

### Modes

Each mode retunes grading, alignment, background extraction, stretch, Ha blend, saturation and curves:

| Mode | Input | Pipeline |
|------|-------|----------|
| `deepsky` | mono FITS (L/R/G/B/Ha) | calibrate → grade → stack per channel → **co-register channels** → GIMP LRGB+Ha layered composite + gentle curves |
| `nebula` | mono FITS | like deepsky but lenient grading + stronger background extraction + Ha-forward blend for faint emission |
| `milkyway` | one-shot-color (iPhone ProRAW/HEIC, jpg/png/tif) | debayer → register → grade → stack → GIMP curves (gradient removal, natural star colors) |
| `planetary` | video (SER/AVI/MP4/MOV) | lucky imaging: sharpness-rank frames → stack best % → sharpen |

The finishing stage is driven automatically in **GIMP** (the engine talks to the same resident
Script-Fu server the GIMP MCP uses), producing an editable layered `.xcf` plus flattened TIFF/PNG.
`format video`/`both` also renders a Ken-Burns `.mp4`.

## Configuration

Configured via `.env` (copy from `.env.example`; the real `.env` is git-ignored — never commit
secrets).

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_URL` | `postgres://astro:astro@localhost:5432/astrostack?sslmode=disable` | Postgres DSN used by the host engine. |
| `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` | `astro` / `astro` / `astrostack` | Compose Postgres credentials. |
| `POSTGRES_PORT` | `5432` | Host port Postgres is published on. |
| `API_ADDR` | `:8080` | Address the API server listens on. |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error`. |
| `ASTRO_DATA_DIR` | `./data` | Root the web UI may browse for capture folders. |
| `ASTRO_WORK_DIR` | `./work` | Scratch space for intermediate FITS/sequences. |
| `ASTRO_OUTPUT_DIR` | `./output` | Where final stacks and reports are written. |
| `SIRIL_BIN` | `/Applications/Siril.app/Contents/MacOS/siril-cli` | Path to headless Siril. |
| `GIMP_BIN` | `/Applications/GIMP.app/Contents/MacOS/gimp-console-2.10` | Path to headless GIMP. |
| `FFMPEG_BIN` | `ffmpeg` | ffmpeg binary. |
| `VITE_API_BASE` | `http://localhost:8080` | API base URL used by the frontend. |

## Architecture

The engine runs on the host (it brokers host-installed Siril/GIMP — the same reason the GIMP MCP runs
on the host); Postgres runs in Compose. No Redis: jobs run in an in-process worker pool with state in
Postgres and live progress over SSE.

- **`cmd/astrostack`** — CLI (`inspect`/`process`/`video`) + HTTP API server (`serve`).
- **`cmd/siril-mcp`** — Siril MCP server (stdio) for Claude; shares the `internal/` engine.
- **`internal/`** — `fits`, `inspect`, `siril`, `grade`, `calib`, `pipeline`, `planetary`,
  `postprocess`, `store`, `api`, `job`, `report`, `config`.
- **`mcp-servers/gimp/`** — vendored GIMP MCP (Python; GIMP's own tooling).
- **`frontend/`** — Vue 3 + Vite + Pinia + Tailwind + vue-i18n + ECharts.

See [docs/architecture.md](docs/architecture.md) and [docs/pipeline.md](docs/pipeline.md) for depth.

## Development

- `just check` runs `go vet` + `golangci-lint` + `vue-tsc` and the test suites.
- House coding conventions live in [`./conventions/`](conventions/); project-specific rules are in
  [`CLAUDE.md`](CLAUDE.md).
- Go tests run on the host (they exercise host `siril-cli`); start Postgres first with `just up`.
- MCP servers: `siril` (Go) and `gimp` (Python) are registered in `.mcp.json` for Claude Code.
  `.mcp.json` runs `./bin/siril-mcp`, so build it first with `just build-mcp` (included in `just setup`).

## Deployment

This is a single-user, host-side tool: Compose provides Postgres (and optionally a built frontend
image under the `web` profile / Adminer under `tools`), while the Go engine runs on the host so it can
reach Siril and GIMP. There is no remote deployment target.

## License

MIT
