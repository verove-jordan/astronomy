# Third-party software, data and services

Everything AstroStack uses that it did not write. The engine is a thin orchestrator: almost all of the
astronomy — the stacking, the catalogues, the sky brightness, the weather — comes from someone else's
work, and this page is the record of whose.

AstroStack itself is **MIT** (`LICENSE`). That covers only the code in this repository. Nothing here is
vendored into the binary except the small embedded data files listed under
[Embedded data](#embedded-data-compiled-into-the-binary); every tool is **invoked**, never bundled, and
every online source is fetched at runtime under its own terms.

**Two obligations to keep in mind if you ever redistribute or commercialise this:**

- **Open-Meteo's free tier is non-commercial**, and its data is CC BY 4.0 — the attribution is rendered
  in the UI (`darksky.weather.attribution`, and per-layer strings in `useMapLayers.ts`). A commercial
  deployment needs their paid API; the endpoint shape is identical, so only the URL and a key change.
- **Several catalogues are CC BY-SA**, which is a share-alike licence on the *data*, not the code:
  HYG, ATHYG and OpenNGC. Keep their attribution with any redistributed copy.

Nothing here is a hard dependency in the sense of blocking a run: every online feed **soft-fails** to a
cache, an offline atlas, a local computation or a documented default, and every optional host tool
falls back to the Siril/GIMP path. See [planner.md](planner.md) and
[architecture.md](architecture.md).

---

## Host software the engine drives

Invoked as external processes (`os/exec`) or over a local socket. None is redistributed; you install
them yourself, under their own licence.

| Tool | Role | Licence | Config |
|---|---|---|---|
| **Siril** 1.4.x | The stacking engine: calibration, registration, stacking, plate-solving, SPCC. Driven with generated `.ssf` scripts. | GPL-3.0 | `SIRIL_BIN` |
| **GIMP** 2.10 | The finish compositor, driven through the vendored MCP or `gimp-console` batch. | GPL-3.0 | `GIMP_BIN`, `GIMP_HOST`/`GIMP_PORT` |
| **ffmpeg** / **ffprobe** | Video frame extraction (planetary/lunar/solar) and video output. `ffprobe` detects >8-bit sources so extraction keeps 16 bits. | LGPL/GPL depending on build | `FFMPEG_BIN`, `FFPROBE_BIN` |
| **GraXpert** | *Optional.* AI background-gradient extraction and denoise. Absent → Siril `subsky`. | see upstream (graxpert.com) | `GRAXPERT_BIN`, `GRAXPERT_URL` |
| **StarNet++ v2** | *Optional.* Star removal for star-reduced finishing. Absent → full stars. **Licence is not redistributable**, which is why the Docker engine image does not bake it in. | see upstream (starnetastro.com) | `STARNET_BIN` |
| **LibRaw** (`dcraw_emu`) | Preferred camera-raw → 16-bit TIFF developer (photometric, exact sRGB). macOS `sips` is the fallback. | LGPL-2.1 / CDDL | (PATH) |
| **GDAL** (`gdalbuildvrt`, `gdalwarp`, `gdalinfo`) | Streams and reprojects the ETH canopy COGs for the in-app "download canopy for this area" build. | MIT/X | (PATH) |
| **`sirilpy`** (+ a Python 3.12 venv) | Siril's *own* scripting module, which Siril requires before it will plate-solve or run SPCC. Not something AstroStack calls — but if it is missing, Siril fails with "Python version check failed" and colour calibration silently degrades. Siril installs it under `<work>/.local/share/siril/.python_module`; a hand-built venv may be needed on macOS. | GPL (with Siril) | — |
| **PostgreSQL** 16 | The only stateful service. Imaging data only — no sky datum is ever persisted. | PostgreSQL License | `DATABASE_URL` |
| **Docker / Compose** | Runs Postgres, the frontend image, and the full-container `stack` profile. | Apache-2.0 | — |
| **Go** 1.23.3 (pinned, `GOTOOLCHAIN=local`) · **Node 22** / **pnpm** | Build toolchains. | BSD-3-Clause · MIT | — |

**Vendored, not authored here:** `mcp-servers/gimp/server.py` — the GIMP MCP server. It is the one
piece of Python in the repository and is copied as-is, because GIMP's own scripting is Python/Scheme.
Do not add new Python; see `CLAUDE.md`.

**Container base images** (`compose.yaml`, `docker/*.Dockerfile`): `postgres:16-alpine`,
`adminer:4`, `node:22-alpine`, `nginx:1.27-alpine`, `golang:1.23-bookworm`, `ubuntu:24.04`, and
`ollama/ollama` for the optional Linux+GPU model profile. The engine image additionally installs
Siril (x86_64 AppImage from free-astro.org, or the `ppa:lock042/siril` build on arm64), GIMP, ffmpeg,
GDAL, LibRaw and GraXpert.

### Optional local AI

The finish supervisor and the AstroAgent chat drive a **host-run, OpenAI-compatible** model server —
they never call a hosted API and no image leaves the machine. Default on macOS is
`mlx-community/Qwen2.5-VL-32B-Instruct-6bit` served by **mlx-vlm** (`just run-ia-model`, ~26 GB on
first download); on Linux+GPU the `ai` Compose profile serves it through **Ollama**. LM Studio is a
drop-in alternative. An empty or unreachable `ASTRO_LLM_URL` means the normal single-pass finish runs
— the feature is opt-in and soft-fails.

---

## Online data services

All keyless by default, all cached on disk, all soft-failing. Anything that takes a key reads it
**server-side only** — never sent to the browser, never logged.

### Weather and atmosphere

| Service | Used for | Terms |
|---|---|---|
| **Open-Meteo Forecast** `api.open-meteo.com/v1/forecast` | The backbone hourly forecast: cloud layers, humidity, dew point, temperature, wind, 300/500/850 hPa winds, boundary-layer depth, CAPE, visibility, precipitation chance. Also the multi-point night scans behind the dark-sky ranking. | **CC BY 4.0, free tier non-commercial.** Weighted quota: `locations × days × variables`, 10 000/day. |
| **Open-Meteo Air Quality** `air-quality-api.open-meteo.com` | Aerosol optical depth → transparency when 7Timer is unavailable. | as above |
| **Open-Meteo Ensemble** `ensemble-api.open-meteo.com/v1/ensemble` | Forecast confidence: how many of ICON-EU's 40 members call a night clear. | as above |
| **Open-Meteo Elevation** `api.open-meteo.com/v1/elevation` | Terrain elevation for the horizon-openness ring and per-spot temperature downscaling. | as above |
| **7Timer! ASTRO** `www.7timer.info/bin/api.pl` | Seeing and transparency indices — now the *fallback*, since seeing is derived from Open-Meteo's wind profile (3-hourly, 10 km GFS vs hourly at model resolution). | free, keyless |
| **NOAA SWPC** `services.swpc.noaa.gov` | Planetary Kp → aurora likelihood. | US Government, public domain |
| **RainViewer** `api.rainviewer.com` | Live rain-radar and satellite-IR tiles (observations, not forecast). Fetched directly by the browser; capped at native z7. | free tier, attribution required |

### Sky brightness, terrain and routing

| Service | Used for | Terms |
|---|---|---|
| **NASA GIBS — VIIRS Black Marble** `gibs.earthdata.nasa.gov` | Keyless night-lights tiles: the light-pollution map overlay and the fallback SQM sampler. | NASA open data |
| **David Lorenz light-pollution model** `djlorenz.github.io/astronomy` | The offline light-pollution atlas built by "download this area" — a modelled sky-brightness product, far better than 8-bit night-lights for SQM. | © David Lorenz; see the site's terms. Credited in the CLI output. |
| **ETH Global Canopy Height 10 m (2020)** `libdrive.ethz.ch` | Tree/forest canopy height COGs → the near-field tree horizon (a 20 m treeline at 30 m blocks ~34° of sky). Streamed via GDAL `/vsicurl/`. | ETH Zürich research dataset; see upstream |
| **OSRM demo server** `router.project-osrm.org` | By-road driving distance and time to each dark-site candidate. Display-only — it never changes the ranking. | public demo, no SLA; run your own for real use |
| **Nominatim (OpenStreetMap)** `nominatim.openstreetmap.org` | Geocoding the site picker's search box. | ODbL; respect the [usage policy](https://operations.osmfoundation.org/policies/nominatim/) |
| **CARTO basemaps** `basemaps.cartocdn.com` | The dark base map under every Leaflet view. | © OpenStreetMap contributors, © CARTO — attribution rendered on the map |

### Astronomical catalogues and ephemerides

| Source | Used for | Licence |
|---|---|---|
| **Gaia DR3** (Siril's extracts, Zenodo records `14692304` / `14738271`) | Offline plate-solving (~1.1 GB → ~3 GB) and optionally SPCC colour calibration (~5 GB). `just download-catalogues[-spcc]`. | ESA/Gaia/DPAC — attribution required |
| **Siril SPCC database** `gitlab.com/free-astro/siril-spcc-database` | Sensor/filter spectral responses for colour calibration. Baked into the engine image. | GPL (with Siril) |
| **HYG Database v4.1** (David Nash / astronexus.com) | The embedded mag ≤ 9 star extract for name annotation, and the `/goto` sky map. | **CC BY-SA 4.0** |
| **ATHYG v3.2** (astronexus) | The deep star catalogue — ~2.5 M stars to about mag 13, so an eleventh-magnitude detection gets a name instead of nothing. Adds distance, spectral type, colour index, absolute magnitude and radial velocity. Downloaded and converted by `astrostack deepstars-athyg` into `<library>/catalogues/athyg_v32.bin` (~130 MB, **never committed**, beside the Gaia files); absent → the embedded mag ≤ 9 extract, which is shallower but never broken. | **CC BY-SA 4.0** |
| **OpenNGC** (Mattia Verga) | Morphological type, size, surface brightness and common names overlaid onto the NGC/IC records — what makes NGC 6946 read as "Fireworks Galaxy, galaxy" instead of "other". | **CC BY-SA 4.0** |
| **JPL Solar System Dynamics — approximate positions of the major planets** (Standish) | The Keplerian element table the 3-D solar-system page's orbits come from: six elements and six rates per planet, fitted for 1800–2050, arcminute-class over that span. Compiled in (`internal/astro/heliocentric.go`), no download. | Public domain (NASA/JPL-Caltech) |
| **IAU/IAG WGCCRE 2015 rotational elements** (Archinal et al.) | Every body's pole direction and prime-meridian rate — what gives each world its real axial tilt and rotation angle. Compiled in (`internal/solarsystem/planets.go`). | Published report |
| **Solar System Scope planetary textures** | The surface maps the 3-D solar-system page draws its worlds with. **Downloaded, never vendored** — `just download-planet-textures` fetches them into `<work>/solarsystem` (~20 MB at 2k). Absent → every body is shaded procedurally, so nothing breaks without them. | **CC BY 4.0 — attribution required**, and the page renders it in its legend |
| **Messier / NGC / IC / Sharpless / LDN** (Siril's bundled catalogue) | Deep-sky coordinates, sizes and magnitudes for the Tonight planner and name→coordinate resolution. | public-domain astronomical data, shipped with GPL Siril |
| **Stellarium** western skyculture | Constellation lines and names for the sky map. | GPL |
| **Minor Planet Center** `minorplanetcenter.net/iau/MPCORB/CometEls.txt` | Comet orbital elements for the events calendar. | IAU MPC, free use |
| **CelesTrak** `celestrak.org` | TLEs for ISS and bright-satellite transits (capped to a ~10-day horizon — TLEs decay). | free use, attribution |
| **Aladin Lite v3 + CDS surveys** `aladin.cds.unistra.fr` | The optional sky-image viewer (DSS2 colour). Loaded on demand from the CDS CDN. | CDS, Université de Strasbourg |
| **SkyWatcher / Celestron hand-controller star lists** | The GoTo alignment helper's HC-exact star names. Transcribed from the manufacturers' published alignment-star lists (see `internal/align/starlists/README.md`). | manufacturer documentation |

### Embedded data (compiled into the binary)

These are the only external data files inside the executable. All are small, all carry their
provenance and licence in a sibling `README.md`, and all are refreshable with a `just` recipe.

| File | Contents | Licence |
|---|---|---|
| `internal/deepstars/catalogue/hyg_mag9.csv.gz` (1.4 MB) | 83 479 stars at mag ≤ 9 | CC BY-SA 4.0 (HYG) |
| `internal/skycat/catalogue/*.csv` (~1 MB) | Messier, NGC, IC, Sh2, LDN + the OpenNGC type overlay | public domain + CC BY-SA 4.0 (OpenNGC) |
| `internal/align/starlists/*.csv` | Celestron (82) and SynScan (~148) alignment-star lists | manufacturer documentation |
| `internal/skyevents/data/meteor_showers.json` | Annual meteor-shower table | public-domain astronomical data |
| `frontend/src/assets/skymap.json` | Sky-map stars + constellation lines | CC BY-SA (HYG) + GPL (Stellarium) |

---

## Libraries

### Go

Direct dependencies only; the full transitive set with resolved licences is `go.mod` + `go.sum`. The
project deliberately runs a **small dependency surface** — a pinned Go 1.23.3 toolchain means new deps
must be old-compatible, so most astronomy is hand-rolled in `internal/astro` rather than pulled in.

| Module | Role | Licence |
|---|---|---|
| `github.com/jackc/pgx/v5` | PostgreSQL driver (raw pgx, no ORM) | MIT |
| `github.com/minio/minio-go/v7` | S3 client | Apache-2.0 |
| `github.com/soniakeys/meeus/v3` + `/unit` | Eclipses, moon phases, solstices, perigee | MIT |
| `github.com/joshuaferrara/go-satellite` | SGP4 satellite propagation | BSD-2-Clause |
| `github.com/klauspost/compress` | gzip HTTP middleware | Apache-2.0 |
| `github.com/ebitengine/purego` | cgo-free loading of the ZWO camera/wheel SDKs | Apache-2.0 |
| `go.bug.st/serial` | Serial link to the Celestron hand controller | BSD-3-Clause |
| `golang.org/x/image` · `x/sync` · `x/sys` | TIFF codecs · `singleflight`/`errgroup` · syscalls | BSD-3-Clause |
| `github.com/stretchr/testify` | Test assertions | MIT |

Notably **not** a dependency: FITS I/O. `internal/fits` implements the header and pixel reader by
hand, as do `internal/astro` (positional astronomy), `internal/geogrid` (EPSG:4326 raster sampling)
and `internal/lightpollution/atlas.go` — no GIS or FITS library is linked in.

### Frontend

| Package | Role | Licence |
|---|---|---|
| `vue` · `vue-router` · `pinia` · `vue-i18n` | The app framework, routing, state, i18n (en + fr) | MIT |
| `leaflet` (+ `@types/leaflet`) | Every map surface | BSD-2-Clause |
| `echarts` + `vue-echarts` | Altitude curves, timelines, charts | Apache-2.0 |
| `markdown-it` | Renders AstroAgent replies | MIT |
| `tz-lookup` | Offline lat/lon → IANA timezone | CC0-1.0 |
| `tailwindcss` · `postcss` · `autoprefixer` | Styling | MIT |
| `vite` · `typescript` · `vue-tsc` · `vitest` · `@vue/test-utils` · `happy-dom` · `@pinia/testing` · `prettier` | Build and test toolchain | MIT (TypeScript: Apache-2.0) |

---

## Status notes

Kept here so the table above never quietly drifts from reality:

- **The two star catalogues are not alternatives.** The embedded mag ≤ 9 HYG extract is compiled in
  and always present; ATHYG is an opt-in download that deepens it to ~mag 13. `deepstars.Load` falls
  back to the embedded set whenever the `.bin` is absent or unreadable, so a missing download means
  shallower names, never a broken annotation.
- **`ASTRO_LIGHTPOLLUTION_API_URL`/`_KEY`** are blank by default — an optional *calibrated* SQM
  provider (lightpollutionmap.info-style). Without it the chain is offline atlas → GIBS VIIRS →
  configurable default, all keyless.
- **`ASTRO_WEATHER_METEOBLUE_KEY`** exists in config but is unused; it is reserved for a future paid
  satellite layer. meteoblue's free API allows roughly 1 250 calls total, which cannot support grid
  scanning.
- **`ASTRO_CANOPY_TILE_URL`** has no default: no keyless tree-cover tile source has been verified.
  Canopy data comes from the ETH atlas download instead.

## Keeping this current

Adding an external dependency means adding a row here. To re-derive the tables:

```sh
grep -rhoE 'https://[^"`) ]+' internal/ cmd/ scripts/ justfile | sed -E 's#(https://[^/]+).*#\1#' | sort -u
go list -m -f '{{.Path}} {{.Dir}}' all     # then read each module's LICENSE
python3 -c "import json;d=json.load(open('frontend/package.json'));print(*d['dependencies'])"
```

Per-source detail lives next to the code: `internal/skycat/catalogue/README.md`,
`internal/deepstars/catalogue/README.md`, `internal/align/starlists/README.md`, and the provider
package doc comments (`internal/weather`, `internal/lightpollution`, `internal/canopy`,
`internal/routing`). Every environment variable is in [configuration.md](configuration.md) and
`.env.example`.
