# S3 storage

Captures, results, the calibration library and full backups can mirror to any S3-compatible
storage (AWS, MinIO, Scaleway, …) so local disk stays small without losing data. Everything is
soft-fail and verified: **no local file is ever deleted without confirming its copy on S3 first.**
Code: `internal/api/s3.go` + `s3conn.go`, `internal/transfer`, `internal/job/stager.go` +
`libpuller.go`, `internal/libmirror`, `internal/backup`, `internal/secret`.

## Connections & secrets

Two ways to give the engine credentials:

- **UI connections** (Processing → Storage): endpoint / access key / secret saved in Postgres,
  with the secret **AES-256-GCM encrypted at rest** (`internal/secret`). The master key comes from
  `ASTRO_ENCRYPTION_KEY` (base64, 32 bytes) or an auto-generated key file kept **outside** the
  backup roots — so a database dump alone can never leak usable credentials. The secret is
  decrypted only to build a client and is **never returned to the UI or logged**. One connection
  is the *default*; it drives the pipeline and all jobs.
- **Env fallback** (`ASTRO_S3_*` — see [configuration.md](configuration.md)) for headless setups.

Bucket and key prefix are per-request UI state, so several buckets/prefixes can coexist. A plain
**bucket file manager** (create/delete buckets, browse/move/upload/download objects) lives on the
same page.

## Mirror layout

Everything lives under the user-chosen prefix:

| Key | Contents |
|---|---|
| `<prefix>/data/<relative-path>` | capture-data mirror (a classified ledger maps local files to keys) |
| `<prefix>/output/<relative-path>` | finished stacks + run reports |
| `<prefix>/library/<file>` | calibration masters — flat `master_*` / `phone_master_*` FITS plus their `.sig` reuse signatures and `_defects.lst` bad-pixel maps. The multi-GB `catalogues/` subtree is **never** mirrored |
| `<prefix>/backup/<stamp>/` | full snapshots (see below) |

## Transfers (Storage tab / `POST /api/s3/transfer`)

Four operations, each running as a job in a dedicated transfer lane (they never starve processing
jobs), parallel per file (default 6, `ASTRO_S3_CONCURRENCY`), resumable via pause:

- **Upload / Sync** — copy folders up; sync skips objects that already match.
- **Download** — restore folders locally.
- **Free local** (`removeLocal`) — the space-saver: every file is **verified on S3
  (content check) before its local copy is deleted**; a folder with any unverifiable file is
  left untouched.

Freed data stays fully usable: the browser merges S3-only folders into the Import browser, and
previews/results serve **local-first with an S3 fallback** — a freed file is fetched into a
regenerable serve cache (`work/cache/s3/…`) on demand.

## Full-S3 processing

A job launched with storage mode **"Full S3 (free local after)"** pulls its inputs from the
mirror, runs locally (the engine itself never streams pixels from S3), pushes inputs + outputs
back, then frees the local copies (verified). With `ASTRO_S3_LOW_DISK=true` (default) the pull is
**staged in waves** — calibration frames first, then one channel's lights at a time, freed right
after that channel stacks — so peak local disk stays around one channel's worth of frames.
Transient S3 errors auto-pause the job (it resumes without recomputing; already-pushed files are
skipped).

## Library mirror

“Copy library to S3” (Library tab / `POST /api/library/s3-sync`) mirrors the calibration masters.
From then on the library behaves like a synced cache: when a run **matches** a master that is
absent locally, the engine pulls just that file (and its `_defects.lst` sidecar) on demand right
before Siril reads it, and frees transiently-pulled copies after the run. A machine that has never
built a dark can still calibrate with the pool's deep master.

## Backup & restore

`POST /api/backup` snapshots to `<prefix>/backup/<stamp>/`, componentwise (each soft-fails
independently; a manifest written last marks completeness):

| Component | Contents |
|---|---|
| `db.dump` | full Postgres dump (`pg_dump -Fc`) — catalog, jobs, presets, connections |
| `library.tar` | the calibration masters (catalogues excluded) |
| `atlas/` | the offline light-pollution atlas |
| `appstate.json` | **browser-only** state — favorites, setups, preferences, AstroAgent chats — assembled by the UI (`GET /api/backup/appstate` + client IndexedDB) and re-applied browser-side on restore |

**Restore is destructive** for the database (`pg_restore --clean --if-exists`) — it replaces the
current catalog/jobs with the snapshot. `.env` secrets are never included in backups.

## Safety properties worth knowing

- Credentials: UI secrets encrypted at rest, never echoed; env credentials never logged.
- Deletes: only after per-file S3 verification; aborted folder-wide otherwise.
- Serving: output URLs transparently fall back to the mirror when the local file was freed.
- Pool hygiene: a catalogued frame whose file was freed is skipped from master pools with a
  counted warning (never a dangling-symlink stack failure).
