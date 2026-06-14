# Commit Message Conventions

Conventional-commits style. Issue-tracker references are optional — include them
only if the project uses a tracker.

## Format

```
<type>(<scope>): <short description> [<TICKET-ID>]

<optional body — e.g. the issue URL, or context on the change>
```

Examples:

```
perf(ingest): optimize batch cronjob for 10M+ candidate throughput

feat(auth): add password-reset flow [PROJ-142]

https://tracker.example.com/issue/PROJ-142
```

## Rules

- Subject in **imperative mood**, max ~72 characters (excluding the `[TICKET]` tag).
- Subject is a distilled summary of what the change does — not a copy-paste of a ticket title.
- One blank line between subject and body. Body is optional; use it for the issue URL or non-obvious context.
- The `[TICKET-ID]` tag and issue URL are optional. Omit them on personal projects or anything without a tracker.

## Commit Types

| Type | When to use |
|------|-------------|
| `feat` | New feature or capability that did not exist before |
| `fix` | Bug fix, crash fix, incorrect-behavior correction |
| `perf` | Performance improvement, no functional change |
| `refactor` | Restructuring with no functional or performance change |
| `chore` | Maintenance (deps, config, tooling) with no production-code change |
| `docs` | Documentation only |
| `test` | Adding/updating tests, no production-code change |
| `style` | Formatting/whitespace/lint, no logic change |
| `build` / `ci` | Build system or CI pipeline changes |

**Type priority when ambiguous:** describes a bug → `fix`; mentions optimize/performance/throughput → `perf`; adds a user-facing capability → `feat`. Otherwise pick the type matching the primary intent.

## Scope

A short lowercase token (no spaces); 1–2 words, hyphen-joined if needed:

1. Changes concentrated in one package/dir → that name (`auth`, `ingest`, `labels`).
2. Card/feature clearly names an area → that (`stored-request`, `tree-select`).
3. Changes span many areas equally → the component name (`frontend`, `backend`, `api`).
