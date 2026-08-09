# Getting started

From a fresh clone to your first stacked image. Written for someone who has never seen this project
— if a step assumes something you do not have, it says so.

If you only want the short version, the [README quickstart](../README.md#quickstart) is the same
path without the explanations.

---

## 1. Choose how you will run it

There are two ways, and they are genuinely different.

| | **Host mode** (recommended on macOS) | **Container mode** (`just stack`) |
|---|---|---|
| What runs where | The Go engine runs on your Mac and drives your own Siril/GIMP; Docker provides Postgres only | Everything in Docker, with Linux Siril/GIMP/GraXpert baked into the image |
| You must install | Go, Node/pnpm, Siril, ffmpeg (+ optionally GIMP) | Docker and `just`, nothing else |
| Best for | Daily work on a Mac — faster, and it uses the tools you already have | Linux, a server, or "just make it run" |
| Ports | API 8080, UI 5173 | API 8080, UI 8082 |

Siril and GIMP are desktop applications that cannot run in a Linux container on macOS, which is why
host mode exists at all. It is a deliberate exception to this project's otherwise
everything-in-Docker rule — see
[architecture.md](architecture.md#deliberate-deviation).

**Container mode is two commands.** If that is what you want:

```bash
cp .env.example .env
just stack            # builds the images the first time — this takes a while
```

Then open <http://localhost:8082> and skip to [§5](#5-your-first-run).

The rest of this page covers host mode.

---

## 2. Install the prerequisites

```bash
just --version        # if this fails, install just first: https://github.com/casey/just
docker info           # must print a server section — Docker Desktop has to be RUNNING
go version            # 1.23 or newer
node --version        # 20 or newer
pnpm --version        # `corepack enable` if missing
```

Then the astronomy tools:

```bash
brew install --cask siril     # required — it does the stacking
brew install ffmpeg           # required for video output and the demo/screenshot tools
brew install --cask gimp      # recommended — it does the layered finish
brew install libraw           # recommended — develops DSLR/phone raw files
```

Things worth knowing before you hit them:

- **`just setup` is macOS-flavoured.** It installs `golangci-lint` with Homebrew and assumes `pnpm`
  is already on your PATH. On Linux, install both yourself and skip `just setup`.
- **Siril is not checked at startup.** The engine starts fine without it and prints one warning to
  stderr; the failure shows up when your first job runs. If you installed Siril somewhere other
  than `/Applications/Siril.app`, set `SIRIL_BIN` in `.env`.
- **GIMP is optional but visible.** Without it the finish falls back to Siril's simpler
  composition — the run succeeds, the picture is just less good.

Optional extras, all soft-fail (missing → a warning and a fallback, never a failed run):
[GraXpert](https://www.graxpert.com) for AI gradient removal and denoise,
[StarNet++ v2](https://www.starnetastro.com) for star reduction, and a local vision model for the
[finish supervisor](agent.md).

---

## 3. Configure

```bash
cp .env.example .env
```

`.env.example` is heavily commented; you can run with it unchanged. The one setting worth looking at
now is where your captures live:

```bash
ASTRO_DATA_DIR=./input      # the only folder the web UI may browse
```

**None of the data directories exist in a fresh clone** — `input/`, `output/`, `work/` and
`library/` are all git-ignored. The engine creates the ones it writes to; the input root is the one
you have to make yourself, because only you know what goes in it:

```bash
mkdir -p input
cp -r /path/to/your/captures input/M31
```

No sample dataset ships with the project (astrophotography frames are hundreds of megabytes each).
Use your own — any folder of FITS, or a folder of DSLR raws, will do.

---

## 4. Start it

Two one-off commands, then two that stay running in their own terminals:

```bash
just setup      # once: Go deps, dev tools, the Siril MCP binary, frontend deps
just up         # starts Postgres in Docker (leave it running)

just dev        # terminal 1 — the API on :8080 (this is what drives Siril and GIMP)
just web        # terminal 2 — the web UI on :5173
```

`just dev` should print that it is listening on `:8080`. It applies database migrations itself on
boot, so there is no separate migrate step (`just migrate` exists and is harmless, just redundant).

Open <http://localhost:5173>. You should land on **Processing → Import**.

Every page has a **help** button next to its title that opens a short guided tour of that page. It
is worth two minutes on the Import page before continuing.

---

## 5. Your first run

### What a capture folder should look like

AstroStack works out what your files are rather than making you tell it. It reads, in order of
authority: the FITS headers, then file and folder names, then — if those say nothing — the pixels
themselves. Any of these layouts work:

```
input/M31/                          input/M31/                       input/rosette/
  Light_L_001.fits                    lights/                          DSC_0001.NEF
  Light_L_002.fits                    darks/                           DSC_0002.NEF
  Dark_001.fits                       flats/                           darks/
  Flat_L_001.fits                     bias/                              DSC_0100.NEF
```

- **Monochrome + filter wheel** — the filter must be discoverable: in the `FILTER` header, in the
  file name (`..._Ha_...`), or in a parent folder. If it is in none of them, AstroStack infers the
  filters from the signal and shows you its confidence so you can correct it.
- **Colour (one-shot colour)** — a Nikon/Canon/Sony raw, a colour camera's Bayer FITS, or plain
  colour TIFF/JPEG. Nothing to configure: it is detected during inspection and stacked as a single
  colour channel, and the Import page shows a **One-shot colour** badge so you can confirm.
- **Older captures with bare filenames** can carry a hand-written `info.txt` listing the capture
  order — see [calibration.md](calibration.md).

Calibration frames are optional. Without darks and flats the stack is noisier and keeps its dust
shadows, but it works.

### Launching

1. **Processing → Import**, browse to your folder, select it, press **Inspect**.
2. Read the inventory: frame counts per type, per-filter integration, and any warnings. If this
   looks wrong, everything downstream will be wrong — fix it here.
3. Pick a **preset** matching the subject (galaxy, star cluster, emission nebula, …) or just leave
   the defaults.
4. **Run pipeline.**

You are redirected to the job page, which streams progress: the current step, the CPU and memory of
whatever tool is running, a rolling log, and a preview after each milestone.

**It takes a while.** A few dozen frames is minutes; several hundred across multiple nights is
tens of minutes to hours. The live CPU figure is how you tell "working" from "stuck".

---

## 6. How to tell it worked

Results land in `output/<object>/<timestamp>/`:

```
output/M31/20260809_213000/
  final.xcf              layered GIMP composite (if GIMP is installed)
  final.tif  final.png   flattened results
  master_L.fits  …       the stacked master per channel
  run.json               everything about the run: parameters, per-frame stats, warnings
  previews/              the milestone previews the job page showed
```

`run.json` is the durable record — the **Runs** page re-renders any run from it, even after the
database is reset.

Read the **warnings** on the job page even when the image looks fine. They are where the honest bad
news lives: a channel that lost most of its frames to grading, calibration masters that did not
match, or colour calibration that could not run because the field would not plate-solve.

---

## 7. When it does not work

| Symptom | Cause | Fix |
|---|---|---|
| `just dev` exits with "the API needs Postgres" | Postgres is not running | `just up` (and check Docker Desktop is started) |
| The UI loads but the first job fails immediately | Siril not found | `brew install --cask siril`, or set `SIRIL_BIN` in `.env`. The startup warning is on stderr, easy to miss under `just dev` |
| The file browser shows nothing | `ASTRO_DATA_DIR` points at a folder that does not exist, or has no captures | `mkdir -p input` and put a capture there |
| Colour frames vanish from the inventory | The folder holds monochrome AND colour lights | One run cannot stack both — split them into separate folders. The inventory warns about this |
| Plate-solving and colour calibration silently do nothing | The offline star catalogues are not downloaded | `just download-catalogues` (~3 GB; add `just download-catalogues-spcc` for photometric colour calibration, ~5 GB more). Without them these steps degrade quietly rather than failing |
| "port already in use" | Something else holds 5432, 8080, 5173 or 8082 | Override `POSTGRES_PORT`, `API_ADDR`, `WEB_PORT` or `WEB_PORT_PROD` in `.env` |
| `just setup` fails at `golangci-lint` or `pnpm` | Not on macOS, or pnpm missing | Install both by hand; the rest of `just setup` is `go mod download` and `pnpm install` |
| A `just gitnexus-*` recipe fails | Those are author-only code-graph tools | Ignore them |
| Jobs die on large stacks | Too many concurrent workers for the RAM | Set `ASTRO_MAX_WORKERS=3` in `.env` |

---

## 8. Where to go next

- [ui.md](ui.md) — every page of the web UI, and what each is for
- [pipeline.md](pipeline.md) — what actually happens between "Run" and the final image
- [modes/](modes/README.md) — the eight processing modes in depth
- [calibration.md](calibration.md) — the master library, and how to shoot calibration frames
- [configuration.md](configuration.md) — every environment variable
- [mount.md](mount.md) — connecting a Celestron mount, and polar alignment from the camera
- [verification.md](verification.md) — how to check a change did what it claimed
