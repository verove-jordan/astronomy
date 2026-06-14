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

# Inspect a capture directory and print the inventory (host).
inspect DIR:
    go run ./cmd/astrostack inspect "{{DIR}}"

# Run the full auto pipeline on a capture directory (host). Flags after DIR, e.g. -v --out ~/done
process DIR *args:
    go run ./cmd/astrostack process {{args}} "{{DIR}}"

# Process a lunar/planetary video (host).
video FILE *args:
    go run ./cmd/astrostack video {{args}} "{{FILE}}"

# Run the Siril MCP server in the foreground (manual testing).
mcp-siril:
    go run ./cmd/siril-mcp

# Build the Siril MCP binary into ./bin (used by .mcp.json).
build-mcp:
    @mkdir -p {{bin}}
    go build -o {{bin}}/siril-mcp ./cmd/siril-mcp

# Build all binaries + the frontend.
build:
    @mkdir -p {{bin}}
    go build -o {{bin}}/astrostack ./cmd/astrostack
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
