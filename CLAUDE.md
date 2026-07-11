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

**Full-container mode (`stack` profile) coexists with the exception.** Because every tool path is a
config env var, the engine also runs **entirely in Docker** with no Go changes — `just stack` builds an
`engine` image (`docker/engine.Dockerfile`) that bakes in the **Linux** builds of Siril 1.4.x (AppImage,
pinned for output parity) / GIMP 2.10 / GraXpert / ffmpeg, plus `frontend` + `db`. nginx templates its
`/api` upstream (`API_UPSTREAM`, default host, `engine:8080` under `stack`). The finish-supervisor VLM is
**decoupled/opt-in**: `stack` never pulls it; on Linux+GPU the `ai` profile (Ollama) serves it in a
container, on macOS it stays native (`just run-ia-model`) since Docker has no Metal. Host-dev (above)
stays the faster daily macOS path; `stack` is the portable/Linux-server/parity path. **StarNet++ is not
baked in** (licence not redistributable) — mount it + set `STARNET_BIN`; it soft-fails to full stars.
See `docs/architecture.md` → "Fully containerized mode".

**Siril/GIMP integration.** Drive host `siril-cli` (default
`/Applications/Siril.app/Contents/MacOS/siril-cli`, override via `SIRIL_BIN`) with generated `.ssf`
scripts; parse its `progress:`/`log:`/`status:` lines for job progress. GIMP is reached via the
vendored MCP (`GIMP_BIN=/Applications/GIMP.app/Contents/MacOS/gimp-console-2.10`) or `gimp-console`
batch for the automated pipeline.

**Optional astro-AI tools (same host-engine model).** The pipeline can drive two more host-installed,
open-source CLIs the same way it drives `siril-cli` (`os/exec`, stream stdout, parse `%`): **GraXpert**
(`GRAXPERT_BIN`, AI background-gradient extraction on the linear masters, ahead of a gentle Siril `subsky` cleanup) and
**StarNet++** (`STARNET_BIN`, star removal — for star-reduced finishing in GIMP). They are **invoked,
never vendored or bundled** (their AGPL/free licences stay with the user's own install, exactly like
GPL Siril/GIMP). Both are **optional**: when the binary is absent the run logs a warning and falls back
(Siril subsky / full stars). Runners live in `internal/graxpert` and `internal/starnet`; the pipeline
wiring (soft-fail) is in `internal/pipeline/enhance.go`. Per-mode toggles are `mode.Preset.BackgroundAI`
and `mode.Preset.StarReduce`. `astrostack process --no-ai` skips both. **Do not add new Python** for
these — they are external binaries.

**Finish supervisor (opt-in local-AI agent) is mode-generic.** The `--supervise` run option (+ the
post-run **Refine** panel) drives a host vision model to render → judge → re-tune → keep-best, and now
covers **every** stacking mode, not just deep-sky. The loop (`internal/pipeline/supervise.go`,
`measureFinish`/`scoreFinish`/`critique`) is mode-agnostic; each mode plugs in a `candidateRenderer`
(`supervise_renderer.go`) that owns its cheap **re-finish** and tunable knobs: deepsky/nebula
(`supervise_deepsky.go`, the staged GIMP reentry, Tier A/B/C), comet (`supervise_comet.go`, re-combine
the star/comet masters), milkyway (`supervise_nightscape.go`, `nightscape.Regrade` over persisted linear
sky/fg), planetary (`supervise_planetary.go`, re-run `planetary.Refinish` over the persisted masters — no
re-stack/re-deconv). New modes re-finish only (single-stage tierA); Tier C re-stack stays deep-sky-only.
Post-run refine dispatches by mode in `refine.go` (`RefineExistingRun`). To add a mode: implement
`candidateRenderer` + persist its re-finish inputs, then gate the call on `superviseOn` in that mode's
pipeline entry. Everything soft-falls to the standard finish; a run never fails because of the agent.

**Persistence.** Postgres via `pgx/v5` + `sqlc`; versioned SQL migrations via `golang-migrate`.
Per the house DB convention, `created_at`/`updated_at` are **int64 millisecond** timestamps; durations
(exposure) are stored in ms and temperatures in milli-°C to stay integer-clean.

**iPhone DNG calibration (milkyway).** Phone darks/bias/flats live in a **separate** library from the
deep-sky Siril masters — table `phone_calib_masters` + `internal/calib/phone.go` (`PhoneMaster`,
`MatchPhoneCalibration`), keyed by **ISO + exposure + sensor dimensions + camera model** (not
gain/offset/bin) because they are applied **per-pixel in Go, in linear light** by the nightscape recipe,
never by Siril `calibrate`. Kept apart so the deep-sky matcher (which ignores dimensions) can never pick
a phone master. DNG metadata (ISO/exposure/model/dims/orientation) is read by `internal/rawmeta` — a pure-Go
TIFF/EXIF IFD parser with an `mdls` fallback (macOS-only; Linux `stack` soft-fails to folder/filename
classification). `inspect.ClassifyRawStills` auto-detects dark/flat/bias among raw stills (folder/filename
tokens first, then a `sips`-downscaled pixel-stats pass reusing `classifyByStats`). Calibration runs in
**sensor-native** space before orientation; masters are **dimension-guarded** (`matchOrDrop` +
`sameSensor`) so a mismatched-resolution master is dropped, never applied. A milkyway run with cal frames
auto-builds+persists masters (`nightscape/library.go`); later runs auto-reuse them by ISO/dims. NOTE: the
optional **true-linear develop** (`LINEAR_RAW_BIN`, physically-exact subtraction) is **not** implemented —
it needs a raw-develop binary validated against real ProRAW; today all DNGs develop via `sips` (lights and
cal share the transform, so subtraction still cancels fixed-pattern/thermal signal).

**`info.txt` sidecars + heterogeneous combine.** Older captures have bare filenames (no
filter/gain/type); a hand-written `info.txt`/`info.txt.txt` next to them lists the capture order — one
filter token per chronological capture sub-run — plus gain/exposure/temp (e.g. `LLL RR GG BB Ha Ha` /
`gain L200 RGB250 Ha300`). `internal/inspect/manifest.go` parses it and back-fills frames as a **fallback
only** (header/filename always win; it never overrides). To combine sessions of one target shot at
**different gain and/or orientation**, drop them under one folder (e.g. `input/M101/`) and inspect it:
same-filter light sets at different gain become separate groups, each calibrated with its **own
gain-matched masters** (the library keys on `g{gain}o{offset}_b{bin}`). A session shot through a different
optical train (e.g. a star diagonal) is **mirror-flipped** and can never be aligned by rotation — so each
group is first **parity-normalized** (`parityCache` in `reuse_process.go`: plate-solve one frame `-noflip`,
read the sign of `det(CD)` = `CDELT1·CDELT2·det(PC)` — Siril 1.4.3 stores PC+CDELT, not CD — and `mirrorx`
any group not at the East-left `det<0` convention). Groups are then co-registered (homography,
**`-framing=min`** = common field-of-view) and stacked **`-weight=wfwhm`** — see
`pipeline/reuse_process.go` `processChannelGroups`. The single-session path is byte-identical. Jobs over
the same input dir are **serialized** (`job.Manager.lockTarget`) and master writes are atomic
(temp+rename in `calib`) so concurrent runs never corrupt the shared `library/`.

**S3 storage.** Captures/results can mirror to S3 (import/process/results + sync + backup-everything) so
local disk stays free without data loss. **S3 credentials** come from either a **UI-managed connection**
(Processing → Storage) — endpoint/access-key/secret entered in the UI and stored in Postgres with the
**secret AES-256-GCM encrypted at rest** (master key `ASTRO_ENCRYPTION_KEY` or an auto-generated key file
kept OUTSIDE the backup roots; `internal/secret`) — **or** the legacy env `ASTRO_S3_*`. The **default**
connection drives the pipeline (`s.s3Config(ctx)` / `m.s3ConfigResolved(ctx)` resolve default-connection →
env). The **secret is decrypted only to build a client and NEVER returned to the UI or logged**
(`store.S3Connection.SecretEnc` is `json:"-"`). Bucket + prefix are per-request UI state. Mirror layout
under `<prefix>`: `data/`, `output/`, `backup/<stamp>/`. Transfers/backups/restores are **jobs** (own worker
lane), `removeLocal` **verifies each file on S3 before deleting local** (aborts the folder otherwise), and
freed previews/results serve local-first with an S3 fallback (frontend must tag file/preview/thumb URLs via
`services/api.ts` `s3Suffix`/`withS3`). Full-S3 process mode pulls inputs → runs (engine stays local-only)
→ pushes → frees. Backup gathers **browser-only** state (favorites/setups + AI-chat IndexedDB) UI-side
(`utils/appstate.ts`); `.env` secrets are excluded. See `docs/architecture.md` → "S3 storage".

**Code graph (gitnexus).** This repo is indexed by **gitnexus** (a code knowledge-graph; binary
`gitnexus`, MCP server `gitnexus mcp`). Use it as the FIRST move for any "where / what / what-breaks"
question about Go (`cmd/`, `internal/`) or Vue/TS (`frontend/src`) — it is faster and more accurate
than grep:

- "Where is X defined / called?" → `mcp__gitnexus__context({name:"X"})`
- "Where is the logic for concept Y?" → `mcp__gitnexus__query({query:"Y"})`
- "What breaks if I change Z?" → `mcp__gitnexus__impact({target:"Z"})`
- "Do my uncommitted edits drift from the graph?" → `mcp__gitnexus__detect_changes`

Reach for `Grep`/`Glob` only when gitnexus returns nothing or the target is non-code text (i18n keys,
`.ssf` Siril scripts, SQL, config, markdown). For a deep multi-step exploration, delegate to the
`gitnexus-search` subagent rather than running many queries inline.

**Sync the graph before you implement.** The graph must reflect current code before you reason about
a change. Hooks keep it trailing-fresh automatically — a `SessionStart` staleness check (re-index if
>2h old) plus a debounced background re-index on each source edit (`Edit`/`Write`/`MultiEdit` on
`*.go`/`*.vue`/`*.ts`/`*.sql`…). But when you need a **guaranteed**-fresh graph at the start of a code
task — especially right after `git pull`, a branch switch, or a burst of edits — run
**`just gitnexus-sync`** (incremental; `just gitnexus-reindex` forces a full rebuild) and let it finish
*before* your impact/context queries. The index lives in `.gitnexus/` (gitignored, local); scope is set
by `.gitnexusignore`, which keeps the ~32 GB `input/` capture data and the vendored GIMP MCP out of the
graph. The `mcp__gitnexus__*` tools come from the **host-global** `gitnexus mcp` server (registered in
`~/.claude.json`, shared across all your repos) — `gitnexus mcp` serves every indexed repo, so always
pass `repo:"astronomy"` to disambiguate from the other indexed repos. The binary lives in the active
nvm Node's `bin`; if Node is upgraded, re-point that global server (`claude mcp` / `gitnexus setup`).
