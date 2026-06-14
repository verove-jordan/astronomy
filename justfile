# AstroStack task runner. Thin wrappers only.
# NOTE (host-engine exception, see CLAUDE.md): the Go engine, the Siril MCP and the Go tests
# run on the HOST (they drive host Siril/GIMP). Docker Compose runs Postgres (+ tools) only.

set dotenv-load := true
set shell := ["bash", "-uc"]
set positional-arguments := true

bin := "./bin"
siril_bin := env_var_or_default("SIRIL_BIN", "/Applications/Siril.app/Contents/MacOS/siril-cli")
db_url := env_var_or_default("DATABASE_URL", "postgres://astro:astro@localhost:5432/astrostack?sslmode=disable")

# List available recipes.
default:
    @just --list

# First-run setup: .env, Go deps, dev tools, MCP binary, frontend deps. Idempotent.
setup:
    @test -f .env || cp .env.example .env
    go mod download
    @command -v golangci-lint >/dev/null || brew install golangci-lint
    go install github.com/air-verse/air@latest
    go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
    go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
    just build-mcp
    @test -d frontend/node_modules || (cd frontend && pnpm install) 2>/dev/null || true
    @echo "Setup done. Next: just up && just migrate"

# Start Postgres (compose).
up:
    docker compose up -d db

# Start Postgres + Adminer DB UI (http://localhost:${ADMINER_PORT:-8081}).
tools:
    docker compose --profile tools up -d

# Stop the compose stack.
down:
    docker compose down

# Apply database migrations (against the compose Postgres).
migrate:
    migrate -path migrations -database "{{db_url}}" up

# Roll back the last migration.
migrate-down:
    migrate -path migrations -database "{{db_url}}" down 1

# Create a new migration pair: just migrate-create add_foo
migrate-create name:
    migrate create -ext sql -dir migrations -seq {{name}}

# Regenerate type-safe DB code from SQL (sqlc).
sqlc:
    sqlc generate

# Run the API server on the host with hot reload (drives host Siril/GIMP).
dev:
    air

# Run the frontend dev server on the host (Vite).
web:
    cd frontend && pnpm dev

# Inspect a capture directory and print the inventory (host).
inspect DIR:
    go run ./cmd/astrostack inspect "{{DIR}}"

# Run the full auto pipeline on a capture directory (host).
process DIR *args:
    go run ./cmd/astrostack process "{{DIR}}" {{args}}

# Process a lunar/planetary video (host).
video FILE *args:
    go run ./cmd/astrostack video "{{FILE}}" {{args}}

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
    @test -d frontend && (cd frontend && pnpm build) || true

# Run tests (Go on host; frontend unit tests). Accepts pass-through args: just test ./internal/grade
test *args:
    go test {{ if args == "" { "./..." } else { args } }}
    @test -d frontend/node_modules && (cd frontend && pnpm test --run) || true

# Lint + format-check + type-check (read-only; the pre-push gate's first half).
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
