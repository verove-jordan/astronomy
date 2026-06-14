# Justfile Conventions

Every project has a **`justfile` at its root**. `just` is the single, memorable
entry point for every common task — a new contributor runs `just` and sees
everything they can do. Recipes are **thin wrappers** that delegate to Docker
Compose (see `docker-conventions.md`), scripts, or tools — never large embedded
logic.

## Why just (not Make / npm scripts / loose shell)

- Recipes are documented and self-listing (`just --list`); no `.PHONY`, no tab traps.
- One vocabulary across every project regardless of language — muscle memory transfers.
- `just check` and `just ci` make **local == CI**, so "works on my machine" stops happening.

## Canonical recipe vocabulary

Use these names so every repo feels the same. Omit what doesn't apply; never rename these.

| Recipe | Purpose |
|---|---|
| `default` | Runs `just --list`. A bare `just` shows the menu. |
| `setup` / `bootstrap` | First-run setup: copy `.env`, build images, init deps. Idempotent. |
| `up` / `down` | Start / stop the Compose stack. |
| `dev` / `run` | Run the app for local development (foreground, hot-reload). |
| `build` | Build images / compile artifacts. |
| `test` | Run the test suite (in-container). Accepts pass-through args. |
| `lint` | Lint + format-check + type-check. Read-only; fails on issues. |
| `fmt` | Auto-format the codebase in place. |
| `check` | `lint` + `test` — the pre-push gate. |
| `ci` | Exactly what CI runs. Keep it equal to the pipeline. |
| `migrate` / `seed` | DB migrations / seed data. |
| `logs` / `sh` | Tail logs / open a shell in a service. |
| `clean` | Remove artifacts, volumes, caches. **Destructive → `[confirm]`.** |

## Rules

- **`default` lists recipes.** A bare `just` must never do real work.
- **Every recipe carries a `#` doc comment** on the line above — it shows in `just --list`.
- **Thin recipes.** More than ~5 lines or real branching → move it to `scripts/` and call that.
- **Run through the container.** Per the docker-always rule, `test`/`lint`/`migrate` use `docker compose run --rm <svc> …`; never invoke host toolchains.
- **Config at the top** as `just` variables; load env with `set dotenv-load`. Private helpers are prefixed `_`.
- **Pass-through args** with `*args` (e.g. `just test -k name`); positional with `set positional-arguments`.
- **Destructive recipes gate on `[confirm(...)]`.** Anything that drops volumes/data must prompt.
- **No secrets in the justfile** — read from `.env`/environment.
- **`set shell := ["bash", "-uc"]`** so undefined vars fail loudly; multi-line recipes use a `#!/usr/bin/env bash` shebang + `set -euo pipefail`.

## Skeleton (fill the `<…>` per stack)

```just
# Task runner for this project. Run `just` to see available recipes.
set shell := ["bash", "-uc"]
set dotenv-load := true       # load .env into recipe environment
set export := true            # export just-vars to recipes

svc := "app"                  # primary service for run/test/lint

# List all recipes.
default:
    @just --list

# First-run setup: env file + build images. Safe to re-run.
setup:
    test -f .env || cp .env.example .env
    docker compose build

# Build images and start the stack (detached).
up:
    docker compose up -d --build

# Stop and remove containers.
down:
    docker compose down

# Run the app for development (foreground).
dev:
    docker compose up

# Run the test suite. Extra args pass through: `just test -k name`.
test *args:
    docker compose run --rm {{svc}} <test-cmd> {{args}}

# Lint + format-check + type-check. Fails on any issue.
lint:
    docker compose run --rm {{svc}} <lint-cmd>

# Auto-format the codebase in place.
fmt:
    docker compose run --rm {{svc}} <format-cmd>

# Pre-push gate — mirrors CI exactly.
check: lint test
ci: check

# Apply database migrations.
migrate *args:
    docker compose run --rm {{svc}} <migrate-cmd> {{args}}

# Tail logs (all services, or one: `just logs app`).
logs service="":
    docker compose logs -f {{service}}

# Shell into a service container.
sh service=svc:
    docker compose exec {{service}} bash

# Remove containers, volumes, and artifacts. Destructive.
[confirm("Remove volumes and build artifacts? [y/N]")]
clean:
    docker compose down -v --remove-orphans
```

The `just` recipe names are the project's command surface — the README and CI both reference them, so they are the single source of truth for "how do I run X".
