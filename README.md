# AstroStack

**English** · [Français](README.fr.md)

> Point it at a folder of astrophotography captures and it auto-sorts, grades, calibrates, stacks, and
> finishes them into the best possible final image — and plans your next session while you're at it.

AstroStack inspects a capture directory, figures out **what's in it** (light frames per filter
L/R/G/B/Ha, plus darks, bias/offsets, flats, dark-flats), **discards the bad sub-frames** (elongated
stars, trails, clouds), picks and applies the **right master calibration frames**, then registers and
stacks per channel and combines them (LRGB + Ha) into a finished image. It drives **Siril** for the
heavy lifting and **GIMP** for the finish, with optional AI enhancers (**GraXpert**, **StarNet++**) and
an opt-in **local vision-model supervisor** that auto-tunes the finish. A built-in **session planner**
(tonight's targets, GoTo alignment, an events calendar, weather and light-pollution overlays) rounds
out the workflow. Built for a Takahashi FC-100 DF + ZWO ASI 1600MM Pro mono rig, but the rig is
configurable.

Siril and GIMP are host-installed macOS apps, so the Go engine **runs on the host** and drives them
directly; Docker Compose provides Postgres. This is a deliberate exception to the usual
"everything-in-a-container" rule — see [docs/architecture.md](docs/architecture.md).

---

## Quickstart

```bash
git clone <repo-url> && cd astronomy
cp .env.example .env          # adjust host tool paths / secrets if needed
just setup                    # Go deps, dev tools, Siril MCP binary, frontend deps (idempotent)
just up                       # start Postgres (Docker)
just migrate                  # create the schema

# A) one-shot from the CLI:  just process <mode> <format> <path>
just process deepsky  image ~/Astro/M31          # mono LRGB+Ha → layered .xcf + tif/png
just process planetary video ~/Astro/moon.mp4    # lucky imaging

# B) or the web UI (two terminals):
just dev                      # API on http://localhost:8080  (host; drives Siril/GIMP)
just web                      # UI  on http://localhost:5173
```

Open <http://localhost:5173>, go to **Processing → Import**, point it at a capture folder, and launch a
run.

---

## Prerequisites

This is a **host-side macOS tool**: the engine shells out to host-installed Siril/GIMP/ffmpeg (and the
optional AI tools), so those cannot live in a container. Only Postgres runs in Docker.

### Required

| Tool | Install | Why |
|------|---------|-----|
| macOS (Apple Silicon recommended) | — | the host engine drives macOS app bundles; the AI model server needs Apple MLX |
| Docker + Compose | Docker Desktop | runs Postgres (the only containerized service) |
| [`just`](https://github.com/casey/just) | `brew install just` | task runner — every command below is a recipe |
| Go 1.23+ | `brew install go` | the engine, CLI, and Siril MCP (toolchain pinned to 1.23.3) |
| Node 20+ / pnpm | `brew install node pnpm` | the Vue web UI |
| Siril | `brew install --cask siril` | calibration · registration · stacking · stretch (**the core engine**) |
| ffmpeg | `brew install ffmpeg` | video frame extraction + Ken-Burns MP4 render |

### Recommended

| Tool | Install | Why |
|------|---------|-----|
| GIMP | `brew install --cask gimp` | the LRGB+Ha finish composite; absent → falls back to Siril `rgbcomp` |
| Python 3.12 | `brew install python@3.12` | Siril's bundled scripting for **plate-solving + SPCC color calibration** (without it the sky can go brown); also powers the AI model venv |

### Optional (AI enhancers + the local supervisor)

| Tool | Install | Why |
|------|---------|-----|
| GraXpert | [graxpert.com](https://www.graxpert.com) | AI background-gradient extraction / denoise; absent → Siril `subsky` |
| StarNet++ v2 | [starnetastro.com](https://www.starnetastro.com) | star removal for star-reduced finishing; absent → full stars |
| Local vision model | `just run-ia-model` (downloads ~26 GB) | the opt-in **finish supervisor** — a host MLX vision model that critiques and re-tunes the finish (see below) |
| OpenCV | `brew install opencv` | only for the optional GoCV trail detector; absent → the pure-Go Hough detector |

GraXpert and StarNet++ are **soft-fail**: when the binary is missing the run logs a warning and falls
back. Disable both explicitly with `process … --no-ai`. None of the optional tools are bundled — they
are *invoked* like Siril/GIMP, so their licences stay with your own install.

---

## Usage

| Recipe | What it does |
|--------|--------------|
| `just setup` | First-run setup (Go deps, dev tools, MCP binary, frontend deps). Idempotent. |
| `just up` / `just down` | Start / stop Postgres (Docker). |
| `just migrate` / `just migrate-down` | Apply / roll back DB migrations. |
| `just inspect DIR` | Print the classified inventory of a capture folder (no processing). |
| `just process MODE FORMAT PATH` | Full auto pipeline. MODE: `deepsky`·`nebula`·`milkyway`·`planetary`; FORMAT: `image`·`video`·`both`. Input type is auto-detected. |
| `just video FILE` | Shortcut for `process planetary video` (lucky imaging). |
| `just refine RUNDIR` | Re-run **only** the finish (via the AI supervisor) on an existing run — no re-stacking. |
| `just dev` | Run the API server on the host with hot reload. |
| `just web` | Run the Vue web UI dev server. |
| `just run-ia-model` | Serve the local vision model for the finish supervisor (first run downloads ~26 GB). |
| `just stop-ia-model` / `just ia-model-status` | Stop / health-check the model server. |
| `just mcp-siril` | Run the Siril MCP server in the foreground (manual testing). |
| `just tools` | Start Adminer (DB UI) at `http://localhost:8081`. |
| `just check` | Lint + test (the pre-push gate). |
| `just clean` | Remove containers, volumes, and build artifacts (destructive; confirms). |

`just process` accepts pass-through flags after the path, e.g.
`just process deepsky image ~/Astro/M31 -v --supervise`.

### Modes

Each mode retunes grading, background extraction, stretch, Ha blend, saturation and curves:

| Mode | Input | Pipeline |
|------|-------|----------|
| `deepsky` | mono FITS (L/R/G/B/Ha) | calibrate → grade → stack per channel → co-register channels → GIMP LRGB+Ha composite + gentle curves |
| `nebula` | mono FITS | like deepsky but lenient grading + AI background extraction + Ha-forward blend + StarNet++ star reduction |
| `milkyway` | one-shot-color (iPhone ProRAW/HEIC, jpg/png/tif) | debayer → register → grade → stack → GIMP curves; tunable *look* (natural/iphone/deepsky) + *brightness* |
| `planetary` | video (SER/AVI/MP4/MOV) | lucky imaging: sharpness-rank frames → stack best % → sharpen |

---

## The web UI

The UI is two top-level areas: a **session planner** (what to shoot, how to align, when events happen)
and the **Processing** hub (turn captures into a final image).

### Planner

| Page | Route | What it's for |
|------|-------|---------------|
| **Tonight** | `/tonight` | Ranks tonight's deep-sky targets for your location, gear, and the moon/darkness conditions. Filters by type/score/framing, camera **or** eyepiece (visual) mode, altitude charts, sky map. Built-in **Polar** (polar-scope reticle + alignment help) and **Dark sky** (light-pollution dark-site finder) tabs. |
| **GoTo** | `/goto` | Computes a well-spread, ordered set of **mount-alignment stars** for your GoTo routine (six mount profiles). Walk the sequence interactively — center/skip — and the server re-plans around your choices. |
| **Calendar** | `/calendar` | An astronomical-events almanac: eclipses, moon phases, meteor showers, conjunctions, oppositions, equinoxes… as a month calendar over a date window, or the next N of a single type — each scored for your site and gear. |

The observing site (map / address / geolocation) is chosen once and shared across all three pages;
weather and light-pollution overlays come from keyless public APIs by default.

### Processing hub (`/processing`)

One page, five tabs:

| Tab | Route | What it's for |
|-----|-------|---------------|
| **Import** | `/processing/import` | The main page: browse and **multi-select capture folder(s)**, inspect/merge them into one inventory, review the channel mapping + light/calibration/file tables, then configure and **launch a run** (mode, output format, calibration toggles, cross-session reuse, and the optional local-AI supervisor). |
| **Live** | `/processing/live` | Start an **incremental live-stacking** session that watches a local folder or S3 prefix and re-stacks as new subs arrive; the heavy finish runs once on **Stop & finalize**. |
| **Tasks** | `/processing/tasks` | The job queue — every run with status + progress; click through to the live detail view. |
| **Runs** | `/processing/runs` | A gallery of past, on-disk runs (independent of the DB); open any to re-render its full result panels. |
| **Library** | `/processing/library` | The reusable **calibration master library** (darks/flats/bias) the pipeline builds and matches across sessions. |

The **job detail** view (under *Tasks*) streams live progress over SSE: a progress bar, the current
step, live CPU/RAM usage, a rolling log, and a live preview image. On completion it shows the final
image/video, per-channel stack stats, per-frame grade charts, the masters used, download links, and —
for supervised runs — the **AI supervisor panel** (one card per iteration with its scores, reasoning,
and a badge on the chosen best).

---

## Demo video

Record a polished walkthrough of the web UI straight from a YAML scenario — animated cursor, captions,
intro/outro cards, optional music + voiceover, and a **×7 time-lapse** of a live stacking job. Generate
a scenario from the docs with the `/demo-video` Claude command, then render it (the app must be running):

```bash
just demo tour        # deterministic: replays an existing finished run (no capture data needed)
just demo overview    # full tour incl. a live ×7 stacking job
```

The MP4 lands in `output/demo/`. It's a host tool (Playwright + the host `ffmpeg`); details and the
scenario schema are in [`tools/demo/README.md`](tools/demo/README.md).

---

## How processing works

A *functional schema* of what happens when you process a folder — from the Import page through the
engine to the final files.

### Data flow (UI ↔ engine ↔ tools)

```mermaid
flowchart TB
    subgraph UI["Browser — Vue 3 UI (:5173)"]
        IMP["Import tab<br/>pick folder(s) · mode · format<br/>· toggles · reuse · ☐ AI supervisor"]
        JOB["Job view<br/>live progress · preview · logs"]
    end

    subgraph ENG["astrostack engine — host (:8080)"]
        INS["POST /api/inspect<br/>classify + merge folders"]
        REU["POST /api/reuse/preview<br/>discover prior sessions"]
        CRE["POST /api/jobs<br/>→ in-process worker pool"]
        PIPE["pipeline.Process"]
        SSE["GET /api/jobs/:id/events (SSE)"]
    end

    subgraph TOOLS["External host tools"]
        SIRIL["Siril — calibrate/register/stack/stretch"]
        GIMP["GIMP — LRGB+Ha composite"]
        GRAX["GraXpert (opt)"]
        STAR["StarNet++ (opt)"]
        LLM["MLX vision model (opt, :1234)"]
        FF["ffmpeg — video"]
    end

    PG[("Postgres<br/>jobs · frame catalog · masters")]

    IMP -->|select| INS
    IMP --> REU
    IMP -->|Run| CRE
    CRE --> PIPE
    PIPE --> SIRIL & GIMP & GRAX & STAR & LLM & FF
    PIPE <--> PG
    PIPE -. progress+preview .-> SSE
    SSE -. stream .-> JOB
    CRE -->|job id| JOB
```

### The pipeline (deep-sky)

```mermaid
flowchart LR
    A["1· Inspect<br/>classify L/R/G/B/Ha<br/>+ darks/flats/bias"] --> B["2· Master calibration<br/>build/reuse masters<br/>(library · cross-session)"]
    B --> C["3· Per channel<br/>calibrate → register<br/>→ grade/reject → stack"]
    C --> D["4· Co-register channels<br/>L·R·G·B·Ha aligned"]
    D --> E["5· Finish<br/>GraXpert bg → SPCC → stretch<br/>→ GIMP LRGB+Ha → StarNet"]
    E --> F["6· (opt) AI supervisor<br/>re-render finish a few times,<br/>score, keep the best"]
    F --> G["Outputs<br/>.xcf · tif · png · mp4<br/>+ JSON/Markdown report"]
```

1. **Inspect** — walk the folder(s), read each FITS header, classify every file (light/dark/flat/
   bias/dark-flat/video) and group into *sets* by object, filter, exposure, gain, offset, temperature
   and binning. Bare-filename legacy captures are labelled from an `info.txt` sidecar. Multiple folders
   are merged into one inventory, so calibration in one can serve lights in another.
2. **Master calibration** — stack a master per calibration set (Winsorized sigma). Masters are saved to
   a **reusable library** and matched to each light set (darks by exposure+temp+gain+offset, flats by
   filter, bias by gain+offset); a lights-only session pulls the right masters from the library.
3. **Per channel** — calibrate + register the lights, **grade** each sub from its registration metrics
   (FWHM, roundness, star count, background) plus a Hough **trail detector**, reject the bad ones, and
   stack only the survivors.
4. **Co-register channels** — register the per-channel masters to one reference so L/R/G/B/Ha line up
   before compositing.
5. **Finish** — background-extract (GraXpert if present, else Siril `subsky`), color-calibrate (SPCC),
   stretch, then drive the resident GIMP Script-Fu server to build a layered image (RGB + L luminance +
   Ha screen), apply gentle curves, and export an editable `.xcf` plus flattened TIFF/PNG. Optional
   StarNet++ produces a star-reduced variant. If GIMP is unavailable it falls back to Siril `rgbcomp`.
6. **AI supervisor** *(opt-in)* — when enabled, a bounded loop re-renders only the fast GIMP composite a
   few times (varying saturation, Ha screen/black-point, chroma blur, crop), scores each render with
   deterministic metrics **and** a local vision model, and keeps the best. Off by default → a
   byte-identical single-pass finish.

Each run writes its outputs + a JSON/Markdown report; with the API the full report (per-frame grades,
masters used, integration breakdown) is stored on the job and rendered in the **Runs**/job view.

For the planetary path, cross-session reuse, and per-mode tuning, see
[docs/pipeline.md](docs/pipeline.md).

### The opt-in AI finish supervisor

A small, fully local feature that treats the finish as an optimisation loop. Start the model once with
`just run-ia-model` (serves an MLX vision model — Qwen2.5-VL by default — on `:1234`), then enable it
per run: tick **"Run with local AI agent"** in the Import page (deep-sky/nebula only), pass
`process … --supervise`, or `just refine <run-dir>` to re-tune an existing stack without re-stacking.
It is **off by default and soft-fails**: with the box unticked or the server down, the finish is
identical to the standard pipeline. See [`.env.example`](.env.example) (`ASTRO_LLM_*`) for the knobs.

---

## How the planner gets its data

A *functional schema* of how the planner's three pages get their data — stars, events, weather, light
pollution — which API serves each, where every value comes from, and what's **cached versus stored**.

### Data flow (UI ↔ engine ↔ data sources)

```mermaid
flowchart TB
    subgraph UI["Browser — planner pages (:5173)"]
        PAGES["Tonight · GoTo · Calendar<br/>+ one shared observing site<br/>(lat/lon/elev/tz)"]
    end

    subgraph ENG["astrostack engine — GET /api/sky/* (:8080)"]
        SKY["targets · events · series · polar · align<br/>geocode · lightpollution (+tiles)<br/>darksites · weather (+grid)"]
    end

    subgraph SRC["Where each value comes from — no database"]
        EMB["① Embedded / on-disk catalogs<br/>bright-star + meteor-shower (go:embed)<br/>Siril DSO name→coords (disk + in-mem)"]
        CMP["② Local compute, per request<br/>planets · moon · eclipses · seasons<br/>visibility/optics · SGP4 passes · polar/horizon"]
        EXT["③ External feeds — keyless, soft-fail<br/>Open-Meteo · 7Timer! · NOAA SWPC · NASA GIBS VIIRS<br/>CelesTrak TLE · Minor Planet Center · OSM Nominatim"]
    end

    DISK[("Disk cache<br/>work/cache/&lt;domain&gt;<br/>TTL + stale-offline fallback")]
    PG[("Postgres<br/>jobs · frames · masters<br/>— imaging only —")]

    PAGES -->|lat/lon · time| SKY
    SKY --> EMB & CMP & EXT
    EXT <-->|cache| DISK
    SKY -. sky data is never persisted .-> PG
```

### The sky-data API (function schema)

Every planner value is served read-only under `GET /api/sky/*` (registered in `internal/api/api.go`);
the browser passes the shared observing site (`lat`/`lon`/`time`) and the engine fans out to the source
in the last column. The imaging endpoints (`/api/inspect`, `/api/jobs`, `/api/reuse/*`) belong to the
[processing flow above](#how-processing-works), not here.

| Endpoint (all `GET`) | Feeds | Data source | Local store |
|----------------------|-------|-------------|-------------|
| `/api/sky/targets` | Tonight — ranked targets | Siril DSO catalog (disk) + visibility/optics compute | in-mem catalog |
| `/api/sky/events` | Calendar — almanac over a window | ephemeris + embedded showers + CelesTrak TLE + MPC comets | disk cache |
| `/api/sky/series` | Calendar — next N of one type | same as `events` | disk cache |
| `/api/sky/align` | GoTo — alignment-star sequence | embedded `brightstars.csv` + max-spread compute | embedded |
| `/api/sky/polar` | Tonight → Polar reticle | celestial-pole geometry (compute) | — |
| `/api/sky/lightpollution` | Per-site SQM/Bortle + visibility scores | keyed API → atlas → NASA GIBS VIIRS → default | mem + disk (~720 h) |
| `/api/sky/lightpollution/tiles/{z}/{x}/{y}` | Map overlay tiles | NASA GIBS VIIRS Black Marble (server-proxied) | disk tiles |
| `/api/sky/darksites` | Tonight → Dark-sky finder | light-pollution × Open-Meteo elevation (compute) | inherits lp/elev caches |
| `/api/sky/weather` | Tonight — forecast/seeing panel | Open-Meteo + 7Timer! ASTRO + NOAA SWPC (Kp) | mem + disk (~30 min) |
| `/api/sky/weather/grid` | Tonight — weather map layer | Open-Meteo multi-point grid | disk cache |
| `/api/sky/geocode` | Site picker (address → coords) | OpenStreetMap Nominatim | — (per request) |

Every feed is **keyless by default and soft-fails**: a dead upstream falls back to the disk cache (even
if stale), then to the offline atlas / local compute / a configurable default — so no `/api/sky/*` call
returns a hard error. Weather degrades per-source (one feed down ≠ the panel down). An optional
calibrated light-pollution provider takes a key, read **server-side only** (never the UI, never logged).

### What's stored locally — and what isn't

**In Postgres** — the imaging system of record (raw `pgx/v5`, embedded `*.up.sql` migrations; no ORM):

- `sessions`, `frames`, `frame_metrics` — capture sessions, the per-frame catalog, and per-frame grades.
- `master_frames` — the calibration-master *index* (the FITS blobs themselves live on disk in `library/`).
- `jobs`, `outputs`, `finish_iterations` — processing runs, their artifacts, and supervised-finish steps.
- `targets` — canonical deep-sky name + coords, used **only** to group light frames for cross-session
  reuse. It is populated from the imaging path (frame object names), not from the planner.

**Never in Postgres** — every planner/sky datum lives in one of three non-DB tiers:

- *Embedded in the binary* (`go:embed`) — the bright-star alignment catalog and the meteor-shower table.
- *Computed per request* — target visibility, planets/moon/eclipses/seasons, satellite passes (SGP4),
  polar & horizon geometry, and the dark-site grid search.
- *Fetched from keyless feeds, cached on disk* under `work/cache/<domain>` (TTL + stale-offline
  fallback) — weather, light pollution (incl. VIIRS map tiles), elevation/horizon, comet elements, and
  satellite TLEs. Geocoding is per-request (uncached). Siril's bundled DSO catalogs are read from disk
  and memoized in memory.

> In short: **Postgres is the imaging system of record; the planner is stateless over cached public
> data.** Delete `work/cache/` and every sky value simply re-fetches or recomputes — nothing is lost.

All feeds are overridable in `.env` (`ASTRO_WEATHER_*`, `ASTRO_LIGHTPOLLUTION_*`, `ASTRO_ELEVATION_*`,
plus the `ASTRO_LAT`/`ASTRO_LON`/rig site knobs) — see [Configuration](#configuration) below.

---

## Configuration

All configuration is via `.env` (copy from [`.env.example`](.env.example); the real `.env` is
git-ignored — **never commit secrets**). The most important variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_URL` | `postgres://astro:astro@localhost:5432/astrostack?sslmode=disable` | Postgres DSN used by the host engine. |
| `API_ADDR` | `:8080` | Address the API server listens on. |
| `ASTRO_DATA_DIR` | `./data` | Root the web UI may browse for capture folders. |
| `ASTRO_WORK_DIR` | `./work` | Scratch space for intermediate FITS/sequences. |
| `ASTRO_OUTPUT_DIR` | `./output` | Where final stacks and reports are written. |
| `ASTRO_LIBRARY_DIR` | `./library` | Persistent calibration-master library. |
| `SIRIL_BIN` | `/Applications/Siril.app/Contents/MacOS/siril-cli` | Headless Siril. |
| `GIMP_BIN` | `/Applications/GIMP.app/Contents/MacOS/gimp-console-2.10` | Headless GIMP (finish). |
| `FFMPEG_BIN` | `ffmpeg` | ffmpeg binary. |
| `GRAXPERT_BIN` / `STARNET_BIN` | `graxpert` / `starnet++` | Optional AI tools (resolved on `PATH`); missing → fallback. |
| `ASTRO_LLM_URL` / `ASTRO_LLM_MODEL` | `http://127.0.0.1:1234/v1` / — | The opt-in finish-supervisor model server + model id. |
| `ASTRO_MAX_CPUS` / `ASTRO_SIRIL_MEM_RATIO` | `10` / `0.5` | Cap Siril's threads / RAM so a heavy stack doesn't freeze the host. |
| `ASTRO_SPCC_SENSOR` | `ZWO ASI1600MM` | SPCC sensor name — **must** match Siril's database exactly. |
| `ASTRO_REUSE_ENABLED` | `true` | Cross-session reuse (grow integration + deepen calibration). |
| `VITE_API_BASE` | `http://localhost:8080` | API base URL used by the frontend. |
| `ASTRO_LAT` / `ASTRO_LON` / `ASTRO_TIMEZONE` | Paris | Default observing site for the planner (overridable live in the UI). |

`.env.example` documents the full set, grouped: **host tools**, **AI tools + supervisor**, **resource
limits**, **plate-solving + SPCC**, **planner site + rig + eyepieces**, **cross-session reuse**, **live
stacking + S3**, and the keyless **light-pollution / dark-sky / weather** data services. Secrets
(`HF_TOKEN`, S3 keys, weather/light-pollution API keys) are read from the environment only — never the
UI, never logged.

---

## Architecture

The engine runs on the host (it brokers host-installed Siril/GIMP — the same reason the GIMP MCP runs on
the host); Postgres runs in Compose. No Redis: jobs run in an in-process worker pool with state in
Postgres and live progress over SSE.

- **`cmd/astrostack`** — CLI (`inspect`/`process`/`video`/`refine`) + HTTP API server (`serve`).
- **`cmd/siril-mcp`** — Siril MCP server (stdio) for Claude; shares the `internal/` engine.
- **`internal/`** — `fits`, `inspect`, `siril`, `grade`, `calib`, `pipeline`, `planetary`,
  `postprocess`, `graxpert`, `starnet`, `llm` (the supervisor), `store`, `job`, `api`, `report`;
  `astro`/`skycat`/`skyplan` (ephemeris · catalog · visibility) plus `skyevents`, `align`,
  `lightpollution`, `weather`, `elevation`, `darksky` power the planner's `/api/sky/*` endpoints
  (see [How the planner gets its data](#how-the-planner-gets-its-data)).
- **`mcp-servers/gimp/`** — vendored GIMP MCP (Python; GIMP's own tooling — the only Python in the repo).
- **`frontend/`** — Vue 3 + Vite + Pinia + Tailwind + vue-i18n + ECharts + Leaflet.

Persistence is **raw `pgx/v5`** with versioned, embedded SQL migrations applied by `just migrate`. See
[docs/architecture.md](docs/architecture.md) and [docs/pipeline.md](docs/pipeline.md) for depth.

---

## Development

- `just check` runs `go vet` + `golangci-lint` + `vue-tsc` and the test suites (mirrors the pre-push gate).
- House coding conventions live in [`./conventions/`](conventions/); project-specific rules are in
  [`CLAUDE.md`](CLAUDE.md).
- **Go tests run on the host** (they exercise host `siril-cli`); start Postgres first with `just up`.
- MCP servers `siril` (Go) and `gimp` (Python) are registered in `.mcp.json` for Claude Code. `.mcp.json`
  runs `./bin/siril-mcp`, so build it first with `just build-mcp` (included in `just setup`).

---

## Deployment

This is a single-user, host-side tool: Compose provides Postgres (and optionally a built frontend image
under the `web` profile / Adminer under `tools`), while the Go engine runs on the host so it can reach
Siril and GIMP. There is no remote deployment target.

---

## License

MIT
