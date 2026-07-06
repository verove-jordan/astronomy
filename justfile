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

# Serve the local vision model for the finish supervisor (host; first run downloads ~26 GB).
run-ia-model:
    @scripts/ia-model.sh

# Stop a backgrounded model server (run-ia-model is normally foreground; Ctrl-C there).
stop-ia-model:
    @pkill -f mlx_vlm.server || true

# Health-check the local model server.
ia-model-status:
    @curl -fsS "http://127.0.0.1:${ASTRO_LLM_PORT:-1234}/health" && echo " OK"

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

# One-time download of Siril's OFFLINE Gaia plate-solve catalogue (~1.1 GB → ~3 GB) into
# library/catalogues — makes plate-solving (and therefore SPCC colour calibration) work with no
# network, on the host AND in the Docker engine (same files via the /data/library volume).
download-catalogues:
    @scripts/download-catalogues.sh

# Also fetch the offline SPCC xp_sampled chunks (~5 GB, 48 files). Optional: without them SPCC
# falls back to the online Gaia archive (needs network at run time).
download-catalogues-spcc:
    @scripts/download-catalogues.sh --spcc

# Run the full auto pipeline (host). MODE: deepsky|nebula|milkyway|planetary  FORMAT: image|video|both
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

# Run the Siril MCP server in the foreground (manual testing).
mcp-siril:
    go run ./cmd/siril-mcp

# Build the Siril MCP binary into ./bin (used by .mcp.json).
build-mcp:
    @mkdir -p {{bin}}
    go build -o {{bin}}/siril-mcp ./cmd/siril-mcp

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
