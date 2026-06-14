# astronomy

<!-- BEGIN auto-conventions (managed by /create-dev-project) -->
## Coding conventions (load on demand)

House coding conventions live in `./conventions/`. They are **not** auto-imported — to keep
each prompt lean, READ the matching file (with the Read tool) the first time a task touches
that area, before writing or reviewing code there:

- Writing a **commit** message → `conventions/commit-conventions.md`
- **SQL** schema, migrations, queries, naming → `conventions/database-conventions.md`
- **Docker / Compose**, or building/running anything in containers → `conventions/docker-conventions.md`
- Writing or reviewing **Go** (handlers, errors, concurrency) → `conventions/golang-conventions.md`
- `just` recipes / task-runner work → `conventions/justfile-conventions.md`
- Writing or updating the **README** → `conventions/readme-conventions.md`
- **Tailwind** / styling / design-system work → `conventions/tailwind-conventions.md`
- Writing or updating **tests** → `conventions/testing-conventions.md`
- **Vue 3 / Pinia / TS** components or stores → `conventions/vuejs-conventions.md`

A `UserPromptSubmit` hook auto-loads the convention(s) it can detect from your prompt, so the
right rules are usually already in context; read any it missed using the list above. Edit the
files in `./conventions/`, or re-run `/create-dev-project` to add more. Put project-specific /
business rules BELOW this block (hoist a single line here if you want it always on).
<!-- END auto-conventions -->

## Project rules (AstroStack)

AstroStack auto-sorts and stacks astrophotography captures (Takahashi FC-100 DF + ZWO ASI 1600MM Pro,
filters L/R/G/B/Ha) and drives **Siril** + **GIMP** to produce a final image. See
`docs/architecture.md` and the approved plan for the full design.

**Language policy.** All code we write is **Go** (engine, CLI, Siril MCP) or **Vue 3 + TypeScript**
(web UI). The only Python in the repo is the **vendored GIMP MCP** at `mcp-servers/gimp/server.py`
(GIMP's own scripting is Python/Scheme — it is copied as-is, not authored here). Do not add new Python.

**Host-engine exception to the docker-always convention.** Siril and GIMP are host-installed macOS
apps that cannot run in a Linux container — the same reason the GIMP MCP runs on the host. Therefore:

- The Go engine (`astrostack`: CLI, HTTP API, worker) and the Siril MCP (`siril-mcp`) **run on the
  host** via `just`, and connect to the containerized Postgres at `localhost:5432`.
- **Go tests run on the host** (they exercise host `siril-cli`); only Postgres is provided by Compose.
- Docker Compose runs the **support/stateful** services only: `db` (Postgres), `frontend` (prod), and
  `adminer` (profile `tools`).

This deviation is intentional; keep it documented here and in the README.

**Siril/GIMP integration.** Drive host `siril-cli` (default
`/Applications/Siril.app/Contents/MacOS/siril-cli`, override via `SIRIL_BIN`) with generated `.ssf`
scripts; parse its `progress:`/`log:`/`status:` lines for job progress. GIMP is reached via the
vendored MCP (`GIMP_BIN=/Applications/GIMP.app/Contents/MacOS/gimp-console-2.10`) or `gimp-console`
batch for the automated pipeline.

**Persistence.** Postgres via `pgx/v5` + `sqlc`; versioned SQL migrations via `golang-migrate`.
Per the house DB convention, `created_at`/`updated_at` are **int64 millisecond** timestamps; durations
(exposure) are stored in ms and temperatures in milli-°C to stay integer-clean.
