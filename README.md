# AstroStack

**English** · [Français](README.fr.md)

> Point it at a folder of astrophotography captures and it auto-sorts, grades, calibrates, stacks,
> and finishes them into the best possible final image — and plans your next session while you're
> at it.

AstroStack inspects a capture directory, figures out **what's in it** (lights per filter, darks,
bias/offsets, flats), **discards the bad sub-frames** (elongated stars, trails, clouds), picks and
applies the **right master calibration** (a cross-session library with per-pixel defect maps),
then registers and stacks per channel with count-adaptive rejection and combines them into a
finished image. It drives **Siril** for the heavy lifting and **GIMP** for the finish, with
optional AI enhancers (**GraXpert**, **StarNet++**) and an opt-in **local vision-model supervisor**
that critiques and re-tunes the finish. A built-in **session planner** (tonight's targets, GoTo
alignment, events calendar, weather + light-pollution overlays) rounds out the workflow. Built for
a Takahashi FC-100 DF + ZWO ASI 1600MM Pro mono rig, but the rig is configurable.

Siril and GIMP are host-installed macOS apps, so for daily macOS dev the Go engine **runs on the
host** and drives them directly; Docker Compose provides Postgres. This is a deliberate exception
to the usual "everything-in-a-container" rule. A fully **containerized** mode also ships
(`just stack`) for portable / Linux-server deploys. See
[docs/architecture.md](docs/architecture.md).

## Quickstart

```bash
git clone <repo-url> && cd astronomy
cp .env.example .env          # adjust host tool paths / secrets if needed
just setup                    # Go deps, dev tools, Siril MCP binary, frontend deps (idempotent)
just up && just migrate       # Postgres (Docker) + schema

# A) one-shot from the CLI:
just process deepsky image ~/Astro/M31           # mono LRGB+Ha → layered .xcf + tif/png
just process planetary video ~/Astro/moon.mp4    # lucky imaging

# B) or the web UI (two terminals):
just dev                      # API on http://localhost:8080  (host; drives Siril/GIMP)
just web                      # UI  on http://localhost:5173
```

Open <http://localhost:5173> → **Processing → Import**, point it at a capture folder, launch a run.

**Everything in Docker** (portable / server — no host toolchain; the engine image bakes the Linux
Siril/GIMP/GraXpert):

```bash
cp .env.example .env
just stack                    # db + engine + frontend → UI :8082, API :8080
```

The finish supervisor's ~28 GB vision model is **opt-in and decoupled** — `just stack` never
downloads it. Add it with `just run-ia-model` (macOS, native Metal) or `just stack-ai` +
`just ai-pull` (Linux + NVIDIA GPU). Per-environment matrix, ports and stack variables:
[docs/architecture.md → Fully containerized mode](docs/architecture.md#fully-containerized-mode-stack).

## Prerequisites

Daily macOS development drives host-installed tools (the documented
[host-engine exception](docs/architecture.md#deliberate-deviation)); only Postgres is in Docker.
For the all-container path you need just Docker + `just`.

- **Required**: macOS (Apple Silicon recommended) · Docker · [`just`](https://github.com/casey/just)
  · Go 1.23+ · Node 20+/pnpm · **Siril** (`brew install --cask siril`) · ffmpeg
- **Recommended**: GIMP (the LRGB+Ha finish; absent → Siril `rgbcomp` fallback) · Python 3.12
  (Siril's plate-solve/SPCC scripting)
- **Optional**: [GraXpert](https://www.graxpert.com) (AI background/denoise) ·
  [StarNet++ v2](https://www.starnetastro.com) (star reduction) · a local vision model
  (`just run-ia-model`) for the [finish supervisor](docs/agent.md)

The optional tools are **soft-fail** (missing → warning + fallback; disable with `--no-ai`) and
are *invoked, never bundled* — their licences stay with your install. For **offline
plate-solving + SPCC**, download the Gaia catalogues once: `just download-catalogues`
(`just download-catalogues-spcc` adds the photometric chunks).

## Usage

| Recipe | What it does |
|--------|--------------|
| `just` | List every recipe. |
| `just setup` / `just up` / `just migrate` | First-run setup · start Postgres · apply schema. |
| `just inspect DIR` | Print the classified inventory of a capture folder (no processing). |
| `just process MODE FORMAT PATH` | Full auto pipeline. MODE: `deepsky`·`nebula`·`milkyway`·`planetary`·`comet`; FORMAT: `image`·`video`·`both`. Pass-through flags after the path (e.g. `-v --supervise`). |
| `just video FILE` | Shortcut for `process planetary video` (lucky imaging). |
| `just refine RUNDIR` | Re-run **only** the finish (AI supervisor) on an existing run — no re-stacking. |
| `just dev` / `just web` | Host API with hot reload · Vue dev server. |
| `just stack` / `just stack-down` | The whole app in Docker (no AI model) · stop it. |
| `just run-ia-model` | Serve the local vision model (first run downloads ~28 GB). |
| `just demo tour` | Record a narrated demo video of the UI ([tools/demo](tools/demo/README.md)). |
| `just check` | Lint + test — the pre-push gate. |

### Modes

| Mode | Input | Pipeline |
|------|-------|----------|
| `deepsky` | mono FITS (L/R/G/B/Ha…) | calibrate → grade → stack per channel → co-register → GIMP LRGB+Ha composite (palettes: natural/HaRGB/HOO/SHO/Foraxx/mono) |
| `nebula` | mono FITS | deepsky retuned for faint emission: lenient grading, Ha-forward, star reduction |
| `milkyway` | one-shot-color (iPhone ProRAW/HEIC, DSLR raw) | photometric develop → sky-only stack → foreground composite + graded look |
| `planetary` | video (SER/AVI/MP4/MOV) or stills | lucky imaging: sharpness-rank → multi-point align → AP-weighted stack → deconvolve |
| `comet` | mono FITS (timestamped) | dual star/comet stacks over one global alignment + auto-fit motion track |
| live stacking | a folder/S3 prefix being written | incremental re-stack during capture, full pipeline on Stop |

How stacking works stage by stage: [docs/pipeline.md](docs/pipeline.md) · per-mode deep dives:
[docs/modes/](docs/modes/README.md).

## The web UI

- **Planner** — [Tonight](docs/planner.md) (ranked targets, astro weather, dark-sky finder, polar
  alignment) · GoTo (mount-alignment star sequences) · Calendar (events almanac). All from keyless
  public data, cached, soft-fail: [docs/planner.md](docs/planner.md).
- **Processing hub** — six tabs: Import & inspect (multi-folder inventory, **presets**, launch),
  Live (live stacking), Tasks (jobs with SSE progress, pause/resume, per-stage rerun, the
  supervisor panel), Runs (on-disk gallery), Library (calibration masters), Storage (S3
  connections, sync, verified free-local, backup/restore). Page-by-page: [docs/ui.md](docs/ui.md).
- **AstroAgent** — a local-model chat with confirmation-gated tools over your jobs, data and sky:
  [docs/agent.md](docs/agent.md).

## Configuration

Everything is env-driven. Copy [`.env.example`](.env.example) (fully commented, grouped) to `.env`
— `just` and Compose both load it; **never commit secrets**. Flagship variables: `SIRIL_BIN` /
`GIMP_BIN` (host tools), `ASTRO_DATA_DIR`/`ASTRO_OUTPUT_DIR`/`ASTRO_LIBRARY_DIR` (data roots),
`ASTRO_LLM_URL`/`ASTRO_LLM_MODEL` (supervisor model), `ASTRO_SPCC_SENSOR` (must match Siril's
database), `ASTRO_LAT`/`ASTRO_LON` (observing site), `ASTRO_S3_*` (S3 fallback credentials). Full
tables with defaults: [docs/configuration.md](docs/configuration.md).

## Architecture & docs

Host Go engine (CLI + HTTP API + in-process worker pool; no Redis) driving Siril/GIMP/ffmpeg and
the optional AI tools; Vue 3 frontend; Postgres via raw `pgx/v5` with embedded migrations; MCP
servers for Claude (`siril`, vendored `gimp`). The docs are topic-organized:

| Doc | Covers |
|---|---|
| [architecture.md](docs/architecture.md) | system shape, components, containerized `stack` mode, provenance & tool health |
| [pipeline.md](docs/pipeline.md) | how stacking is made, stage by stage |
| [calibration.md](docs/calibration.md) | master library, cross-session pools, **dark defect maps**, matching rules |
| [modes/](docs/modes/README.md) | per-mode deep dives (deepsky · nebula · milkyway · planetary · comet · livestack) |
| [storage-s3.md](docs/storage-s3.md) | S3 mirror, connections & secrets, verified frees, backup/restore |
| [configuration.md](docs/configuration.md) | every environment variable |
| [api.md](docs/api.md) | the HTTP API reference |
| [planner.md](docs/planner.md) | the sky-planner pages and their data sources |
| [ui.md](docs/ui.md) | the web UI, page by page |
| [agent.md](docs/agent.md) | the local AI: finish supervisor, AstroAgent chat, series |
| [verification.md](docs/verification.md) | end-to-end verification recipes with pass criteria |

## Development

- `just check` runs `go vet` + `golangci-lint` + `vue-tsc` + the test suites (mirrors the pre-push
  gate). **Go tests run on the host** (they exercise host `siril-cli`); start Postgres first.
- House conventions live in [`./conventions/`](conventions/); project rules in
  [`CLAUDE.md`](CLAUDE.md). MCP servers are registered in `.mcp.json` (build once:
  `just build-mcp`, included in `just setup`).
- Verification recipes with objective pass criteria: [docs/verification.md](docs/verification.md).

## License

MIT
