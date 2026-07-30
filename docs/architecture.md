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
| `internal/photom` | Photometric normalization across mixed-session groups (percentile-curve fit; ON by default for deep-sky — a flat narrowband curve seeds from the header exposure/gain instead of mis-fitting, and the clamp admits genuine cross-gain ratios). |
| `internal/dither` | Pointing-pattern diagnosis from registration offsets (dithered / drift / static) — the walking-noise advisory. |
| `internal/noise` · `internal/imgops` · `internal/optics` | Noise measurement/starlet denoiser, shared image ops, flat-defect QC. |
| `internal/pipeline` | Orchestrate inspect → masters → calibrate → grade → register → stack → combine; soft-fail AI steps in `enhance.go`; palettes, supervisor, per-stage rerun. |
| `internal/preset` | The built-in "best params per situation" catalog (16 recipes) merged with user presets. |
| `internal/postprocess` | LRGB+Ha channel combine, color calibration, stretch; optional GIMP touch-ups. |
| `internal/graxpert` | Optional host CLI: GraXpert AI background-gradient extraction / denoise (`GRAXPERT_BIN`). |
| `internal/starnet` | Optional host CLI: StarNet++ v2 star removal for star-reduced finishing (`STARNET_BIN`). |
| `internal/llm` | Optional, opt-in: drives a host-run OpenAI-compatible vision model to auto-tune the finish for **every stacking mode** — deep-sky/nebula composite, comet colour composite, milkyway grade, planetary sharpen — via per-mode `candidateRenderer` adapters (`internal/pipeline/supervise_*.go`); the shared render→judge→re-tune loop soft-fails when the server is down. |
| `internal/planetary` | SER/AVI/MP4/MOV/stills lucky-imaging path: native-res disk-masked sharpness ranking, multi-point ZNCC alignment, per-AP top-K selection stack (each region built from its locally-sharpest frames), RL deconvolution, true-luminance colour compose (`true_lum`). Opt-in earthshine reveal (`earthshine_gain`): deterministic limb circle fit + SNR-gated lift of the unlit disc, composited after the Siril finish. |
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

### The emission screens (natural family)

Narrowband shot *alongside* LRGB is composited as additive **screen layers** over the broadband base
rather than replacing it, so the data lights the image up instead of sitting unused. All three run the
same machine — continuum-subtract (`excess = line − k·broadband`, so only true emission survives) →
RBF-flatten → autostretch to a dark background → wash-gate → Screen in GIMP — and are declared once as
a table (`internal/pipeline/emissionscreen.go`) rather than as three copies that drift:

| Layer | Continuum ref | Colour | Knob | Default |
|---|---|---|---|---|
| Hα 656 nm | R → L | pure red | `ha_screen` | 0.42 (on) |
| [OIII] 501 nm | G → B → L | teal (red killed) | `oiii_screen` | 0 (opt-in) |
| [SII] 672 nm | R → L | `sii_tint`: deep red *or* gold | `sii_screen` | 0 (opt-in) |

[SII] is the awkward one: at 672 nm it is *deeper* red than Hα, which sRGB cannot express — pure red is
already the end of the ramp — so screening it "more red" would merely brighten the Hα layer. `sii_tint`
picks how to tell them apart instead: `deep_red` (default) keeps a trace of blue for a crimson that
reads as natural, `gold` is the amber accent the Hubble palette established and is far easier to see.

The [OIII] and [SII] screens default to **0**, so a run that does not ask for them emits byte-identical
GIMP script to before the knobs existed. A screen-only layer never constrains anything that reasons
about coverage (`paletteResolved.screenOnly`) — it fades where its nights didn't reach, so letting it
bound a multi-night mosaic crop would collapse the canvas to its own footprint.

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

## Observability & resource metrics

A deep-sky run walks a **named step plan** (`internal/pipeline/progress_steps.go`): masters, each
channel, then the preset-derived finish steps (align → combine+background → optional AI colour
denoise → colour calibration+stretch → GIMP composite → optional StarNet/star-fix → export). Every
step boundary emits `▶ <step>` / `✓ <step> done in <dur> — peak tool RSS <n>` journal lines, warnings
are `⚠`-prefixed and surface live the moment they happen (`warnLive`), a failed job publishes a final
`✗` line, and only these markers (plus one `[i/N] <step>` skeleton per step) are mirrored to the
engine's stdout so `docker logs` stays readable. When the stream goes quiet (the CPU-only AI denoise
can be silent for an hour) a per-job **heartbeat** (`internal/job/heartbeat.go`) publishes
`still running: <step> — 14m into this step, no output for 90s · cpu 10.8/12 cores · rss 6.7 GiB`
after 45 s of silence, then every 30 s (SSE + stdout only, never persisted). Resource numbers come
from a single refcounted **engine-wide sampler** (`internal/job/enginemon.go`): it samples this
process's whole subtree (Siril, GraXpert, StarNet, GIMP, ffmpeg are all children) at 1 Hz and
publishes each running job's live CPU/RSS + job-wide peak + host core count — the job header shows
`x.x / N cores` and stays live through pure-Go/GIMP phases. Known limit: a host-offloaded GraXpert
(`ASTRO_GRAXPERT_URL`) runs outside the subtree and is not counted. Per-step wall times land in
`run.json` (`timings`) plus one final `timing: … · total …` line.

## Deliberate deviation

Running the engine and Go tests on the host is an intentional exception to the house "everything in a
container" rule, forced by the host-Siril/host-GIMP dependency (and the optional GraXpert/StarNet++
CLIs, which run the same way). It is the fastest path for daily macOS dev and is documented in
`CLAUDE.md`.

## Filters: one canonical set, recorded three times

`internal/filters` is the single source of truth for filter names — the canonical set
(`L, R, G, B, Ha, OIII, SII`), the aliases capture programs spell them with (`s2`, `sulfur`, `O3`,
`h-alpha`, Johnson `V`→`G`), the display order and which of them are narrowband. `constants/filters.ts`
mirrors it on the frontend, pinned by a spec. Everything that enumerates or orders filters — ingest,
the stacker, the wash gates, the capture sequencer, chip colours — reads one of those two.

That consolidation is not cosmetic. The lists used to be copy-pasted into a dozen places and drifted:
two of them stopped at `Ha`, so a wheel slot holding `SII` could only ever be named `"S6"`.

**A wheel reports slot numbers, never names.** The slot→filter mapping is entered once in
Capture → Filter slots and stored server-side (`app_settings["capture.filter_slots"]`), because a
5-slot wheel gets its filters swapped between sessions and nothing else records what was fitted. The
sequencer resolves a step's filter *name* against those labels (alias-aware, so a step asking for
`SII` finds a slot labelled `S2`), and every captured frame then records the filter **three
independent times**:

```
<root>/[panel/]<Filter>/Light_300sec_Bin1_filter-SII_-15.0C_gain200_2026-07-29_221403_frame0001.fit
                ^ folder                  ^ file name              plus FILTER = 'SII' in the header
```

Calibration follows the layout ingest already parses: `flats/<Filter>/` (flats are per-filter), and
`darks/` `bias/` `darkflats/` with no filter segment (those group filter-agnostically). Redundancy is
the point — a header stripped by a converter, or a file renamed by hand, still leaves the folder
saying which filter these frames were shot through.

## Real ZWO hardware on Apple Silicon: the x86_64 device sidecar

Device I/O runs in its own process (`astrostack device`) for four reasons — `air` restarts the engine
on every save, a vendor SDK crash must not take the engine with it, live view must keep its cadence
while stacking saturates the CPU, and the process can be built for a different architecture than the
engine. That last one is not hypothetical:

**ZWO publish no arm64 macOS library.** The ASI and EFW SDKs, and ZWO's own ASIStudio, are
`i386 + x86_64` only (`lipo -archs` on the bundled `libASICamera2.dylib` confirms it). A native arm64
engine therefore cannot `dlopen` them, and no newer SDK download fixes that.

The fix is to build **only the sidecar** as x86_64 and let Rosetta 2 run it:

```sh
just device-x86     # cross-builds bin/astrostack-x86 and runs it
```

The engine, the frontend and every bit of stacking stay **native arm64**; they talk to the sidecar
over HTTP on `127.0.0.1:8084` exactly as before. Verified working on an M2 Max: the driver report
goes from *"has no arm64 build"* to `asi: SDK loaded`. ZWO's own software runs the same way here, so
USB access under translation is a well-trodden path. `just device` (native) remains the simulator
path for development.

The libraries are found automatically in `/Applications/ASIStudio.app/Contents/Frameworks`, or point
`ASI_SDK_LIB` / `EFW_SDK_LIB` at an unpacked copy of ZWO's "ASI Camera SDK [Linux & macOS]" download.
As with Siril and GraXpert, the SDK is **invoked, never vendored**.

Rosetta costs perhaps 20–40 % on pure compute, which does not matter here: the sidecar does USB I/O,
a memcpy, a small preview encode and focus metering on a ROI. Everything expensive — stacking, Siril,
GraXpert — stays native in the engine. If a high-frame-rate planetary run drops frames, lower the
camera's **USB bandwidth** control before suspecting translation.

**Control ids are read from the camera, never hardcoded.** `ASIGetControlCaps` reports each control's
name *and* its numeric id; the driver builds the mapping from that at connect time
(`internal/device/asi/controls.go`). Hardcoding `ASI_CONTROL_TYPE` values is a trap — the enum has
grown between SDK versions, the header does not ship with the binary library, and an id that is off
by one silently drives the wrong control (asking for the cooler and getting image flip).

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
points at). The **Siril SPCC sensor/filter database** is baked into the image at a pinned commit
(`SPCC_DB_REF` build arg → `/opt/siril-spcc-database`) and symlinked by the entrypoint into Siril's
user data dir (`$XDG_DATA_HOME/siril/siril-spcc-database`) — the GUI normally downloads it on first
use, which a headless container never does, and without it `spcc` aborts even on a plate-solved image
(the colour ladder then degrades to the star-field fallback). With the local Gaia catalogues under
`library/catalogues` (`just download-catalogues`) plate-solve + colour calibration run fully offline
in the container. Known issue: the **arm64 distro Siril 1.4.4 segfaults inside SPCC's aperture
photometry** (local and online catalogues alike); the engine's colour ladder falls to **PCC** on the
same solve (`internal/postprocess/colorcal.go`), which completes fine — so arm64 containers get a
photometric balance from Gaia photometry rather than per-star spectra until upstream fixes SPCC. **StarNet++** is not baked in (licence not redistributable) — mount it +
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

### Glacier / cold storage classes

Objects can live on **cold S3 storage classes** (Glacier Instant Retrieval, Glacier Flexible
Retrieval, Deep Archive) to cut cost, and the whole S3 feature is class-aware. The model
(`internal/s3store/glacier.go`) splits classes into two families: **instant** (`STANDARD`, `*_IA`,
`INTELLIGENT_TIERING`, and — despite its name — `GLACIER_IR`), which read immediately, and
**archived** (`GLACIER`, `DEEP_ARCHIVE`), which must be **restored** (thawed, minutes → ~48 h) before a
GET or a server-side copy. Key facts the code encodes: `""` == `STANDARD`; `HEAD`/`Stat` works on an
archived object (only `GET`/`CopyObject` fail with `InvalidObjectState`); a class change is a
`CopyObject` onto the same key (ledger untouched) that **carries the `Astro-Md5` + content-type
forward**; and everything **soft-fails** on an endpoint without Glacier (MinIO) so a run is never
blocked by a missing feature.

The long thaw is modeled as a **durable, visitable Task**, not a held worker: a job waiting on a
restore parks as a `causeThaw` pause (zero workers, survives restart) and the existing 60 s
auto-resume sweep re-checks it on a 2→15 min cadence, bounded by a 48 h deadline, then finishes
automatically. This covers every S3 surface:

- **Explorer → "Change storage class"** — archive classic→Glacier, or restore Glacier→classic (thaw
  then transition), or restore-only; a per-object `tier` job on its own low-cost lane
  (`internal/job/tier.go`). Archived objects are badged and their download becomes a "Restore" action.
- **Process / download from Glacier** — a full-S3 run (or the Import-from-S3 download) whose inputs
  are archived initiates the thaw and parks; on resume it re-pulls and stacks once readable
  (`internal/job/storage.go`, the pull + low-disk stager both thaw-gate).
- **Backups** — the natural archival target: a backup can write its heavy data to a cold class (the
  manifest stays instant so the picker keeps working); a restore thaws first.
- **Library mirror** — a matched master that is archived kicks off its restore and falls back to a
  local rebuild for that run (never blocks), so a later run finds it warm.
- **Serving fallback** — a freed-then-archived preview/result replies **409 `{archived}`** instead of
  a broken image, so the UI can offer a restore.
- **Connections** — an optional per-connection **default storage class** (instant only — the pipeline's
  own control writes must stay readable; an archived default is rejected).

Retrieval tier (Standard default / Bulk / Expedited) is selectable per thaw. See
**[docs/storage-s3.md](storage-s3.md)** → "Glacier".
