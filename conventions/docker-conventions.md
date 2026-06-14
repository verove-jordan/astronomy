# Docker & Compose Conventions

## Core rule — everything runs in a container

**Every app, service, and dependency runs inside a Docker container, orchestrated
by Docker Compose at the project root.** Do not install language runtimes,
databases, or build tools on the host. Do not run build / test / lint / run
commands on the host — run them through the container.

```
docker compose run --rm <service> <command>     # one-off (build, test, lint, migrate)
docker compose exec <service> <command>          # against a running service
```

A thin wrapper script (e.g. `dexec <service> <cmd>`) that forwards to the above is encouraged so commands stay short.

## Project layout

- One `compose.yaml` at the repo root. No top-level `version:` key (Compose v2).
- One **service per app/process**; each service builds from its own `Dockerfile`.
- A `.dockerignore` next to every `Dockerfile` (exclude `.git`, deps, build output, secrets).
- Local dev overrides in `compose.override.yaml` (auto-merged) or behind a `--profile`.

## Dockerfile

- **Pin the base image** to a specific tag (or digest). Never `FROM node:latest` in a committed Dockerfile. Prefer `-slim` / `-alpine` / `distroless` runtimes.
- **Multi-stage builds**: a `builder` stage compiles/installs; the final stage copies only the artifact into a minimal runtime image.
- **Layer caching**: copy dependency manifests (`go.mod`, `package.json`, `pyproject.toml`/`uv.lock`) and install deps *before* copying source, so code changes don't bust the dependency layer.
- **Run as non-root**: create a user and `USER app`. Don't run the process as root.
- **No secrets in layers**: never `COPY` a `.env` or key; pass secrets at runtime (env, secret mounts). Secrets baked into a layer are recoverable.
- Add a `HEALTHCHECK`, `EXPOSE` the port explicitly, and use exec-form `CMD ["bin","arg"]`.

```dockerfile
FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /app ./cmd/server

FROM gcr.io/distroless/static-debian12
COPY --from=builder /app /app
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app"]
```

## Compose

- Name services for their role (`api`, `worker`, `db`, `cache`).
- `depends_on` with `condition: service_healthy` — rely on healthchecks, not start order.
- **Named volumes** for persistent data (databases); never bind-mount DB data into the repo.
- **Bind-mount source** into app services in dev for hot reload; not in production images.
- Configuration via `.env` (committed `.env.example`, real `.env` git-ignored). Never commit secrets.
- `profiles:` for optional services (e.g. `tools`, `e2e`) so the default `up` stays lean.
- Internal `networks` for service-to-service traffic; publish only the ports a human needs.

## Don'ts

- No host-installed toolchains; no `go`/`pnpm`/`python` run directly on the host.
- No `latest` base tags committed; no running as root; no secrets baked into images or committed `.env`.
- No `docker run` one-offs for project work — express it as a Compose service so it's reproducible.
