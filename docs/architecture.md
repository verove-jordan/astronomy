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

Every external tool, catalogue, data service and library — with its licence and what breaks without it
— is catalogued in [third-party.md](third-party.md). Nothing is bundled: host tools are invoked, online
feeds are fetched at runtime and cached, and both soft-fail.

## Components

| Package | Responsibility |
|---------|----------------|
| `internal/config` | Environment configuration. |
| `internal/fits` | Read FITS headers + pixels — hand-rolled, no external FITS library. |
| `internal/inspect` | Walk a directory, classify each file (light/dark/flat/bias/dark-flat/video), group into sets. Bare-filename legacy captures are labeled from an `info.txt` sidecar (`manifest.go`) that lists the per-sub-run filter order + gain. |
| `internal/siril` | `SirilRunner`: generate `.ssf`, exec `siril-cli`, parse `progress:`/`log:` + `seqstat` CSV. |
| `internal/grade` | Per-frame quality metrics + rejection rules; trail handling. |
| `internal/stacknative` | The Go pixel combiner for the algorithms Siril lacks (trimmed mean, Robust Chauvenet, DSS auto-adaptive / entropy-weighted, local normalization) — streams registered frames in row bands; validated against Siril for the algorithms both implement. |
| `internal/stackalg` | The canonical catalogue of frame-combination and pixel-rejection algorithms (engine-free): what exists, which engine runs it, what its parameters mean, and the count-adaptive default. One source of truth behind the Siril clause, the knob whitelist and the UI menu — see [stacking.md](stacking.md). |
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
| `internal/skylog` | Records the sky a capture session ran under: hourly samples (weather + Moon + target altitude/airmass + sky brightness) plus a rolled-up summary, written **while the session runs** because the weather feeds have no archive. Pure `Observe`/`Summarize` behind two narrow interfaces, so it imports neither the database nor an HTTP client. |
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

Everything here is bounded by one fact: Open-Meteo's free tier weights a call by
**locations × days × variables** (10 000/day). Every design decision below exists to keep a page
view inside that budget, because the failure mode is not an error — it is silently blank layers.

**The animated cloud map** is rendered server-side as PNG tiles:
`GET /api/sky/weather/tiles/{metric}/{time}/{z}/{x}/{y}` (`internal/api/weather.go` →
`internal/weathertile`), with `GET /api/sky/weather/grid/frames` supplying the scrubber's time axis.
The browser is a plain Leaflet tile layer (`useFrameTileLayer.ts`, registered through the modular
layer registry `useMapLayers.ts`); the earlier client-side canvas renderer is gone.

Behind both endpoints is ONE cube per region: `weather.Provider.Grid` always fetches the fixed
`gridSupersetLayers` set (total cloud + the three altitude bands, humidity, precipitation chance,
dew spread — 8 variables), so the frames axis and every metric share a single upstream fetch. Tiles
past z8 fold onto their z8 ancestor block (`weathertile.regionZoomCap`) and the frames endpoint
anchors to the same quantizer, so zooming and panning do not mint new cubes. A `singleflight` group
collapses the tile burst a viewport fires, a 429 opens a **70 s breaker**, and a failed cube is
negative-memoised for 30 s. Degraded responses serve the **last cached frames** (bounded by
`staleGrace`) or a transparent tile, never a 5xx — an error body would make Leaflet retry and burn
more quota. The disk cache is **versioned** (`gridCacheVersion`), so a semantic or geometry change
ignores stale cubes instead of mis-rendering them.

**Seeing** is derived, not fetched. `internal/weather/seeing.go` computes it from the wind profile
Open-Meteo already returns (300/500/850 hPa winds, surface wind, boundary-layer depth): jet strength
plus wind shear across the layers, mapped onto an arcsecond FWHM. That is hourly and at model
resolution, where 7Timer's ASTRO feed is 3-hourly on a 10 km GFS grid — 7Timer remains the fallback,
and each hour records which source it used (`Hour.SeeingSource`).

**Weather still does not change the clear-sky visibility scores** — it is overlays, the forecast
panel, and a separate live score (`skyplan.ScoreLive`) — with one deliberate exception, below.

### Ranking dark sites for a night

The dark-sky finder answers "where should I go **on this night**", which cannot be answered from
terrain alone. `internal/darksky` therefore blends a third term into its score: the forecast for the
selected night, taking `ASTRO_DARKSKY_WEATHER_WEIGHT` (default 0.3) off the top while darkness and
horizon openness keep their 0.6/0.4 proportion in the remainder. Weight 0, or no forecast, reproduces
the historical score exactly, and a candidate with no forecast is never scored as if it had a bad one.

The cost discipline is `weather.NightScan`: a multi-point request restricted to the night's hours via
`start_hour`/`end_hour`. Ten hours instead of a forecast day is what turns a 160-point area scan from
~100 call-equivalents into ~4, which is what makes ranking next Saturday affordable at all. One search
spends exactly **two** upstream calls:

1. a coarse pass over a lattice covering the whole drawn box, applied to every surviving cell
   **before** the shortlist is cut, so a clearer-but-slightly-brighter spot can climb into it;
2. a precise pass at the finalists' exact coordinates, carrying the elevations the horizon step
   resolved (Open-Meteo downscales temperature to a supplied elevation) and the pressure-level winds
   the seeing index needs.

The night aggregate (`weather.ScoreNight`) weights each hour by how much moonlight spoils it
(`astro.MoonGlowFactor`), charges low cloud more than high cloud at the same coverage, and flags
`fog_risk`, `frost` and `above_inversion` — the last being the winter case where a summit stands above
a stratus deck whose top is estimated from the sampled valley floor plus the boundary-layer depth.
Candidates come back with their normalised sub-scores, so the UI's darkest-to-clearest slider re-ranks
what is already on screen without spending another call. `GET /api/sky/nights` supplies the picker's
twilight and Moon arithmetic (pure computation, no upstream).

## Capture conditions: the logbook

`capture_sessions`/`capture_frames` record **what** was shot. `capture_conditions` records **what sky
it was shot under** — which is half of the answer when deciding whether two nights of the same target
can be stacked together: a 60%-lit Moon 20° from the target, or a transparency that collapsed at
01:00, explains a set that will never blend.

The constraint that shapes the whole design is that **the engine can only see a forecast, not an
archive.** `weather.Provider.Forecast` asks Open-Meteo with `past_days=1&forecast_days=2`; 7Timer and
NOAA SWPC are recent-or-forward only; there is no archive endpoint anywhere in the engine. Conditions
older than about a day are simply not retrievable — so they are **sampled live, while the session
runs**, and sessions captured before this shipped can never be backfilled.

- **Cadence is hourly** (`ASTRO_CAPTURE_CONDITIONS_INTERVAL_MIN`, default 60). The feeds are
  themselves hourly, so sampling faster repeats the same numbers while spending a free-tier request
  budget a long winter night can genuinely exhaust. A sample is taken at the start, on each tick, and
  once at the end (suppressed if one just landed).
- **Half of each row is computed locally** and stays correct on a night when every feed is down:
  Moon position/illumination/phase (`astro.MoonNow`), the target's altitude, azimuth, airmass
  (clamped — `astro.Airmass` is +Inf below the horizon, which no column can hold) and its angular
  distance from the Moon. `source` (`live`/`cached`/`unavailable`) is what keeps an all-zero weather
  row readable as "the feed was down" rather than "the sky was flawless".
- **The summary is rewritten after every sample**, denormalized onto `capture_sessions`, so the
  logbook list draws one line per night without joining — and a session killed mid-night still carries
  an accurate record of the hours it did get.
- **The full hourly forecast is archived twice** (`capture_forecasts`, start and end) in its own table,
  because one payload is tens of kilobytes and the list selects every session column at once. That is
  what makes "what was forecast vs what actually happened" answerable.
- **The site comes from the browser** (`lat_deg`/`lon_deg` on the start request, from the sky store's
  picked location), falling back to `ASTRO_LAT`/`ASTRO_LON`. The engine's configured site is right at
  home and wrong on every trip to a dark sky.

Everything soft-fails: `internal/skylog` reaches weather and light pollution only through the API
layer's existing nil-safe shims (`weatherAt`, `siteAt`), every sink error is remembered for the UI and
then dropped, and a capture never fails because the logbook had trouble.

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

## The colour model: one pipeline, not two

A capture is either **monochrome** (a filter wheel, stacked per filter and combined into LRGB) or
**one-shot colour** (every light carries all three primaries). The verdict is
`inspect.Inventory.ColorModel`, decided once while scanning the folder and read by both entry
points, so the CLI and the web UI cannot disagree — they used to, and a colour folder submitted from
the UI as "deepsky" lost every frame in silence.

**Detection.** `Frame.Bayer` (a `BAYERPAT` card) alone cannot answer this: a developed DSLR raw, a
debayered RGB FITS and a colour TIFF have no Bayer pattern and are still colour. `Frame.Channels`
carries the plane count beside it, filled from `NAXIS3` for FITS and from the container header for
TIFF/PNG/JPEG, which gives three distinguishable states — mono, CFA awaiting demosaic, and RGB.
The pre-existing spurious-`BAYERPAT` veto still runs first (older ASICAP captures stamp one even on
a mono camera), so wheel evidence always beats a header artefact. Colour is part of the set key, so
a folder holding two rigs cannot merge them into one stack.

**Processing.** A colour run is the ordinary per-channel pipeline with exactly one channel, named
`RGB` (`filters.Color`). That is what makes it inherit the calibration library, frame grading, trail
masking, set QA, plate-solving, SPCC, GraXpert, StarNet, denoise, star-quality auto-fix, the
supervised finish, stage previews, per-stage rerun and the S3 paths, instead of a parallel
implementation drifting away from them. The seams are in `internal/pipeline/color.go`; see
[modes/README.md](modes/README.md#monochrome-or-colour--the-same-modes) for what differs.

**Calibration order is load-bearing.** A raw CFA mosaic is calibrated CFA-aware and demosaiced
*last* (`calibrate … -cfa -equalize_cfa -debayer`). Demosaicing first would interpolate every hot
pixel and dust shadow across its neighbours, so the defect map and the flat would be correcting a
smeared copy of the artefact rather than the artefact. Camera raws and colour stills are brought in
with Siril `convert` rather than `link`, which only symlinks FITS; monochrome runs still link, so
their scripts are byte-identical to before colour existed.

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

## Naming the stars: two catalogues, one query

A finished run can be annotated (`POST /api/jobs/{id}/stars` → `<runDir>/stars.json`): every detected
star gets a marker, and the ones the catalogue recognises get a name and a hover card. That needs a
star catalogue, and the two the engine ships are **not alternatives** — one is a floor, the other
raises it.

| | Embedded (`internal/deepstars/catalogue/hyg_mag9.csv.gz`) | Deep (`<library>/catalogues/athyg_v32.bin`) |
|---|---|---|
| Source | HYG v4.1 | **ATHYG v3.2** = Tycho-2 + Gaia DR3 + HYG's names |
| Stars | 83 479, to magnitude 9 | **2 552 164**, to about magnitude 13 |
| Size / where | 1.4 MB, `go:embed`ed, always present | ~130 MB, downloaded, gitignored, never committed |
| Extra fields | — | distance, spectral type, B−V, absolute magnitude, radial velocity |
| Installed by | nothing — it is compiled in | `just download-deepstars` |

`deepstars.Load(path)` returns the deep catalogue when the file is there and the embedded one when it
is not, so **a missing download means shallower names, never a broken feature** — CI, a fresh clone
and an offline machine all keep working unchanged.

**Why the deep one matters.** Measured on the real M42 run, going from embedded to ATHYG took the
frame from *5 named stars out of 698 detections* to *70*, with distance on 61 of them and a spectral
type on 37. The plate-solve check also stopped starving: M51 went from 2 usable check stars out of 5
to 18 out of 30, and M31 from 0 out of 4 to 13 out of 30 — both had been failing validation outright
and emitting no labels at all, purely because a magnitude-9 catalogue has almost nothing in a small
deep-sky field.

**Why it is a custom binary and not the CSV.** ATHYG ships as two ~99 MB gzipped CSVs; parsing those
into 2.5 million Go structs is ~600 MB resident, which the engine cannot spend beside Siril. So
`deepstars.Build` converts them once into a **declination-sorted, fixed-width record file** (52 bytes
per star, `format.go` owns the layout). A cone query then binary-searches the dec band with `ReadAt`
and streams only that slab: an M42-sized field costs a few hundred KB of reads and **~9 ms**, and
nothing but the small string tables (proper names, spectral types, constellations — interned, so a
record carries a 2-byte index) is ever resident. Declination is the sort key precisely because,
unlike RA, it has no wrap-around, so a band is always one contiguous range.

Two traps the builder is armoured against, both found the hard way:

- **RA is in HOURS** in ATHYG, as in HYG — ×15 to get degrees.
- **The second file has no header row.** The release splits one CSV by byte count, not by document,
  so a header-expecting parser silently eats its first star. The builder applies a hard-coded column
  order and *verifies it* against the first file's real header, so an upstream schema change fails
  the build instead of shifting every field.

Fields where zero is itself a measurement (B−V = 0 is a real A0 star; a star really can have zero
radial velocity) carry an explicit absent-sentinel rather than being encoded as 0, and a "distance"
past 100 kpc — what a negative parallax produces — is dropped, because a wrong number on the hover
card is worse than a blank one. Light years, solar luminosities and an effective temperature are
*derived at display time* (`frontend/src/utils/starInfo.ts`) from the raw catalogue values, so a
formula that turns out to be wrong is fixed in one place instead of baked into every stars.json ever
written. Licence and attribution: `docs/third-party.md`.

## The photograph as a volume: the 3D field map

A stack is a projection: everything in it is painted on one plane, whatever its real distance. Once
the deep catalogue gives a star a **parallax**, that stops being necessary — `internal/scene3d` turns
a finished run into a scene where each detected star sits along its own line of sight at its own
distance, and each catalogued object hangs at its. Measured on the real M42 run: 698 detections, 690
of them placed, spanning 73 pc to 4 kpc through a field 1.3° across.

**All of it is computed in Go, once, and cached beside the run** (`scene3d.json` 1.7 KB,
`scene3d.bin` 17.7 KB, `scene3d_bg.png` 4.3 MB). The browser fetches those, hands the binary to the
GPU untouched, and draws three calls a frame. There is no 3D library: the scene is point sprites, a
textured quad per object and some lines, so `useStarField3D.ts` is a few hundred lines of hand-written
WebGL2 and **no new npm dependency**.

### One line of shader maths

Each record carries a **unit direction** and a **distance in parsec**. The vertex shader places it:

```glsl
float t = clamp((log(dist) - uLogNear) / (uLogFar - uLogNear), 0.0, 1.0);  // log depth
float z = uZRef + t * uDepth * uZSpan;          // uDepth is the slider, 0…1
vec3 pos = aDir * (z / aDir.z);                 // slide ALONG the ray, never across it
```

At `uDepth = 0` every star lands on one plane, and because a TAN plate solution *is* a pinhole
projection, the perspective view of that plane is the photograph — exactly, to a hundredth of a pixel
(pinned by tests on both sides of the wire). Opening the slider spreads the field into a logarithmic
cone; logarithmic because a real field covers three or four decades, and placed linearly everything
past the first tenth piles onto the back plane.

The span the warp runs over has to cover **everything the scene draws** — every placed star and every
catalogued object. That is not obvious and getting it wrong is invisible in the data and glaring on
screen: the warp clamps anything outside its range onto an end plane, so a span taken from the stars
alone put M51 at 7 Mpc on the very same plane as a star at 600 pc, twelve thousand times nearer.
The range is the stars' own 1st-to-99th percentile spread, trimmed at the ends so one wild
photometric estimate cannot set the scale, widened to take in every object. On the M51 run that
gives 73 pc → 7.05 Mpc: the whole star field occupies the near 28 % of the cone and the galaxy sits
at the very back, with decade rings at 100 pc, 1 kpc, 10 kpc, 100 kpc and 1 Mpc to read the gap by.

The scene's basis comes from `annotate.Solve.Frame`: the sky positions of the final image's centre and
of its two far edge midpoints. Three points fix orientation, field of view and parity, and they are
settled in `internal/annotate` for the same reason `Label.Extent` is — that is the one place the
validated solution and the crop mapping live, so no consumer can derive the geometry a second time and
disagree. (M42 comes out `right_handed: false`: that session was shot through a star diagonal.)

### Stars as light sources

A star's size and brightness follow the inverse-square law **from wherever the camera is**, not from
Earth: `m = M + 5·log10(d/10 pc)`, computed in real parsecs and never in warped scene units. Flying
toward a star genuinely brightens and swells it; a blue supergiant reads as luminous from far off
while a red dwarf beside it stays faint. At depth 0 the camera sits at the origin — which is Earth —
so `d` is the star's own distance and `m` is its Earth magnitude: the photograph, out of the same
expression, with no special case.

"Real scale" in the literal sense is not renderable and the UI says so instead: the Sun at 100 pc
subtends 0.1 milliarcsecond, so the hover card reports the true angular size as a number (radius from
luminosity and temperature by Stefan–Boltzmann, then θ = 2R/d) and it always reads as a fraction of a
mas. That *is* the answer — a star is a point source at any distance a telescope sees it from.

Colour is computed, not sampled. `annotate`'s `starHex` deliberately lifts every colour toward white
so a marker ring stays legible, and the sampled pixel also carries the stack's colour balance and
stretch — feeding that into a 3D scene gives a field of pastel dots. Instead the star's B−V gives an
effective temperature (Ballesteros), and a blackbody at that temperature has exactly one colour:
Planck's spectrum integrated against the CIE colour-matching functions, into sRGB, normalised to a
**hue** so brightness is not counted twice. The temperature relation is the same function
`frontend/src/utils/starInfo.ts` uses for the hover card, guarded on the same B−V range, so a star's
rendered colour and the number written beside it can never disagree. On the M42 run all 690 placed
stars come out physically coloured.

### Motion: where a star will be

Proper motion is an *angle* per year and radial velocity a *speed* along the line of sight; neither is
a velocity alone. With the distance the scene already has, `v_tan = 4.74·μ(″/yr)·d(pc)` on the local
East/North axes plus the radial part gives a true space velocity, rotated into the scene basis and
stored as three `int16` at 0.1 km/s. The viewer draws it as **where the star will be after N years**
(a slider, default 100 kyr) — so the arrow's length is proportional to speed *and* means something
concrete, and pushing the slider shears the field: cluster members drift together while the field
scatters. Red is receding, blue approaching. 61 of M42's 690 stars have one.

### Three sources of depth, and one of them is a guess

| Source | How | On the M42 run |
|---|---|---|
| **Measured** | the catalogue's own parallax (Gaia DR3 / Tycho-2 via ATHYG) | 61 stars |
| **Estimated** | spectroscopic parallax from this frame's colour and magnitude | 629 stars |
| **Unknown** | neither — **not placed at all**, only counted | 8 stars |

The estimate needs a colour index, and a stack has no absolute colour scale, so the relation is
**fitted in-frame**: the identified stars pair their catalogued B−V with the colour this finish
rendered them, giving `CI ≈ a·h + b` by 2σ-clipped least squares (M42: 65 pairs, RMS 0.17). B−V then
becomes an absolute magnitude through an embedded **ZAMS table** — deliberately a standard table
rather than a fit to the frame's own stars, because any real field mixes dwarfs with giants and
fitting it would bake that contamination in. `d = 10^((m − M + 5)/5)` follows.

Its known failure is that a red giant is placed several times too close, so the layer grades itself:
every star that *does* have a parallax gets a photometric distance computed without ever consulting
it, and the manifest ships the median ratio and the scatter. M42 scores **×0.79 ±0.21 dex** — the
estimates run about 20 % close (nebular reddening pushes them there), with a factor-1.6 spread. The
UI draws estimated stars differently, counts them separately, offers one toggle to hide them, and
warns outright when a frame's own grade says they are decoration rather than data. A mono stack, or
one whose colours do not track the catalogue, gets no estimates at all and says so.

### Objects have shapes, and each says which kind it is

A billboard is honest but obviously wrong: a galaxy is not a sticker facing the camera. How much can
be done better differs sharply by object class, so the engine emits a **shape descriptor** per object
— never a mesh; a 32×32 disc is a few thousand vertices and about a dozen numbers, so the descriptor
is both the lighter wire format and the one that keeps every astrophysical decision in Go. The viewer
holds a dumb tessellator with no astronomy in it, and vertices carry `(dir, distPc, uv)` rather than a
position so the **same** depth warp the stars use applies per-vertex.

Three tiers, kept apart and labelled in the UI:

| Tier | What it means | Who gets it |
|---|---|---|
| **measured** | the geometry follows from catalogued numbers | spiral/lenticular galaxies: `cos²i = (q²−q₀²)/(1−q₀²)`, q₀ = 0.2 |
| **assumed** | the size is measured, the *form* is a standard assumption | round planetary nebulae and SNR as shells; ellipticals as oblate spheroids |
| **modelled** | no measurement of the third dimension exists at all | every diffuse nebula |

The vendored OpenNGC carries a major axis, minor axis and position angle for **10 481 galaxies** (99 %
with a PA) and 140 planetary nebulae — which is what makes the first two tiers real. Their limits are
stated rather than hidden: inclination is good to ±3–5° between 50° and 80°, degenerate near face-on,
and **which edge tilts toward us cannot be told from an ellipse at all**, so every disc is flagged
`flip_ambiguous`. An elliptical's projection genuinely does not fix its 3D shape (a face-on oblate and
an edge-on prolate can look identical), so its flattening is labelled a lower bound. A strongly
bipolar planetary nebula is *refused* a shell rather than forced into one — the flat plane is the more
honest answer.

For diffuse nebulae nothing is measured. The image records `∫ε·dz` along each line of sight, which is
one number where a function is wanted, so a shape needs an assumption. Two routes, both labelled
`modelled`:

- **A curated prior** where the structure has actually been published — `internal/scene3d/shapes.csv`,
  ~18 objects, each with a citation. M42 is the case that matters: it is a *blister* H II region, a
  cavity excavated by the Trapezium on the **near face** of OMC-1, so its bowl opens toward the
  observer. A blind inversion would give a symmetric blob — convincing, and the wrong shape.
- **The generic inversion** otherwise: depth ∝ √I under the stated assumption that a structure is
  about as deep as it is wide.

Either way the volume is rendered as ~24 alpha-blended depth slices whose per-fragment alpha is
computed from the backdrop sample, so the shape lives entirely in the fragment shader and each slice
is four vertices.

### Objects: measured from the frame where possible

Each catalogued object with a known distance becomes a quad cut from the run's own image by the
ellipse `annotate` already projected into final-image pixels, drawn additively — nebulae emit, and the
near-black sky in the cutout contributes nothing, so the ellipse self-mattes with no visible edge. The
texture is `scene3d_bg.png`: the final image **with its stars painted out** (local-median patches, or
the starless output when a StarReduce run produced one). Without that the field is drawn twice — right
at depth 0, and visibly wrong the moment the slider opens and one copy stays pinned to the object.

Distances come from an embedded table (`dsodist.csv`, ~170 objects) because the object catalogues the
app already loads have no distance column at all. But for **clusters** the frame can do better than a
lookup: histogram the member parallaxes inside the footprint in log distance, take the half-sample
mode (a cluster is a narrow peak on a background spanning decades — no bin width to choose), and
require the members to be a quarter of what is in the ellipse. That distance is a measurement from
this picture, so the manifest keeps the catalogued value beside it and labels which was used, rather
than quietly resolving a disagreement.

### The wire format

`scene3d.bin` is header | fixed-width records | name table, laid out so every attribute lands where
`gl.vertexAttribPointer` can address it — floats on multiples of 4, the 16-bit fields on 2. **32 bytes
per star** (v2: the record grew a space velocity and a two-byte index into the run's `stars.json`,
which is how a hover reads the full catalogue row without a second copy of it living in this file), uploaded as one buffer, with no parsing, no per-star objects and no garbage. Like the deep
star catalogue it is versioned and self-describing, and a reader meeting an unknown version or record
size refuses the file rather than misreading it; `internal/scene3d/format.go` and
`frontend/src/utils/scene3d.ts` are pinned to the same layout by tests on both sides that would fail
rather than let the browser draw a scrambled field.

### The Galaxy the field sits in

The **Milky Way layer** draws the Galaxy around the photograph, at true scale and orientation. It is a
point cloud of 180 000 stars sampled in Go from published structure — one binary, 1.8 MB, ten bytes a
point (`internal/scene3d/galaxycloud.go`, served by `GET /api/galaxy/points`).

It is **run-independent**: it is the Galaxy, not this photograph's Galaxy. The only per-run part is the
3×3 that rotates the galactic frame into the image's, which is a uniform — so the cloud is generated
once per process, cached in the browser for a week behind an ETag, and uploaded to the GPU once per
session however many runs are opened. Its columns are the galactic axes expressed in the scene's frame,
so the matrix carries the field's **parity** automatically: a run shot through a star diagonal draws a
chirally flipped Galaxy, which is right, because the photograph is itself a mirror.

The structure (`internal/scene3d/galaxymodel.go`) is the canonical copy; `frontend/src/utils/galaxy.ts`
is a three-scalar mirror for framing, pinned by a test. The components:

| Component | Source | Share of the mass |
|---|---|---|
| Spiral arms + H II knots | Reid et al. 2019 log-spiral loci and fitted widths | 0.077 |
| Boxy/peanut bulge | Wegg & Gerhard 2013 axis ratios, 27° bar angle | 0.20 |
| Long bar | Wegg, Gerhard & Portail 2015 | 0.035 |
| Thin + thick disc | Bland-Hawthorn & Gerhard 2016 (2.6/0.30 and 2.0/0.90 kpc) | 0.68 |
| Stellar halo | broken power law, −2.5 inside 25 kpc, −3.8 beyond | 0.008 |

Sampling is **by mass** — every point stands for the same amount of stellar mass — so the contrast
between the bulge, the arms and the inter-arm disc falls out of how many points land there rather than
out of a brightness fudge. Each point then carries its population's colour, through the same
B−V → Planck → sRGB path the run's own stars use, and its surface-brightness weight, which is where the
difference between old red bulge stars and young blue arm stars belongs. Four decisions are worth
recording:

- **The arms are continued past the fit, with the steeper of their two pitch angles.** Reid et al.
  measured each arm over a third of a turn; drawn only there, the arms form a one-sided fan and the
  Galaxy's light comes out a kiloparsec off-centre. Continued, the map is a proper spiral. The pitch
  choice is what makes it safe: Norma's *inner* pitch is −1°, so continued with that its "spiral" closes
  into a circle and the map fills with concentric rings — which is exactly what an earlier version did.
  The continuation carries 55 % of the arm's weight, so the measured stretch reads as the brighter one.
- **The arm/interarm contrast is a measured output, not an input.** The first pass put it at 12×, which
  looks like bright wires laid on a smooth disc. The arms' share is set so it comes out near 6×, where a
  grand-design spiral sits in blue light — and these arms *are* the blue population. Pinned by a test
  that fails in both directions.
- **There is a 250 pc hole around the Sun.** An honesty measure, not a rendering trick: inside it the
  scene already holds the run's own stars at distances measured or estimated from the photograph, and
  dropping invented stars in among them would mix a model into a measurement at the one scale where the
  user is looking closely. At galaxy scale the hole cannot be seen.
- **Dust is not modelled, and the UI says so.** Extinction depends on where the eye stands — the same
  cloud reddens what is behind it and leaves what is in front alone — so a darkening pass would dim the
  near side of the bulge as readily as the far side. Doing it properly needs a 3D dust map and a
  per-frame integration along every line of sight. What is drawn instead is the density contrast itself,
  which is a fact.

Each point is drawn at **the angle the patch of Galaxy it stands for actually subtends**, which is one
line and is what keeps the cloud's surface brightness independent of where the eye is: pull back and
each sprite shrinks as 1/d while four times as many crowd into the same screen area. Past the model's
own resolution — one point standing for a tenth of the frame — the cloud fades out rather than drawing a
blob the size of the field, which is why the start of the journey is the photograph and the run's own
measured stars, with no model mixed in. (Measured against the *frame*: an earlier version faded at six
pixels and blanked the Galaxy over most of the slider, where a point is a perfectly reasonable sixty.)

**And the cloud is stretched, for exactly the reason every astronomical image is.** It accumulates
linearly into an RGBA16F framebuffer, because adding light is what additive blending does; but a galaxy's
bulge outruns its outer disc by more than a hundred to one and eight bits of screen cannot hold that.
Drawn straight, the core came out a featureless white ellipse with the arms barely above black, and a
fifth of the frame was clipped by the time the camera reached the disc. A second pass tone-maps the
buffer — Reinhard on **luminance** with a 2.2 gamma, the colour scaled by the ratio so the blue arms stay
blue and the bulge stays gold, and a cap on how far any one pixel may be lifted so the halo does not read
as grain. Measured on the real Orion field's orientation at the galactic vantage: clipping fell from
2.5 % of the frame to 0.4 % while the mean brightness *rose*, which is what a stretch is. The bar becomes
visible as a distinct ellipse at its 27°; the arms resolve into blue knots and pink H II beads.

The stretch applies to the cloud alone. The run's own stars are drawn afterwards, straight to the screen:
their brightness is physical — the inverse-square law from wherever the camera is — and stretching it
would break the contract that depth 0 is the photograph. A context without `EXT_color_buffer_float` skips
the pass and draws the cloud direct, which clips the core but is never broken.

### Zooming out: the journey, and how far it may go

The galaxy slider is a journey with two legs (`frontend/src/utils/scene3dgalaxy.ts`). It moves the
camera **and opens the lens**, and it has to do both: the run's own field is a degree across, and
framing a forty-kiloparsec disc through a one-degree lens would put the camera two megaparsecs away.

- **Leg one** runs from Earth to a vantage 35 kpc out and 55° above the galactic plane. That elevation
  is built in the *galactic* frame and only then expressed as scene angles — ramping the scene's own
  pitch to 55° once put the camera 16° *below* the plane, and a spiral 16° from edge-on is a set of
  nested arcs with no visible disc.
- **Leg two exists when the run caught something outside the Galaxy**, and it keeps going until that
  object and the Milky Way are in one frame at true relative scale. On the M51 run the slider spans
  8.6 pc → 12.07 Mpc, passing the whole Galaxy at t = 0.44. An M42-style run has nothing out there and
  its journey ends at the disc, unchanged.

Two things about leg two were bugs first. **The manual zoom-out ceiling is now scale-aware**: one fixed
400 units cannot serve both spaces — the warped view is five units deep, and a galaxy at 7 Mpc needs
seven thousand just to be in front of the lens. And **the pivot tracks the framing, not the slider**:
easing it linearly toward the midpoint while the camera pulled back logarithmically put it 360 kpc down
the line of sight with the camera 62 kpc behind and 45 kpc of frame, so the middle of the slider showed
empty space with the Galaxy off screen and M51 still megaparsecs away.

The same needle problem breaks the plain field view, and for the same reason: a field's cone is a
hundred parsecs across and four thousand deep — 87:1 on the real M42 run — so viewing it from the side
through the lens that photographed its tip shows a sliver of the middle. **"Fly out" therefore opens the
lens as well**, by a factor computed from the cone's bounding radius and where the eye is standing, and
the reset button winds everything — depth, lens and journey — back to the photograph.

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
