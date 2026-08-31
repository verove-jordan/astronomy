# AstroStack task runner. Thin wrappers only.
# NOTE (host-engine exception, see CLAUDE.md): the Go engine, the Siril MCP and the Go tests
# run on the HOST (they drive host Siril/GIMP). Docker Compose runs Postgres (+ tools/web) only.

set dotenv-load := true
set shell := ["bash", "-uc"]
set positional-arguments := true

bin := "./bin"

# List available recipes.
default:
    @just --list

# First-run setup: .env, Go deps, dev tools, MCP binary, frontend deps. Idempotent.
setup:
    @test -f .env || cp .env.example .env
    go mod download
    @command -v golangci-lint >/dev/null || brew install golangci-lint
    @command -v air >/dev/null || go install github.com/air-verse/air@latest
    just build-mcp
    @test -d frontend/node_modules || (cd frontend && pnpm install)
    @echo "Setup done. Next: just up && just migrate, then just dev + just web"

# Start Postgres (compose).
up:
    docker compose up -d db

# Start Postgres + Adminer DB UI (http://localhost:${ADMINER_PORT:-8081}).
tools:
    docker compose --profile tools up -d

# Build + start the production frontend image (http://localhost:${WEB_PORT_PROD:-8082}).
web-prod:
    docker compose --profile web up -d --build frontend

# --- Full-container stack (everything in Docker; portable to a Linux server) -------------------------
# The AI model is decoupled and opt-in: `stack` never pulls or runs it. Bring it up later with `ai-up`
# (container, Linux+GPU) or `run-ia-model` (native mlx, macOS).

# Build the engine + frontend images (no model — Ollama is a pulled image, not built).
stack-build:
    GIT_DESCRIBE=$(git describe --tags --always --dirty) BUILD_TIME=$(date -u +%Y-%m-%dT%H:%MZ) docker compose --profile stack build

# Run the whole app in containers WITHOUT the model — db + engine + frontend (UI :${WEB_PORT_PROD:-8082}, API :${ENGINE_PORT:-8080}).
stack:
    GIT_DESCRIBE=$(git describe --tags --always --dirty) BUILD_TIME=$(date -u +%Y-%m-%dT%H:%MZ) API_UPSTREAM=engine:8080 docker compose --profile stack up -d --build

# Run the whole app in containers WITH the model (Linux+GPU; needs nvidia-container-toolkit). Then: just ai-pull
stack-ai:
    API_UPSTREAM=engine:8080 docker compose --profile stack --profile ai up -d --build

# Stop the containerized app services (engine + frontend + ai); leaves Postgres running.
stack-down:
    docker compose --profile stack --profile ai stop engine frontend ai

# Tail the containerized engine logs.
stack-logs:
    docker compose --profile stack logs -f engine

# Open a shell inside the running engine container.
engine-sh:
    docker compose --profile stack exec engine bash

# Start ONLY the Ollama model container, decoupled from the stack (Linux+GPU). Then run `just ai-pull`.
ai-up:
    docker compose --profile ai up -d ai

# Pull the vision-model weights into the running ai container (explicit, ~heavy). Uses ASTRO_LLM_MODEL.
ai-pull:
    docker compose --profile ai exec ai ollama pull "${ASTRO_LLM_MODEL:-qwen2.5vl:32b}"

# Stop the model container.
ai-down:
    docker compose --profile ai stop ai

# Stop the compose stack.
down:
    docker compose down

# Apply database migrations (host engine -> compose Postgres).
migrate:
    go run ./cmd/astrostack migrate up

# Roll back the last migration.
migrate-down:
    go run ./cmd/astrostack migrate down

# Run the API server on the host with hot reload (drives host Siril/GIMP).
dev:
    air

# Run the frontend dev server on the host (Vite).
web:
    cd frontend && pnpm dev

# It runs as its OWN process so `just dev` — which restarts the engine on every source save — can
# never drop a USB connection mid-sequence.
#
# Re-running this RESTARTS it: any device server already on the port is stopped first, and waited
# for, so a stray sidecar from an earlier session is never left blocking the start.
#
# Device server (simulator, or hardware with a native SDK): camera / filter wheel / mount.
device:
    @scripts/device-stop.sh
    go run ./cmd/astrostack device

# ZWO ship no arm64 macOS library — their SDK, and their own ASIStudio, are x86_64 only. A native
# arm64 process therefore cannot dlopen libASICamera2. Building JUST this sidecar as x86_64 and
# letting Rosetta run it solves that: the engine, the frontend and every bit of stacking stay native
# arm64 and talk to it over HTTP exactly as before. This is why device I/O lives in its own process.
#
# Re-running this RESTARTS it, and the running server is stopped only AFTER the build succeeds —
# a compile error must not leave you with the working sidecar killed and nothing in its place.
#
# Device server built as x86_64, for real ZWO hardware on an Apple-Silicon Mac.
device-x86:
    @command -v arch >/dev/null && arch -x86_64 /usr/bin/true 2>/dev/null || \
        (echo "Rosetta 2 is not installed — run: softwareupdate --install-rosetta" && exit 1)
    GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -o bin/astrostack-x86 ./cmd/astrostack
    @scripts/device-stop.sh
    ./bin/astrostack-x86 device

device-status:
    @curl -fsS "http://${ASTRO_DEVICE_ADDR:-127.0.0.1:8084}/health" && echo " OK"

# The Celestron hand controller's mini-USB port is a Prolific bridge, and macOS ships no driver for
# it — so "no port" can mean an absent extension, a discontinued chip, an unpowered mount or a
# charge-only cable, and only the USB bus can tell them apart. Run this FIRST, before blaming a cable.
#
# Diagnose the Mac -> hand-controller link (add PROBE=1 to also open the port and ask the mount).
mount-doctor:
    @go run ./cmd/astrostack mount doctor {{ if env_var_or_default("PROBE", "") != "" { "-probe" } else { "" } }}

# Stop `just device` first: macOS gives a serial port to one process at a time.
#
# Connect, identify the mount, and time 500 echoes.
mount-probe:
    go run ./cmd/astrostack mount probe

# MOTION=nudge adds ±10" out-and-back moves and an hourly deadman check; the default never moves
# anything. Writes a PASS/FAIL report you can read in the morning.
#
# The overnight endurance run: `just mount-soak 8h`.
mount-soak DURATION='8h':
    go run ./cmd/astrostack mount soak \
        -duration {{DURATION}} \
        -motion {{ env_var_or_default("MOTION", "none") }} \
        -report "$(pwd)/output/mount-soak-$(date -u +%Y%m%dT%H%M%SZ).txt"

# Serve the local vision model for the finish supervisor (host; first run downloads ~28 GB).
run-ia-model:
    @scripts/ia-model.sh

# Stop a backgrounded model server (run-ia-model is normally foreground; Ctrl-C there).
stop-ia-model:
    @pkill -f mlx_vlm.server || true

# Health-check the local model server.
ia-model-status:
    @curl -fsS "http://127.0.0.1:${ASTRO_LLM_PORT:-1234}/health" && echo " OK"

# Serve the host GraXpert on demand so the containerized engine can offload denoise / background
# extraction to a NATIVE process (Docker on macOS can't reach the host binary or the GPU). Foreground;
# then set ASTRO_GRAXPERT_URL=http://host.docker.internal:8083 for `just stack` and restart the engine.
run-graxpert-service:
    go run ./cmd/graxpert-host

# Health-check the host GraXpert service.
graxpert-service-status:
    @curl -fsS "http://127.0.0.1:${ASTRO_GRAXPERT_PORT:-8083}/health" && echo " OK"

# Inspect a capture directory and print the inventory (host).
inspect DIR:
    go run ./cmd/astrostack inspect "{{DIR}}"

# Build the OFFLINE light-pollution atlas from the David Lorenz model (accurate, propagation-modeled sky
# brightness — rural France reads Bortle 2-3, not 4-5) into the data dir. Downloaded once; every per-site /
# finder / map query is then fully offline. REGION: france (default) | europe | world (or use the CLI's
# --bbox for a custom area). Power-user alternative (pre-gridded URL / Falchi GeoTIFF via gdal): the script.
update-light-pollution-data REGION="france":
    go run ./cmd/astrostack lightpollution-atlas --region "{{REGION}}"

# Legacy/power-user offline atlas via a pre-gridded URL or a Falchi/VIIRS GeoTIFF (needs gdal+jq); configure
# ASTRO_LIGHTPOLLUTION_ATLAS_URL or ..._TIFF_URL in .env. See scripts/update-light-pollution.sh.
update-light-pollution-data-custom:
    @scripts/update-light-pollution.sh

# Build the OFFLINE tree-canopy-height atlas for the dark-sky finder's tree-aware horizon (a spot hemmed in
# by forest then scores its low southern horizon correctly). Point ASTRO_CANOPY_ATLAS_TIFF_URL at an ETH/Meta
# canopy-height GeoTIFF (or /vsicurl/ URL) in .env; optional ASTRO_CANOPY_BBOX + ASTRO_CANOPY_RES_DEG (default
# ~90 m). Needs gdal + jq. Optional + soft-fails; restart the engine to load it. See scripts/update-canopy.sh.
update-canopy-data:
    @scripts/update-canopy.sh

# Rebuild the frontend sky-map dataset (frontend/src/assets/skymap.json): the star + constellation-line
# figures the GoTo "Find it in the sky" map renders. Fetches the HYG catalogue + Stellarium constellations
# (network at build time ONLY); the app then renders the sky fully offline. MAG = faintest star (default 6.0
# = naked-eye limit). Re-run only to refresh the data or change density; commit the regenerated JSON.
gen-skymap-data MAG="6.0":
    go run ./cmd/astrostack skymap-data --mag "{{MAG}}"

# Rebuild the embedded deep star catalogue (internal/deepstars/catalogue/hyg_mag9.csv.gz) the
# star-annotation endpoint uses for name labels (proper/Bayer/Flamsteed/HD). Fetches the HYG database
# (network at generation time ONLY; same source pin as gen-skymap-data). MAG = faintest star kept.
# Re-run only to refresh or change depth; commit the regenerated file.
gen-deepstars-data MAG="9.0":
    go run ./cmd/astrostack deepstars-data --mag "{{MAG}}"

# One-time download of the DEEP star catalogue (ATHYG v3.2 — Tycho-2 + Gaia DR3 + HYG names; ~200 MB
# of CSV → ~130 MB .bin, ~15 s to convert) into library/catalogues. The embedded extract stops at
# magnitude 9, so a typical eleventh-magnitude detection is anonymous and has no distance; ATHYG
# names it, gives it a spectral type, and gives the parallax the 3D field map places it with. It also
# feeds the plate-solve check: on sparse fields the magnitude-9 set left only 2-5 usable check stars
# and the solve failed validation outright. Optional — absent, everything falls back to the embedded
# extract. MAG drops stars fainter than the limit (0 = keep all).
download-deepstars MAG="0":
    go run ./cmd/astrostack deepstars-athyg --mag "{{MAG}}"

# One-time download of Siril's OFFLINE Gaia plate-solve catalogue (~1.1 GB → ~3 GB) into
# library/catalogues — makes plate-solving (and therefore SPCC colour calibration) work with no
# network, on the host AND in the Docker engine (same files via the /data/library volume).
download-catalogues:
    @scripts/download-catalogues.sh

# Also fetch the offline SPCC xp_sampled chunks (~5 GB, 48 files). Optional: without them SPCC
# falls back to the online Gaia archive (needs network at run time).
download-catalogues-spcc:
    @scripts/download-catalogues.sh --spcc

# Run the full auto pipeline (host). MODE: deepsky|nebula|milkyway|planetary|comet  FORMAT: image|video|both
# e.g. just process deepsky image ~/Astro/M31   ·   just process planetary video ~/Astro/moon.mp4
process MODE FORMAT PATH *args:
    go run ./cmd/astrostack process {{args}} {{MODE}} {{FORMAT}} "{{PATH}}"

# Re-run the finish via the local AI agent on an existing run dir — no re-stack. e.g. just refine output/M101/<runID>
refine RUNDIR *args:
    go run ./cmd/astrostack refine {{args}} "{{RUNDIR}}"

# Process a lunar/planetary video (host).
video FILE *args:
    go run ./cmd/astrostack video {{args}} "{{FILE}}"

# Record a demo video of the web UI from a YAML scenario (host; needs the app running — see just dev/web).
# Generate a scenario with the /demo-video Claude command. e.g. just demo overview · just demo tour --headless
demo scenario="tour" *args:
    @scripts/demo.sh {{scenario}} {{args}}

# Regenerate the in-app help-tour screenshots into frontend/public/tour/ (host; needs the app running).
# Re-run whenever the UI changes, then commit frontend/public/tour/. e.g. just tour-shots --locales en
tour-shots *args:
    @scripts/tour-shots.sh {{args}}

# Run the Siril MCP server in the foreground (manual testing).
mcp-siril:
    go run ./cmd/siril-mcp

# Build the Siril MCP binary into ./bin (used by .mcp.json).
build-mcp:
    @mkdir -p {{bin}}
    go build -o {{bin}}/siril-mcp ./cmd/siril-mcp

# Build the host GraXpert service binary into ./bin.
build-graxpert-host:
    @mkdir -p {{bin}}
    go build -o {{bin}}/graxpert-host ./cmd/graxpert-host

# Build all binaries + the frontend (build identity stamped via ldflags — shows in /api/health,
# every run record, and the UI's engine chip, so a stale-engine run is identifiable at a glance).
build:
    @mkdir -p {{bin}}
    go build -ldflags "-X github.com/verove-jordan/astronomy/internal/buildinfo.Version=$(git describe --tags --always --dirty) -X github.com/verove-jordan/astronomy/internal/buildinfo.BuiltAt=$(date -u +%Y-%m-%dT%H:%MZ)" -o {{bin}}/astrostack ./cmd/astrostack
    go build -o {{bin}}/siril-mcp ./cmd/siril-mcp
    @test -d frontend/node_modules && (cd frontend && pnpm build) || true

# Run the Go tests (on host; they exercise host Siril). Pass-through args: just test ./internal/grade
test *args:
    go test {{ if args == "" { "./..." } else { args } }}

# Lint + format-check + type-check (read-only; fails on issues).
lint:
    go vet ./...
    golangci-lint run
    @test -d frontend/node_modules && (cd frontend && pnpm vue-tsc --noEmit) || true

# Auto-format the codebase in place.
fmt:
    gofmt -w .
    @test -d frontend/node_modules && (cd frontend && pnpm format) || true

# The pre-push gate: lint + test.
check: lint test

# Refresh the gitnexus code-graph (incremental). Run before impact/context queries on fresh edits.
gitnexus-sync:
    @.claude/hooks/gitnexus-sync.sh

# Force a full gitnexus re-index (after big refactors or a branch switch).
gitnexus-reindex:
    @.claude/hooks/gitnexus-sync.sh --force

# Show the gitnexus index status for this repo.
gitnexus-status:
    @gitnexus status

# Tail compose logs.
logs:
    docker compose logs -f

# Open a psql shell in the db container.
sh:
    docker compose exec db psql -U "${POSTGRES_USER:-astro}" -d "${POSTGRES_DB:-astrostack}"

# Remove containers, volumes, and build artifacts. DESTRUCTIVE.
[confirm("This drops the Postgres volume and build artifacts. Continue?")]
clean:
    docker compose down -v
    rm -rf {{bin}} tmp frontend/dist
