# AstroStack

**English** · [Français](README.fr.md)

> An end-to-end astrophotography workstation: plan the night, drive the rig, then auto-sort,
> calibrate, stack and finish what you shot — deep-sky, planetary, solar, comets, mosaics and
> Milky Way panoramas.

It started as a stacker and is now the whole night. **Plan** what is worth shooting and when,
**drive** the camera, filter wheel and mount, **process** the captures through one of ten recipes,
and **review** the result with its full provenance. Every step is a Go engine and a Vue web UI over
tools that already do the hard parts well — **Siril** for the heavy lifting, **GIMP** for the
finish, with optional **GraXpert** / **StarNet++** and an opt-in local vision model that critiques
and re-tunes the finish.

Built around a Takahashi FC-100 DF + ZWO ASI 1600MM Pro and a RedCat 51 + ASI2600MC, but the rig is
just configuration — a DSLR, a one-shot-colour camera or an iPhone works with nothing to set up.

| | | |
|---|---|---|
| **Plan** | Tonight's ranked targets, astro weather, dark-sky finder, GoTo alignment stars, an events almanac and a 3-D solar system | [planner.md](docs/planner.md) |
| **Capture** | Live view, full camera control, filter wheel, auto-run sequencer, focus meter, plate-solve centring, polar alignment, session logbook | [ui.md](docs/ui.md) · [mount.md](docs/mount.md) |
| **Process** | Ten modes over mono or colour, a cross-session calibration library, frame grading, S3 mirroring, live stacking | [pipeline.md](docs/pipeline.md) · [modes/](docs/modes/README.md) |
| **Review** | Run gallery with full provenance, per-stage previews and full-resolution stage export, an AI finish supervisor, and a chat agent over your own data | [agent.md](docs/agent.md) |

There are two ways to run it. **`just stack`** puts everything in Docker — the engine image bakes in
Linux Siril, GIMP and GraXpert — and is the one-command path for a new machine, a server, or "just
make it run". **Host mode** runs the Go engine on your Mac against your own Siril/GIMP with only
Postgres in Docker; it is faster to iterate on and is what the Go tests exercise. That second mode
is a deliberate exception to the usual everything-in-a-container rule, because Siril and GIMP are
desktop applications that cannot run in a Linux container on macOS. See
[docs/architecture.md](docs/architecture.md).

## Quickstart

Everything in Docker — nothing to install but Docker and [`just`](https://github.com/casey/just):

```bash
git clone <repo-url> && cd astronomy
just stack
```

That is the whole of it. `just stack` creates `.env` and the data directories, checks Docker and
the ports, builds the images, waits for the engine, reports which tools are present and what
degrades without each one, and prints the URL. It is idempotent — re-run it any time.

**The first build takes 15–40 minutes** and produces a multi-GB image: it bakes in Linux
Siril, GIMP, GraXpert, GDAL and ffmpeg so nothing has to be installed on your machine. Later runs
reuse it.

Open the URL it prints (<http://localhost:8082> by default) → **Processing → Import**, point it at
a capture folder under `./input`, launch a run. Every page has a **help** button that opens a
guided tour of that page.

<details>
<summary><b>Host mode</b> — the faster daily loop on macOS (engine on the host, Postgres in Docker)</summary>

Siril and GIMP are desktop applications that cannot run in a Linux container on macOS, so for daily
development the Go engine runs on the host and drives the ones you already have — a deliberate
[exception](docs/architecture.md#deliberate-deviation) to the everything-in-Docker rule. It is
faster to iterate on and it is what the Go tests exercise.

```bash
git clone <repo-url> && cd astronomy
just setup                    # Go deps, dev tools, Siril MCP binary, frontend deps (idempotent)
just doctor                   # what's installed, and what degrades without it
just up                       # Postgres (Docker); the engine migrates itself on boot

mkdir -p input                # ASTRO_DATA_DIR — the root the UI may browse (git-ignored, so a
cp -r /path/to/captures input/M31   # clone has none of the data dirs; no sample data ships)

# A) the web UI (two terminals):
just dev                      # API on http://localhost:8080  (host; drives Siril/GIMP)
just web                      # UI  on http://localhost:5173

# B) or one-shot from the CLI:
just process deepsky image input/M31         # mono LRGB+Ha, or colour — auto-detected
just process planetary video input/moon.mp4  # lucky imaging
```

</details>

The finish supervisor's ~28 GB vision model is **opt-in and decoupled** — `just stack` never
downloads it. Add it with `just run-ia-model` (macOS, native Metal) or `just stack-ai` +
`just ai-pull` (Linux + NVIDIA GPU). Per-environment matrix, ports and stack variables:
[docs/architecture.md → Fully containerized mode](docs/architecture.md#fully-containerized-mode-stack).

For the walkthrough with failure modes spelled out, see
[docs/getting-started.md](docs/getting-started.md).

## Prerequisites

**Container mode (`just stack`) — this is all you need:**

- [Docker](https://docs.docker.com/desktop/install/mac-install/) (Desktop on macOS, running) ·
  [`just`](https://github.com/casey/just) (`brew install just`)

Everything the pipeline drives is baked into the engine image. Nothing else is installed on your
machine.

**Host mode — additionally, on the host itself:**

- **Required**: macOS (Apple Silicon recommended) · Go 1.23+ · Node 22/pnpm ·
  **Siril 1.4+** (`brew install --cask siril`) · ffmpeg (`brew install ffmpeg`)
- **Recommended**: GIMP (the LRGB+Ha finish; absent → Siril `rgbcomp` fallback) · LibRaw
  (`brew install libraw` — develops DSLR/phone raws) · Python 3.12 (Siril's plate-solve/SPCC
  scripting)
- **Optional**: [GraXpert](https://www.graxpert.com) (AI background/denoise) ·
  [StarNet++ v2](https://www.starnetastro.com) (star reduction) · a local vision model
  (`just run-ia-model`) for the [finish supervisor](docs/agent.md)

Run **`just doctor`** to see which of these you have and what each missing one costs — the same
report `just stack` prints from inside the container. Copy-pasteable install commands are in
[docs/getting-started.md](docs/getting-started.md#2-install-the-prerequisites).

The optional tools are **soft-fail** (missing → warning + fallback; disable with `--no-ai`) and
are *invoked, never bundled* — their licences stay with your install. StarNet++ is therefore never
in the image either: mount it and set `STARNET_BIN`. For **offline plate-solving + SPCC**, download
the Gaia catalogues once: `just download-catalogues` (`just download-catalogues-spcc` adds the
photometric chunks).

## Usage

`just` on its own lists every recipe. The ones you will actually use:

| Recipe | What it does |
|--------|--------------|
| `just` | List every recipe. |
| `just stack` / `just stack-down` / `just stack-logs` | **The whole app in Docker** — set up, build, run, report, print the URL · stop it · tail its logs. |
| `just doctor` | Which external tools this machine has, and what degrades without each one. |
| `just setup` / `just up` / `just down` | Host-mode first-run setup · start Postgres · stop the stack. |
| `just migrate` / `just migrate-down` | Apply / roll back schema migrations (`dev` migrates on boot, so this is rarely needed). |
| `just inspect DIR` | Print the classified inventory of a capture folder (no processing). |
| `just process MODE FORMAT PATH` | Full auto pipeline. MODE: `deepsky`·`nebula`·`milkyway`·`nightpano`·`planetary`·`comet`·`mosaic`·`sun`·`eclipse`·`livestack`; FORMAT: `image`·`video`·`both`. Pass-through flags after the path (e.g. `-v --supervise`). |
| `just video FILE` | Shortcut for `process planetary video` (lucky imaging). |
| `just refine RUNDIR` | Re-run **only** the finish (AI supervisor) on an existing run — no re-stacking. |
| `just dev` / `just web` | Host API with hot reload · Vue dev server. |
| `just device` / `just device-x86` | Camera/mount/wheel server — simulator, or a real ZWO under Rosetta. |
| `just device-status` / `just mount-doctor` | Health-check the device server · diagnose the mount USB link. |
| `just mount-audit` / `just mount-reset` | Read back every setting stored in the mount · put back the ones this app can write. |
| `just run-ia-model` / `just ia-model-status` | Serve the local vision model (first run downloads ~28 GB) · check it. |
| `just download-catalogues` | Offline Gaia catalogues for plate-solving (~3 GB; `-spcc` adds photometric colour calibration). |
| `just download-deepstars` | The 2.5M-star catalogue behind star annotation and the 3D field map. |
| `just download-planet-textures` | Surface maps for the 3-D solar system (optional; absent → procedural shading). |
| `just demo tour` | Record a narrated demo video of the UI ([tools/demo](tools/demo/README.md)). |
| `just tour-shots` | Regenerate the in-app help-tour screenshots (re-run when the UI changes). |
| `just test` / `just lint` / `just fmt` | Test · lint and type-check · auto-format. |
| `just check` | Lint + test — the pre-push gate. |
| `just clean` | **Destructive**: drop containers, volumes and build output. |

The `just gitnexus-*` recipes drive an author-side code-graph index and need a tool that is not part
of this project — ignore them.

### Modes

**Colour is automatic.** Every mode accepts monochrome filter-wheel frames *or* one-shot colour — a
DSLR/mirrorless raw (NEF/CR2/CR3/ARW/RAF/DNG), a colour camera's Bayer FITS, or plain RGB
TIFF/PNG/JPEG. It is detected while inspecting the folder and stacked as a single RGB channel, with
calibration applied in CFA space before demosaicing. Nothing to configure.

| Mode | Input | What it does |
|------|-------|--------------|
| [`deepsky`](docs/modes/deepsky.md) | mono FITS (L/R/G/B/Ha/OIII/SII), or colour | calibrate → grade → stack per channel → co-register → GIMP LRGB composite with Ha/OIII/SII emission screens (palettes: natural/HaRGB/HOO/SHO/HOS/Foraxx/mono). Colour finishes the RGB master directly |
| [`nebula`](docs/modes/nebula.md) | mono FITS, or colour | deepsky retuned for faint emission: lenient grading, Ha-forward, star reduction |
| [`milkyway`](docs/modes/milkyway.md) | one-shot colour (iPhone ProRAW/HEIC, DSLR raw) | photometric develop → sky-only stack → foreground composite + graded look; optional meteor recovery |
| [`nightpano`](docs/modes/nightpano.md) | a hand-swept arc of pointings | each panel stacked by the milkyway recipe, then plate-solved at a 70° field, fitted to one shared lens and reprojected onto a spherical canvas |
| [`planetary`](docs/modes/planetary.md) | video (SER/AVI/MP4/MOV) or stills | lucky imaging: sharpness-rank → multi-point align → AP-weighted stack → deconvolve |
| [`comet`](docs/modes/comet.md) | timestamped FITS, mono or colour | dual star/comet stacks over one global alignment + auto-fit motion track |
| [`mosaic`](docs/modes/mosaic.md) | overlapping panels of one large object | per-panel deepsky stacks → plate-solve each → reproject onto one canvas + feathered blend |
| [`sun`](docs/modes/sun.md) | Hα or white-light video/stills | exposure-tier composite, limb-registered lucky imaging, PSF measured off the limb |
| [`eclipse`](docs/modes/eclipse.md) | a partially eclipsed Sun | the solar recipe fitted to TWO circles, the Moon masked out of the stack and every on-disc measurement; can render the whole event as one progression sheet |
| [`livestack`](docs/modes/livestack.md) | a folder/S3 prefix being written | incremental re-stack during capture, full pipeline on Stop |

How stacking works stage by stage: [docs/pipeline.md](docs/pipeline.md) · per-mode deep dives:
[docs/modes/](docs/modes/README.md).

## The web UI

Page by page, with what each control means: [docs/ui.md](docs/ui.md).

- **Tonight** — ranked targets for your site, gear and the moon, with altitude charts, a sky map,
  animated weather layers, an astro-weather panel you can step night by night, a **dark-sky finder**
  (darkness, tree horizon, driving distance) and a polar-alignment helper.
- **GoTo** — a well-spread, ordered set of mount-alignment stars for six hand-controller profiles,
  walked interactively; the server re-plans around what you centre or skip.
- **Calendar** — an events almanac (eclipses, phases, showers, conjunctions, oppositions, ISS
  passes, comets), each scored for your site and gear.
- **Solar system** — the system in 3-D, every planet where it actually is, on its real axis, with a
  time machine spanning 1800–2050.
- **Capture** — live view with histogram and zoom; full camera control (exposure, gain, offset,
  cooling, and everything else the camera reports); a filter wheel with named slots; a multi-filter
  auto-run sequencer; a focus-quality meter; mount GoTo with plate-solve centring; SER recording; a
  calibration wizard; and an audit that reads back everything stored in the mount.
- **Logbook** — every session past and current: what you shot, when, through what, and under what
  sky, with the night's conditions condensed to a score.
- **Mosaic** — plan a tiled panel grid for a large object, then capture and stack it.
- **Processing** — six tabs: Import & inspect (multi-folder inventory, presets, launch), Live,
  Tasks (SSE progress, pause/resume, per-stage rerun, the supervisor panel), Runs (on-disk gallery
  with full-resolution stage export), Library (calibration masters), Storage (S3 connections, sync,
  verified free-local, backup/restore).
- **AstroAgent** — a local-model chat with confirmation-gated tools over your jobs, data and sky:
  [docs/agent.md](docs/agent.md).

Every page has a **help** button that opens a guided tour of that page.

### Connecting real hardware

Devices run in a separate process, started with `just device` (a full simulator, no hardware needed).

For a **real ZWO camera or filter wheel on an Apple-Silicon Mac**, use `just device-x86` instead. ZWO
publish no arm64 macOS library — their SDK and their own ASIStudio are x86_64 only — so the sidecar is
built for x86_64 and run under Rosetta, while the engine and all stacking stay native arm64. The
libraries are picked up from ASIStudio automatically, or set `ASI_SDK_LIB` / `EFW_SDK_LIB`. Details in
[docs/architecture.md](docs/architecture.md).

The mount speaks the Celestron NexStar protocol over the hand controller's USB port (`just device`
lists candidate serial ports).

## Configuration

Everything is env-driven. Copy [`.env.example`](.env.example) (fully commented, grouped) to `.env`
— `just` and Compose both load it; **never commit secrets**. Note that the data roots are all
git-ignored, so **a fresh clone has none of them**: create `ASTRO_DATA_DIR` (default `./input`, the
only folder the UI may browse) before looking for your captures in the file browser.
Flagship variables: `SIRIL_BIN` /
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
| [getting-started.md](docs/getting-started.md) | **start here** — clone to first stacked image, with the failure modes |
| [architecture.md](docs/architecture.md) | system shape, components, containerized `stack` mode, provenance & tool health |
| [pipeline.md](docs/pipeline.md) | how stacking is made, stage by stage |
| [stacking.md](docs/stacking.md) | combination methods, rejection algorithms, normalization and weighting |
| [calibration.md](docs/calibration.md) | master library, cross-session pools, **dark defect maps**, matching rules |
| [modes/](docs/modes/README.md) | per-mode deep dives — one page each for all ten modes |
| [examples/](docs/examples/) | worked examples: a real run written up end to end, every number measured |
| [mount.md](docs/mount.md) | the Celestron hand-controller link: wiring, the macOS driver trap, recovery, the overnight soak |
| [storage-s3.md](docs/storage-s3.md) | S3 mirror, connections & secrets, verified frees, backup/restore |
| [configuration.md](docs/configuration.md) | every environment variable |
| [api.md](docs/api.md) | the HTTP API reference |
| [planner.md](docs/planner.md) | the sky-planner pages and their data sources |
| [ui.md](docs/ui.md) | the web UI, page by page |
| [agent.md](docs/agent.md) | the local AI: finish supervisor, AstroAgent chat, series |
| [third-party.md](docs/third-party.md) | every external tool, catalogue, data service and library, with its licence |
| [verification.md](docs/verification.md) | end-to-end verification recipes with pass criteria |

## Development

- `just check` runs `go vet` + `golangci-lint` + `vue-tsc` + the test suites (mirrors the pre-push
  gate). **Go tests run on the host** (they exercise host `siril-cli`); start Postgres first.
- House conventions live in [`./conventions/`](conventions/); project rules in
  [`CLAUDE.md`](CLAUDE.md). MCP servers are registered in `.mcp.json` (build once:
  `just build-mcp`, included in `just setup`).
- Verification recipes with objective pass criteria: [docs/verification.md](docs/verification.md).

## License

MIT — for the code in this repository.

AstroStack orchestrates a great deal of other people's work: **Siril** and **GIMP** do the stacking and
the finishing, and the sky itself comes from **Open-Meteo**, **NASA/NOAA VIIRS**, the **HYG**/**ATHYG**
and **OpenNGC** catalogues, **Gaia DR3**, the **Minor Planet Center**, **CelesTrak**,
**OpenStreetMap** and others. Every tool is invoked rather than bundled, and every feed is fetched at
runtime under its own terms.

Two of those terms bind a redistributor: **Open-Meteo's free tier is non-commercial** and its data is
CC BY 4.0 (the attribution is rendered in the UI), and the **HYG, ATHYG and OpenNGC catalogues are CC
BY-SA**. The complete list, with licences and the reasoning behind each choice, is in
[docs/third-party.md](docs/third-party.md).
