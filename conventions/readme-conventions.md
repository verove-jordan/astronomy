# README Conventions

Every project has a `README.md` at its root, and it is **kept current**. The
README is the front door — written for someone who has never seen the project.
A stale README is a bug: update it in the **same change** that alters setup,
usage, configuration, or architecture (commit type `docs`, or folded into the
feature commit).

## Principles

- **Front-loaded.** First screen answers *what is this*, *why*, and *how do I run it* — before any internals.
- **Runnable.** Every command shown must actually work, copy-paste, today. No aspirational or stale commands. Prefer `just` recipes so commands have one source of truth (see `justfile-conventions.md`).
- **DRY / index-not-manual.** The README is a quickstart + map. When a section outgrows ~20 lines, move it to `docs/` and link. Never duplicate content that lives elsewhere.
- **Minimal prerequisites.** Because everything runs in containers (`docker-conventions.md`), the prereqs should be ~just Docker + `just`. If a reader needs a host toolchain, that's a smell.
- **Honest.** Document current behavior, not intentions. Mark known limitations explicitly.

## Required structure (in order)

1. **Title + one-line description** — what it is and who it's for. This line doubles as the repo's GitHub "About".
2. **Short context** — 2–4 sentences: the problem it solves and the approach. No marketing fluff.
3. **Quickstart** — the fastest copy-pasteable path from clone to running. Ends with the URL/command that proves it works.
4. **Prerequisites** — the few things needed on the host (ideally Docker + `just`).
5. **Usage** — the common `just` recipes as a table; real examples with expected output where it helps.
6. **Configuration** — env vars as a table (name · default · description); point to `.env.example`; state that secrets are never committed.
7. **Architecture** — a few lines + the service/module list (or a small diagram); link to `docs/` for depth.
8. **Development** — how to test/lint (`just check`), where conventions live, how to contribute.
9. **Deployment** *(if applicable)* — how it ships.
10. **License** — SPDX identifier.

Sections 9 and parts of 7–8 are optional for small/internal projects; 1–6 are not.

## Quality bar (production-grade)

- Title line works as a standalone elevator pitch.
- Quickstart works on a clean machine with only the listed prerequisites.
- Every env var the app reads is in the Configuration table and in `.env.example`.
- Commands reference `just` recipes, not raw `docker compose …` duplicated across docs.
- No broken links; no references to files/commands that no longer exist.
- Badges (CI, license) only if they reflect reality — a red/stale badge is worse than none.

## Skeleton

```markdown
# <Project Name>

> One sentence: what this is and who it's for.

<2–4 sentences on the problem it solves and how it approaches it.>

## Quickstart

\`\`\`bash
git clone <repo-url> && cd <repo>
cp .env.example .env     # then fill in secrets
just up                  # build + start everything in Docker
\`\`\`

Open http://localhost:<port>.

## Prerequisites

- Docker + Docker Compose
- [just](https://github.com/casey/just)

Everything else runs in containers — nothing is installed on the host.

## Usage

| Command | Does |
|---|---|
| `just` | List all tasks |
| `just up` / `just down` | Start / stop the stack |
| `just test` | Run the test suite |
| `just lint` | Lint + type-check |
| `just logs [svc]` | Tail logs |

## Configuration

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | HTTP port |
| `DATABASE_URL` | — | Postgres DSN |

See `.env.example`. Never commit real secrets.

## Architecture

<3–5 lines describing the services/modules and how they fit. Link to docs/ for detail.>

## Development

- House conventions live in `./conventions/` (auto-loaded by Claude Code).
- Run `just check` before pushing — it mirrors CI.

## License

<SPDX-License-Identifier, e.g. MIT>
```
